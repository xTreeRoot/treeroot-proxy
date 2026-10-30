import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

ROOT = Path(__file__).resolve().parents[1]


class PanelTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        config = Path(cls.temporary.name) / 'config.json'
        config.write_text(json.dumps({'base_url': 'https://testserver', 'admin_username': 'admin',
                                     'admin_password_hash': '', 'initial_uuid': 'fixture-uuid',
                                     'initial_token': 'fixture-token'}))
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


if __name__ == '__main__':
    unittest.main()
