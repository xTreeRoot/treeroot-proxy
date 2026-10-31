import importlib.util
import json
import os
import sqlite3
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

from fastapi.testclient import TestClient

ROOT = Path(__file__).resolve().parents[1]


class PanelTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        config = Path(cls.temporary.name) / 'config.json'
        config.write_text(json.dumps({'base_url': 'https://testserver', 'admin_username': 'admin',
                                     'admin_password_hash': '', 'initial_uuid': 'fixture-uuid',
                                     'initial_token': 'fixture-token', 'ws_path': '/fixture-ws'}))
        spec = importlib.util.spec_from_file_location('panel_under_test', ROOT / 'app.py')
        cls.panel = importlib.util.module_from_spec(spec)
        with patch.dict(os.environ, TREEROOT_CONFIG=str(config)):
            spec.loader.exec_module(cls.panel)
        cls.initial_hash = cls.panel.password_hash('initial')

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        self.folder = tempfile.TemporaryDirectory(dir=self.temporary.name)
        self.addCleanup(self.folder.cleanup)
        self.panel.DATA = Path(self.folder.name)
        self.panel.DB = self.panel.DATA / 'panel.sqlite3'
        self.panel.CONFIG['admin_password_hash'] = self.initial_hash
        self.panel.RATES.update(sample_time=None, run=None, users={})
        self.panel.STATUS.update(last_poll=None, last_error=None)
        self.panel.RESOURCES.update(sample_time=None, cpu_times=None, data=None)
        self.panel.SERVER.update(sample_time=None, data=None)
        self.panel.initialize()
        self.client = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(self.client.close)
        login = self.client.post('/api/login', json={'username': 'admin', 'password': 'initial'})
        self.assertEqual(login.status_code, 200)
        self.headers = {'x-csrf-token': login.json()['csrf']}

    def seed_events(self):
        now = int(time.time())
        with self.panel.database() as db:
            db.execute("INSERT INTO users(name,client_uuid,email,token,created) VALUES('Second','fixture-2','second@example.test','fixture-2',?)", (now,))
            db.executemany('INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,?,?,?,?)',
                           [(now, 1 if i % 2 else 2, 'google.com' if i % 3 else 'example.org', 443, 'tcp') for i in range(137)])
            db.execute("INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,1,'expired.test',443,'tcp')", (now - 8 * 86400,))

    def test_activity_paginates_all_records_with_stable_anchor(self):
        self.seed_events()
        first = self.client.get('/api/activity').json()
        self.assertEqual((first['total'], len(first['items']), first['pages']), (137, 25, 6))
        anchor = first['before_id']
        with self.panel.database() as db:
            db.execute("INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,1,'new.test',443,'tcp')", (int(time.time()),))
        seen = [r['id'] for r in first['items']]
        for page in range(2, 7):
            result = self.client.get('/api/activity', params={'page': page, 'before_id': anchor}).json()
            self.assertEqual(result['total'], 137)
            seen.extend(r['id'] for r in result['items'])
        self.assertEqual(len(seen), 137)
        self.assertEqual(len(set(seen)), 137)
        self.assertEqual(seen, sorted(seen, reverse=True))
        self.assertEqual(self.client.get('/api/activity').json()['total'], 138)

    def test_activity_filters_empty_last_page_and_invalid_sizes(self):
        self.seed_events()
        filtered = self.client.get('/api/activity', params={'user_id': 1, 'search': 'google', 'page_size': 100}).json()
        self.assertGreater(filtered['total'], 0)
        self.assertTrue(all(r['user_id'] == 1 and r['domain'] == 'google.com' for r in filtered['items']))
        empty = self.client.get('/api/activity', params={'search': 'missing', 'page': 99}).json()
        self.assertEqual((empty['items'], empty['page'], empty['pages']), ([], 1, 1))
        last = self.client.get('/api/activity', params={'page': 99}).json()
        self.assertEqual((last['page'], len(last['items'])), (6, 12))
        for params in [{'page': 0}, {'page_size': 0}, {'page_size': 101}]:
            self.assertEqual(self.client.get('/api/activity', params=params).status_code, 422)

    def test_password_accepts_short_simple_unicode_and_long_values(self):
        current = 'initial'
        for password in ['a', '123', '密码! 空格', 'x' * 600]:
            response = self.client.post('/api/password', headers=self.headers, json={'current': current, 'password': password})
            self.assertEqual(response.status_code, 200)
            other = TestClient(self.panel.app, base_url='https://testserver')
            try:
                self.assertEqual(other.post('/api/login', json={'username': 'admin', 'password': password}).status_code, 200)
            finally:
                other.close()
            current = password
        self.assertEqual(self.client.post('/api/password', headers=self.headers, json={'current': 'wrong', 'password': 'b'}).status_code, 400)

    def test_password_change_revokes_other_sessions_only(self):
        other = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(other.close)
        other.post('/api/login', json={'username': 'admin', 'password': 'initial'})
        self.client.post('/api/password', headers=self.headers, json={'current': 'initial', 'password': 'a'})
        self.assertTrue(self.client.get('/api/me').json()['authenticated'])
        self.assertFalse(other.get('/api/me').json()['authenticated'])

    def test_rates_use_sample_deltas_and_never_restart_totals(self):
        def collect(value, sample_time, run='run-1'):
            with patch.object(self.panel.subprocess, 'check_output', return_value=run), \
                 patch.object(self.panel, 'xray_api', return_value=json.dumps({'stat': [
                     {'name': 'user>>>owner@treeroot.local>>>traffic>>>uplink', 'value': value},
                     {'name': 'user>>>owner@treeroot.local>>>traffic>>>downlink', 'value': value * 2}]})), \
                 patch.object(self.panel.time, 'monotonic', return_value=sample_time):
                self.panel.collect_stats()
            return self.client.get('/api/overview').json()
        self.assertIsNone(collect(100, 10)['speed_upload'])
        overview = collect(150, 15)
        self.assertEqual((overview['speed_upload'], overview['speed_download']), (10, 20))
        self.assertEqual((overview['upload'], overview['download']), (150, 300))
        self.assertIsNone(collect(10000, 20, 'run-2')['speed_upload'])
        self.assertEqual(collect(10050, 25, 'run-2')['speed_upload'], 10)
        self.assertEqual(collect(5, 30, 'run-2')['speed_upload'], 0)
        self.panel.STATUS['last_poll'] = int(time.time()) - 31
        self.assertIsNone(self.client.get('/api/overview').json()['speed_upload'])
        self.panel.STATUS.update(last_poll=int(time.time()), last_error='Failure')
        self.assertIsNone(self.client.get('/api/overview').json()['speed_download'])

    def test_authentication_and_local_chart_resource(self):
        anonymous = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(anonymous.close)
        self.assertEqual(anonymous.get('/api/me').json(), {'authenticated': False})
        self.assertEqual(anonymous.get('/api/activity').status_code, 401)
        response = anonymous.get('/assets/echarts.min.js')
        self.assertEqual(response.status_code, 200)
        self.assertIn('javascript', response.headers['content-type'])
        self.assertEqual(anonymous.get('/assets/unknown').status_code, 404)

    def test_overview_and_rates_do_not_wait_for_proxy_change_lock(self):
        with ThreadPoolExecutor(max_workers=1) as pool:
            for path in ['/api/overview', '/api/rates', '/api/me']:
                self.panel.LOCK.acquire()
                try:
                    future = pool.submit(self.client.get, path)
                    self.assertEqual(future.result(timeout=1).status_code, 200)
                finally:
                    self.panel.LOCK.release()

    def test_busy_mutations_fail_without_queuing_read_requests(self):
        self.panel.LOCK.acquire()
        try:
            response = self.client.patch('/api/users/1', headers=self.headers, json={'name': 'Renamed'})
            self.assertEqual(response.status_code, 503)
            self.assertEqual(self.client.get('/api/rates').status_code, 200)
        finally:
            self.panel.LOCK.release()
        writer = sqlite3.connect(self.panel.DB)
        try:
            writer.execute('BEGIN IMMEDIATE')
            with patch.object(self.panel, 'DB_BUSY_TIMEOUT', 0.05):
                self.assertEqual(self.client.get('/api/me').status_code, 200)
                self.assertEqual(self.client.post('/api/logout', headers=self.headers, json={}).status_code, 503)
        finally:
            writer.rollback()
            writer.close()
        self.assertTrue(self.client.get('/api/me').json()['authenticated'])

    def test_report_and_password_concurrency_is_bounded(self):
        slots = self.panel.REPORT_SLOTS
        for _ in range(8):
            self.assertTrue(slots.acquire(blocking=False))
        try:
            self.assertEqual(self.client.get('/api/overview').status_code, 503)
            self.assertEqual(self.client.get('/api/rates').status_code, 200)
        finally:
            for _ in range(8):
                slots.release()
        for _ in range(2):
            self.assertTrue(self.panel.HASH_SLOTS.acquire(blocking=False))
        try:
            self.assertEqual(self.client.post('/api/login', json={'username': 'admin', 'password': 'initial'}).status_code, 429)
            self.assertEqual(self.client.get('/api/me').status_code, 200)
        finally:
            for _ in range(2):
                self.panel.HASH_SLOTS.release()

    def test_expensive_queries_are_interrupted_and_slots_released(self):
        with patch.object(self.panel, 'READ_TIMEOUT', 0.001):
            with self.assertRaises(sqlite3.OperationalError):
                with self.panel.database(readonly=True) as db:
                    db.execute('WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT SUM(x) FROM n').fetchone()
        with self.panel.database() as db:
            db.executemany('INSERT INTO domains VALUES(1,?,1,?)', [(f'domain-{i}.test', int(time.time())) for i in range(5000)])
        with patch.object(self.panel, 'READ_TIMEOUT', -1):
            self.assertEqual(self.client.get('/api/users').status_code, 503)
        self.assertEqual(self.client.get('/api/users').status_code, 200)

    def test_renaming_never_calls_proxy_and_preserves_identity(self):
        with self.panel.database() as db:
            before = dict(db.execute('SELECT * FROM users WHERE id=1').fetchone())
        with patch.object(self.panel, 'xray_api') as api, patch.object(self.panel, 'save_xray_config') as save:
            response = self.client.patch('/api/users/1', headers=self.headers, json={'name': "User'; --"})
            self.assertEqual(response.status_code, 200)
            api.assert_not_called()
            save.assert_not_called()
        with self.panel.database() as db:
            after = dict(db.execute('SELECT * FROM users WHERE id=1').fetchone())
        self.assertEqual(after, before | {'name': "User'; --"})

    def test_log_batches_and_rotated_cursors_preserve_every_event(self):
        log = self.panel.DATA / 'access.log'
        stamp = time.strftime('%Y/%m/%d %H:%M:%S', time.gmtime())
        line = f'{stamp} accepted tcp:example.test:443 [in -> out] email: owner@treeroot.local\n'
        log.write_text(line * 2501)
        with patch.object(self.panel, 'ACCESS_LOG', log):
            self.panel.collect_access()
            with self.panel.database() as db:
                self.assertEqual(db.execute('SELECT COUNT(*) FROM events').fetchone()[0], 2000)
            log.rename(log.with_name('access.log.1'))
            log.write_text(line * 3)
            self.panel.collect_access()
            self.panel.collect_access()
        with self.panel.database() as db:
            self.assertEqual(db.execute('SELECT COUNT(*) FROM events').fetchone()[0], 2504)
            self.assertEqual(db.execute('SELECT connections FROM domains').fetchone()[0], 2504)
            cursor = json.loads(db.execute("SELECT value FROM state WHERE key='log_cursor'").fetchone()[0])
            self.assertEqual(cursor, [log.stat().st_ino, log.stat().st_size])

    def test_user_aggregate_and_daily_totals_are_accurate(self):
        self.seed_events()
        now = int(time.time())
        with self.panel.database() as db:
            db.executemany('INSERT INTO domains VALUES(?,?,?,?)', [(1, 'a.test', 3, now), (1, 'b.test', 7, now), (2, 'c.test', 11, now)])
            db.executemany('INSERT INTO traffic VALUES(?,?,?,?)', [(now//60*60, 1, 100, 200), (now//60*60, 2, 300, 400)])
        users = self.client.get('/api/users').json()
        self.assertEqual([(u['domains'], u['connections']) for u in users], [(2, 10), (1, 11)])
        overview = self.client.get('/api/overview').json()
        self.assertEqual((overview['day_upload'], overview['day_download']), (400, 600))
        filtered = self.client.get('/api/overview?user_id=1').json()
        self.assertEqual((filtered['day_upload'], filtered['day_download']), (100, 200))

    def test_server_resources_use_cpu_deltas_and_available_memory_with_shared_cache(self):
        proc = {'/proc/stat': 'cpu 100 0 50 850 0 0 0 0 20 0\n',
                '/proc/meminfo': 'MemTotal: 1000 kB\nMemAvailable: 400 kB\nMemFree: 100 kB\n',
                '/proc/uptime': '90061.25 1000.00\n'}
        with patch.object(self.panel.Path, 'read_text', autospec=True, side_effect=lambda path: proc[str(path)]) as read, \
             patch.object(self.panel.os, 'cpu_count', return_value=2), \
             patch.object(self.panel.os, 'getloadavg', return_value=(0.5, 0.3, 0.2)), \
             patch.object(self.panel.shutil, 'disk_usage', return_value=Mock(total=1000, used=250)), \
             patch.object(self.panel.time, 'monotonic', return_value=10) as clock:
            first = self.client.get('/api/resources').json()
            self.assertIsNone(first['cpu_percent'])
            self.assertEqual(first['cpu_cores'], 2)
            self.assertEqual(first['memory'], {'total': 1024000, 'used': 614400, 'percent': 60})
            self.assertEqual(first['disk'], {'total': 1000, 'used': 250, 'percent': 25})
            self.assertEqual(first['uptime_seconds'], 90061)
            self.assertEqual(first['load_average'], [0.5, 0.3, 0.2])
            proc['/proc/stat'] = 'cpu 180 0 70 950 0 0 0 0 90 0\n'
            clock.return_value = 11
            self.assertEqual(self.client.get('/api/resources').json(), first)
            self.assertEqual(read.call_count, 3)
            clock.return_value = 15
            second = self.client.get('/api/resources').json()
            self.assertEqual(second['cpu_percent'], 50)
            proc['/proc/stat'] = 'cpu 1 0 0 2 0 0 0 0\n'
            clock.return_value = 20
            self.assertIsNone(self.client.get('/api/resources').json()['cpu_percent'])

    def test_server_resources_report_unavailable_without_affecting_overview(self):
        with patch.object(self.panel.Path, 'read_text', side_effect=OSError('unavailable')), \
             patch.object(self.panel.os, 'getloadavg', side_effect=OSError('unavailable')), \
             patch.object(self.panel.shutil, 'disk_usage', side_effect=OSError('unavailable')):
            response = self.client.get('/api/resources')
            self.assertEqual(response.status_code, 200)
            for key in ['cpu_percent', 'load_average', 'memory', 'disk', 'uptime_seconds']:
                self.assertIsNone(response.json()[key])
            self.assertEqual(self.client.get('/api/overview').status_code, 200)
        self.client.cookies.clear()
        self.assertEqual(self.client.get('/api/resources').status_code, 401)

    def test_server_information_is_authenticated_cached_and_uses_real_system_fields(self):
        proc = {'/proc/cpuinfo': 'model name : Fixture CPU\n',
                '/proc/net/dev': 'header\nheader\n lo: 1 0 0 0 0 0 0 0 2\n ens17: 100 0 0 0 0 0 0 0 200\n'}
        output = ('Id=xray.service\nActiveState=active\nSubState=running\nMainPID=123\n'
                  'MemoryCurrent=2048\nActiveEnterTimestamp=Sat 2026-10-31 00:00:00 UTC\n\n'
                  'Id=treeroot-backup.timer\nActiveState=active\nSubState=waiting\nMainPID=0\nMemoryCurrent=[not set]\n')
        with patch.object(self.panel.platform, 'uname', return_value=Mock(node='fixture-host', release='fixture-kernel', machine='x86_64', system='Linux')), \
             patch.object(self.panel.platform, 'freedesktop_os_release', return_value={'PRETTY_NAME': 'Fixture Linux'}), \
             patch.object(self.panel.Path, 'read_text', autospec=True, side_effect=lambda path, *a, **k: proc[str(path)]), \
             patch.object(self.panel.Path, 'iterdir', return_value=iter([Path('/proc/1'), Path('/proc/2'), Path('/proc/net')])), \
             patch.object(self.panel.subprocess, 'run', return_value=Mock(stdout=output)) as run:
            first = self.client.get('/api/server')
            self.assertEqual(first.status_code, 200)
            data = first.json()
            self.assertEqual((data['hostname'], data['os'], data['cpu_model'], data['processes']),
                             ('fixture-host', 'Fixture Linux', 'Fixture CPU', 2))
            self.assertEqual(data['interfaces'], [{'name': 'ens17', 'received': 100, 'sent': 200}])
            self.assertEqual((data['services'][0]['pid'], data['services'][0]['memory']), (123, 2048))
            self.assertIsNone(data['services'][1]['memory'])
            self.assertEqual(self.client.get('/api/server').json(), data)
            self.assertEqual(run.call_count, 1)
            for secret in ('admin_password_hash', 'initial_uuid', 'initial_token', 'ws_path'):
                self.assertNotIn(secret, data)
        self.client.cookies.clear()
        self.assertEqual(self.client.get('/api/server').status_code, 401)

    def test_write_csrf_protection_and_resource_cache_headers(self):
        for method, path, body in [('post', '/api/users', {'name': 'test'}),
                                   ('patch', '/api/users/1', {'name': 'test'}),
                                   ('post', '/api/users/1/reset', {}),
                                   ('post', '/api/password', {'current': 'initial', 'password': 'a'}),
                                   ('post', '/api/logout', {})]:
            self.assertEqual(getattr(self.client, method)(path, json=body).status_code, 403)
        self.assertEqual(self.client.get('/api/me').headers['cache-control'], 'no-store')
        self.assertIn('max-age', self.client.get('/assets/echarts.min.js').headers['cache-control'])
        self.assertEqual(self.client.get('/assets/unknown').headers['cache-control'], 'no-store')

    def test_create_toggle_reset_and_failed_change_keep_statistics(self):
        config = Mock()
        config.read_bytes.return_value = b'fixture-config'
        with patch.object(self.panel, 'XRAY_CONFIG', config), \
             patch.object(self.panel, 'xray_api'), patch.object(self.panel, 'add_runtime_user'), \
             patch.object(self.panel, 'save_xray_config') as save:
            created = self.client.post('/api/users', headers=self.headers, json={'name': 'Fixture'})
            self.assertEqual(created.status_code, 200)
            user = created.json()
            uid = user['id']
            with self.panel.database() as db:
                db.execute('UPDATE users SET upload=123,download=456 WHERE id=?', (uid,))
            self.assertEqual(self.client.patch(f'/api/users/{uid}', headers=self.headers, json={'enabled': False}).status_code, 200)
            self.assertEqual(self.client.get('/sub/' + user['clash_url'].split('/sub/')[1]).status_code, 404)
            self.assertEqual(self.client.patch(f'/api/users/{uid}', headers=self.headers, json={'enabled': True}).status_code, 200)
            reset = self.client.post(f'/api/users/{uid}/reset', headers=self.headers, json={})
            self.assertEqual(reset.status_code, 200)
            self.assertNotEqual(reset.json()['clash_url'], user['clash_url'])
            self.assertEqual((reset.json()['upload'], reset.json()['download']), (123, 456))
            save.side_effect = RuntimeError('fixture failure')
            with self.assertLogs('treeroot', level='ERROR'):
                self.assertEqual(self.client.patch(f'/api/users/{uid}', headers=self.headers, json={'enabled': False}).status_code, 503)
            current = self.client.get('/api/users').json()[-1]
            self.assertTrue(current['enabled'])
            self.assertEqual(current['clash_url'], reset.json()['clash_url'])

    def test_subscription_routes_keep_formats_and_reject_invalid_requests(self):
        modern = self.client.get('/sub/fixture-token/clash.yaml')
        legacy = self.client.get('/fixture-token/clash.yaml')
        self.assertEqual(modern.status_code, 200)
        self.assertEqual(modern.content, legacy.content)
        self.assertEqual(modern.json()['proxies'][0]['server'], 'testserver')
        self.assertEqual(self.client.get('/sub/fixture-token/shadowrocket').content, self.client.get('/fixture-token/shadowrocket').content)
        for path in ['/sub/missing/clash.yaml', '/sub/fixture-token/invalid', '/missing/shadowrocket']:
            self.assertEqual(self.client.get(path).status_code, 404)
        self.assertEqual(self.client.patch('/api/users/999999', headers=self.headers, json={'name': 'missing'}).status_code, 404)
        self.assertEqual(self.client.post('/api/users', headers=self.headers, json={'name': '   '}).status_code, 422)


if __name__ == '__main__':
    unittest.main()
