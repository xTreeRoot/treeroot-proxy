"""验证电脑出口的管理员边界、一次性配对、凭据隔离及停用行为。"""
import base64
import io
import json
import time
import unittest
import uuid
import zipfile
from unittest.mock import patch
from fastapi.testclient import TestClient
import test_app


class EgressTests(unittest.TestCase):
    setUpClass = classmethod(test_app.PanelTests.setUpClass.__func__)
    tearDownClass = classmethod(test_app.PanelTests.tearDownClass.__func__)
    setUp = test_app.PanelTests.setUp

    def create(self):
        return self.client.post('/api/egress', headers=self.headers, json={
            'name': 'My Mac', 'browser': {'platform': 'MacIntel', 'platformName': 'macOS',
                                        'platformVersion': '26.3.1', 'architecture': 'arm', 'bitness': '64'}}).json()

    def pair(self, device):
        self.panel.CONFIG.update(egress_ssh_host='192.0.2.1', egress_known_hosts='192.0.2.1 ssh-ed25519 test')
        response = self.client.post('/api/egress/' + device['id'] + '/setup', headers=self.headers, json={})
        self.assertEqual(response.status_code, 200)
        with zipfile.ZipFile(io.BytesIO(response.content)) as archive:
            setup = json.loads(archive.read('setup.json'))
            self.assertEqual(set(archive.namelist()), {'setup.json', 'connector.py', 'egress_proxy.py', '使用说明.txt', '连接.command', '连接.cmd'})
        blob = b'\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20' + b'a' * 32
        body = {'token': setup['token'], 'public_key': 'ssh-ed25519 ' + base64.b64encode(blob).decode()}
        paired = self.client.post('/api/egress/' + device['id'] + '/pair', json=body)
        self.assertEqual(paired.status_code, 200)
        return body, paired.json()

    def test_admin_csrf_and_secret_isolation(self):
        anonymous = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(anonymous.close)
        self.assertEqual(anonymous.get('/api/egress').status_code, 401)
        self.assertEqual(self.client.post('/api/egress', json={'name': 'Denied'}).status_code, 403)
        first, second = self.create(), self.create()
        self.assertNotEqual(first['id'], second['id'])
        for secret in ('access_code', 'proxy_password', 'agent_hash', 'pair_hash', 'public_key', 'port'):
            self.assertNotIn(secret, first)
        with self.panel.database(readonly=True) as db:
            before = tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone())
        self.panel.initialize()
        self.assertEqual(len(self.client.get('/api/egress').json()), 2)
        with self.panel.database(readonly=True) as db:
            self.assertEqual(tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone()), before)

    def test_pairing_single_use_heartbeat_and_disable(self):
        device = self.create()
        body, paired = self.pair(device)
        path = '/api/egress/' + device['id']
        self.assertEqual(self.client.post(path + '/pair', json=body).status_code, 403)
        self.assertEqual(self.client.post(path + '/heartbeat', json={}).status_code, 403)
        headers = {'Authorization': 'Bearer ' + paired['agent_token']}
        self.assertEqual(self.client.post(path + '/heartbeat', headers=headers, json={}).status_code, 200)
        self.assertTrue(self.client.get('/api/egress').json()[0]['online'])
        registry = json.loads((self.panel.egress_manager.directory / 'registry.json').read_text())
        self.assertEqual(registry['devices'][0]['proxy']['password'], paired['proxy_password'])
        self.assertEqual(registry['devices'][0]['port'], paired['remote_port'])
        self.client.patch(path, headers=self.headers, json={'enabled': False})
        self.assertEqual(self.client.post(path + '/heartbeat', headers=headers, json={}).status_code, 403)
        self.assertFalse(self.client.get('/api/egress').json()[0]['online'])
        self.assertFalse(json.loads((self.panel.egress_manager.directory / 'registry.json').read_text())['devices'][0]['enabled'])

    def test_expiry_and_bad_keys_are_rejected(self):
        device = self.create()
        body, _ = self.pair(device)
        with self.panel.database() as db:
            db.execute('UPDATE egress_devices SET pair_hash=?,pair_expires=? WHERE id=?',
                       (self.panel.egress_manager.config.get('missing', 'expired'), int(time.time()) - 1, device['id']))
        self.assertEqual(self.client.post('/api/egress/' + device['id'] + '/pair', json=body).status_code, 403)
        body['public_key'] = 'ssh-ed25519 ' + 'a' * 64
        self.assertEqual(self.client.post('/api/egress/' + device['id'] + '/pair', json=body).status_code, 422)

    def test_ip_check_requires_connected_device_and_does_not_report_server_ip(self):
        device = self.create()
        path = '/api/egress/' + device['id']
        self.assertEqual(self.client.post(path + '/test', headers=self.headers, json={}).status_code, 409)
        _, paired = self.pair(device)
        self.client.post(path + '/heartbeat', headers={'Authorization': 'Bearer ' + paired['agent_token']}, json={})
        with patch('egress.subprocess.run') as execute:
            execute.return_value.stdout = '203.0.113.10'
            response = self.client.post(path + '/test', headers=self.headers, json={})
            self.assertEqual(response.json()['ip'], '203.0.113.10')
            self.assertEqual(execute.call_args.args[0], ['/usr/bin/curl', '--config', '-'])
            self.assertIn('proxy-user', execute.call_args.kwargs['input'])

    def test_delete_revokes_access_and_restore_requires_pairing(self):
        device = self.create()
        _, paired = self.pair(device)
        path = '/api/egress/' + device['id']
        headers = {'Authorization': 'Bearer ' + paired['agent_token']}
        self.assertEqual(self.client.request('DELETE', path, headers=self.headers, json={}).status_code, 200)
        self.assertEqual(self.client.get('/api/egress').json(), [])
        self.assertEqual(len(self.client.get('/api/egress?deleted=true').json()), 1)
        self.assertEqual(self.client.post(path + '/heartbeat', headers=headers, json={}).status_code, 404)
        self.assertEqual(json.loads((self.panel.egress_manager.directory / 'registry.json').read_text())['devices'], [])
        self.assertEqual(self.client.post(path + '/restore', headers=self.headers, json={}).status_code, 200)
        restored = self.client.get('/api/egress').json()[0]
        self.assertFalse(restored['enabled'])
        self.assertFalse(restored['paired'])

    def test_connection_records_are_authenticated_deduplicated_and_isolated(self):
        first, second = self.create(), self.create()
        _, paired = self.pair(first)
        path = '/api/egress/' + first['id'] + '/heartbeat'
        record = {'event_key': uuid.uuid4().hex, 'ts': int(time.time()), 'domain': 'example.org',
                  'port': 443, 'method': 'CONNECT', 'outcome': 'success', 'upload': 123,
                  'download': 456, 'duration_ms': 789}
        headers = {'Authorization': 'Bearer ' + paired['agent_token']}
        self.assertEqual(self.client.post(path, json={'records': [record]}).status_code, 403)
        for _ in range(2):
            response = self.client.post(path, headers=headers, json={'records': [record]})
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.json()['accepted'], [record['event_key']])
        result = self.client.get('/api/egress/records', params={'kind': 'request'}).json()
        self.assertEqual(result['total'], 1)
        item = result['items'][0]
        self.assertEqual((item['device_id'], item['domain'], item['upload'], item['download']),
                         (first['id'], 'example.org', 123, 456))
        overview = self.client.get('/api/egress/overview').json()
        self.assertEqual((overview['connections'], overview['succeeded'], overview['upload'], overview['download']),
                         (1, 1, 123, 456))
        for secret in ('event_key', 'agent_hash', 'proxy_password', 'access_code', 'public_key'):
            self.assertNotIn(secret, item)
        other_path = '/api/egress/' + second['id'] + '/heartbeat'
        self.assertEqual(self.client.post(other_path, headers=headers, json={'records': [record]}).status_code, 403)
        for invalid in [record | {'upload': -1}, record | {'detail': 'secret password'},
                        record | {'domain': 'https://example.org/path?token=secret'}]:
            self.assertEqual(self.client.post(path, headers=headers, json={'records': [invalid]}).status_code, 422)
        self.client.patch('/api/egress/' + first['id'], headers=self.headers, json={'enabled': False})
        self.assertEqual(self.client.post(path, headers=headers, json={'records': [record]}).status_code, 403)
        anonymous = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(anonymous.close)
        self.assertEqual(anonymous.get('/api/egress/records').status_code, 401)

    def test_overview_empty_authentication_and_validation(self):
        anonymous = TestClient(self.panel.app, base_url='https://testserver')
        self.addCleanup(anonymous.close)
        path = '/api/egress/overview'
        self.assertEqual(anonymous.get(path).status_code, 401)
        empty = self.client.get(path).json()
        for key in ('upload', 'download', 'connections', 'succeeded', 'online', 'available'):
            self.assertEqual(empty[key], 0)
        self.assertEqual((empty['devices'], empty['domains'], empty['device_options']), ([], [], []))
        self.assertIsNone(empty['last_record'])
        self.assertEqual(len(empty['series']), 24)
        self.assertTrue(all(point['upload'] == point['download'] == point['connections'] == 0 for point in empty['series']))
        self.assertEqual(self.client.get(path, params={'device_id': 'missing'}).status_code, 404)
        for value in (0, 1, 25, 169, 'invalid'):
            self.assertEqual(self.client.get(path, params={'hours': value}).status_code, 422)
        self.assertEqual(len(self.client.get(path, params={'hours': 168}).json()['series']), 168)

    def test_overview_windows_devices_deleted_history_and_consistent_totals(self):
        first, second, idle = self.create(), self.create(), self.create()
        removed = self.client.request('DELETE', '/api/egress/' + second['id'], headers=self.headers, json={})
        self.assertEqual(removed.status_code, 200)
        now = int(time.time())
        records = [
            (first['id'], now - 86400 - 1, 'older.test', 'success', 10, 20),
            (first['id'], now - 86400, 'example.org', 'success', 100, 200),
            (first['id'], now - 1, 'failed.test', 'failed', 30, 40),
            (first['id'], now, 'example.org', 'success', 5, 6),
            (second['id'], now - 3600, 'example.org', 'success', 7, 8),
            (second['id'], now - 7 * 86400, 'oldest.test', 'failed', 11, 12),
            (first['id'], now - 7 * 86400 - 1, 'expired.test', 'success', 9999, 9999),
            (first['id'], now + 1, 'future.test', 'success', 9999, 9999),
        ]
        with self.panel.database() as db:
            db.execute('UPDATE egress_devices SET public_key=?,last_seen=? WHERE id=?', ('fixture', now, first['id']))
            db.executemany('''INSERT INTO egress_events
                (event_key,device_id,ts,kind,action,domain,outcome,upload,download)
                VALUES(?,?,?,'request','connection',?,?,?,?)''',
                [(uuid.uuid4().hex, *record) for record in records])
            db.execute('''INSERT INTO egress_events(event_key,device_id,ts,kind,action,outcome,upload,download)
                VALUES(?,?,?,'device','ip_test','success',10000,10000)''', (uuid.uuid4().hex, first['id'], now))
            before = tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone())
        with patch('egress.time.time', return_value=now):
            day = self.client.get('/api/egress/overview').json()
            week = self.client.get('/api/egress/overview', params={'hours': 168}).json()
            selected = self.client.get('/api/egress/overview', params={'device_id': first['id']}).json()
            deleted = self.client.get('/api/egress/overview', params={'device_id': second['id']}).json()
            idle_data = self.client.get('/api/egress/overview', params={'device_id': idle['id']}).json()
        self.assertEqual((day['upload'], day['download'], day['connections'], day['succeeded']), (142, 254, 4, 3))
        self.assertEqual((week['upload'], week['download'], week['connections'], week['succeeded']), (163, 286, 6, 4))
        self.assertEqual((day['online'], day['available'], day['last_record']), (1, 2, now))
        self.assertEqual(day['domains'][0], {'domain': 'example.org', 'connections': 3, 'upload': 112, 'download': 214})
        self.assertEqual((selected['upload'], selected['download'], selected['connections']), (135, 246, 3))
        self.assertEqual([item['id'] for item in selected['devices']], [first['id']])
        self.assertEqual(len(selected['device_options']), 3)
        self.assertTrue(deleted['devices'][0]['deleted'])
        self.assertEqual((deleted['connections'], deleted['online'], deleted['available']), (1, 0, 0))
        self.assertEqual((idle_data['connections'], idle_data['upload'], idle_data['download']), (0, 0, 0))
        for data in (day, week, selected, deleted, idle_data):
            self.assertEqual(data['until'] - data['since'], data['hours'] * 3600)
            self.assertEqual(len(data['series']), data['hours'])
            for key in ('upload', 'download', 'connections'):
                self.assertEqual(sum(item[key] for item in data['series']), data[key])
                self.assertEqual(sum(item[key] for item in data['devices']), data[key])
            for item in data['devices'] + data['device_options']:
                for secret in ('access_code', 'proxy_password', 'agent_hash', 'pair_hash', 'public_key', 'port'):
                    self.assertNotIn(secret, item)
        with self.panel.database(readonly=True) as db:
            self.assertEqual(tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone()), before)

    def test_record_pagination_filtering_deleted_history_and_retention(self):
        first, second = self.create(), self.create()
        now = int(time.time())
        with self.panel.database() as db:
            db.executemany('''INSERT INTO egress_events(event_key,device_id,ts,kind,action,outcome,domain,port)
                VALUES(?,?,?,'request','connection',?,?,443)''',
                [(uuid.uuid4().hex, first['id'] if i % 2 else second['id'], now,
                  'failed' if i % 3 else 'success', 'example.org' if i % 2 else 'other.test') for i in range(137)])
            db.execute('''INSERT INTO egress_events(event_key,device_id,ts,kind,action,outcome)
                VALUES(?,?,?,'device','created','success')''', (uuid.uuid4().hex, first['id'], now - 8 * 86400))
        path = '/api/egress/records'
        first_page = self.client.get(path).json()
        self.assertEqual((first_page['total'], first_page['pages'], len(first_page['items'])), (139, 6, 25))
        with self.panel.database() as db:
            self.panel.egress_manager.event(db, self.panel.egress_manager.row(first['id'], db), 'enabled')
        seen = [item['id'] for item in first_page['items']]
        for page in range(2, 7):
            response = self.client.get(path, params={'page': page, 'before_id': first_page['before_id']}).json()
            self.assertEqual(response['total'], 139)
            seen.extend(item['id'] for item in response['items'])
        self.assertEqual(len(seen), len(set(seen)))
        self.assertEqual(seen, sorted(seen, reverse=True))
        filtered = self.client.get(path, params={'device_id': first['id'], 'kind': 'request',
            'outcome': 'failed', 'search': 'example', 'page_size': 100}).json()
        self.assertGreater(filtered['total'], 0)
        self.assertTrue(all(item['device_id'] == first['id'] and item['outcome'] == 'failed' for item in filtered['items']))
        self.assertEqual(self.client.get(path, params={'search': '%'}).json()['total'], 0)
        self.assertEqual(self.client.get(path, params={'start': now + 1}).json()['total'], 0)
        self.assertEqual(self.client.get(path, params={'page': 999}).json()['page'], 6)
        self.assertEqual(self.client.get(path, params={'search': 'absent', 'page': 999}).json()['page'], 1)
        for params in [{'page': 0}, {'page_size': 101}, {'outcome': 'unknown'}, {'start': now, 'end': now - 1}]:
            self.assertEqual(self.client.get(path, params=params).status_code, 422)
        self.client.delete('/api/egress/' + first['id'], headers=self.headers)
        self.assertGreater(self.client.get(path, params={'device_id': first['id']}).json()['total'], 0)
        self.panel.egress_manager.maintain()
        with self.panel.database() as db:
            self.assertEqual(db.execute('SELECT COUNT(*) FROM egress_events WHERE ts<?', (now - 7 * 86400,)).fetchone()[0], 0)
            self.assertEqual(db.execute('PRAGMA integrity_check').fetchone()[0], 'ok')

    def test_device_events_and_offline_transition_are_not_repeated(self):
        device = self.create()
        _, paired = self.pair(device)
        path = '/api/egress/' + device['id'] + '/heartbeat'
        headers = {'Authorization': 'Bearer ' + paired['agent_token']}
        for _ in range(2):
            self.assertEqual(self.client.post(path, headers=headers, json={}).status_code, 200)
        with self.panel.database() as db:
            db.execute('UPDATE egress_devices SET last_seen=? WHERE id=?', (int(time.time()) - 100, device['id']))
        self.panel.egress_manager.maintain()
        self.panel.egress_manager.maintain()
        self.client.post(path, headers=headers, json={})
        actions = [item['action'] for item in self.client.get('/api/egress/records').json()['items']]
        self.assertEqual(actions.count('connected'), 2)
        self.assertEqual(actions.count('disconnected'), 1)
        for action in ('created', 'setup_downloaded', 'paired'):
            self.assertIn(action, actions)
        with self.panel.database() as db:
            original = dict(db.execute('SELECT * FROM egress_devices WHERE id=?', (device['id'],)).fetchone())
            before = tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone())
            event_count = db.execute('SELECT COUNT(*) FROM egress_events').fetchone()[0]
        self.panel.initialize()
        with self.panel.database() as db:
            self.assertEqual(dict(db.execute('SELECT * FROM egress_devices WHERE id=?', (device['id'],)).fetchone()), original)
            self.assertEqual(tuple(db.execute('SELECT client_uuid,token,upload,download FROM users WHERE id=1').fetchone()), before)
            self.assertEqual(db.execute('SELECT COUNT(*) FROM egress_events').fetchone()[0], event_count)
