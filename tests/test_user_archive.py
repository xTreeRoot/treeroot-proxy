"""Recoverable user deletion against the real Go HTTP API and disposable Xray fixture."""
import http.client
import json
import os
from pathlib import Path
import select
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
GO_PANEL = os.environ.get('TREEROOT_GO_PANEL_BIN')
BASE = 'https://testserver'


@unittest.skipUnless(GO_PANEL, 'set TREEROOT_GO_PANEL_BIN for native Go tests')
class UserArchiveTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.folder = Path(temporary.name)
        self.database = self.folder / 'panel.sqlite3'
        self.failure = self.folder / 'fail-xray'
        fake = self.folder / 'xray'
        fake.write_text('#!' + sys.executable + '\nimport sys\nfrom pathlib import Path\n'
                        'if Path(' + repr(str(self.failure)) + ').exists(): sys.exit(1)\n'
                        'print("Added 1 user(s)" if "adu" in sys.argv else "Removed 1 user(s)" if "rmu" in sys.argv else "{}")\n')
        fake.chmod(0o700)
        self.xray = self.folder / 'xray.json'
        self.xray.write_text(json.dumps({'inbounds': [{'tag': 'websocket-in', 'settings': {'clients': []}}],
                                         'outbounds': [], 'routing': {'rules': []}, 'log': {}}))
        config = self.folder / 'config.json'
        config.write_text(json.dumps(dict(base_url=BASE, data_dir=str(self.folder), admin_username='admin',
            admin_password_hash='', initial_uuid='fixture-uuid', initial_token='fixture-token',
            ws_path='/fixture-ws', xray_config=str(self.xray), xray_binary=str(fake))))
        self.env = dict(os.environ, TREEROOT_CONFIG=str(config))
        subprocess.run([sys.executable, '-c', '''
import time
import app
app.CONFIG['admin_password_hash']=app.password_hash('fixture-password')
app.initialize()
with app.database() as db:
    db.execute('UPDATE users SET upload=123,download=456 WHERE id=1')
    db.execute("INSERT INTO domains VALUES(1,'example.test',9,?)",(int(time.time()),))
    db.execute("INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,1,'example.test',443,'tcp')",(int(time.time()),))
    db.execute('INSERT INTO traffic VALUES(?,1,123,456)',(int(time.time()),))
    db.execute('INSERT INTO user_limits VALUES(1,2000,3000,400000,1)')
'''], cwd=ROOT, env=self.env, check=True)
        self.start()
        self.addCleanup(self.stop)
        status, headers, data = self.request('/api/login', method='POST', body={
            'username': 'admin', 'password': 'fixture-password'})
        self.assertEqual(status, 200)
        self.auth = {'Cookie': headers['set-cookie'].split(';', 1)[0],
                     'X-CSRF-Token': json.loads(data)['csrf'], 'Origin': BASE}

    def start(self):
        self.process = subprocess.Popen([str(Path(GO_PANEL).resolve()), '-listen', '127.0.0.1:0',
            '-collect=false', '-limits='], cwd=ROOT, env=self.env, stdout=subprocess.PIPE)
        if not select.select([self.process.stdout], [], [], 15)[0]:
            raise RuntimeError('Go fixture startup timed out')
        self.address = self.process.stdout.readline().decode().strip()
        if not self.address.startswith('127.0.0.1:'):
            raise RuntimeError('Go fixture failed to start')

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            self.process.wait(timeout=5)
        self.process.stdout.close()

    def request(self, path, method='GET', body=None, headers=None):
        connection = http.client.HTTPConnection(self.address, timeout=5)
        try:
            headers = dict(headers or {})
            if method not in ('GET', 'HEAD') and body is None:
                body = {}
            if body is not None:
                body = json.dumps(body).encode()
                headers['Content-Type'] = 'application/json'
            connection.request(method, path, body, headers)
            response = connection.getresponse()
            return response.status, {k.lower(): v for k, v in response.getheaders()}, response.read()
        finally:
            connection.close()

    def snapshot(self):
        with sqlite3.connect(self.database) as db:
            return {name: db.execute(query).fetchall() for name, query in {
                'identity': 'SELECT id,name,client_uuid,email,token,created,upload,download FROM users ORDER BY id',
                'admin': 'SELECT * FROM admin', 'limits': 'SELECT * FROM user_limits',
                'domains': 'SELECT * FROM domains', 'traffic': 'SELECT * FROM traffic',
                'events': 'SELECT * FROM events', 'usage': 'SELECT * FROM limit_usage',
            }.items()}

    def test_delete_restore_preserves_identity_history_and_stays_disabled(self):
        before = self.snapshot()
        self.assertEqual(self.request('/api/users/1', method='DELETE')[0], 401)
        bad_csrf = dict(self.auth, **{'X-CSRF-Token': 'wrong'})
        self.assertEqual(self.request('/api/users/1', method='DELETE', headers=bad_csrf)[0], 403)
        self.assertEqual(self.request('/api/users/1', method='DELETE', headers=self.auth)[0], 200)
        self.assertEqual(json.loads(self.request('/api/users', headers=self.auth)[2]), [])
        archived = json.loads(self.request('/api/users?deleted=true', headers=self.auth)[2])
        self.assertEqual(len(archived), 1)
        self.assertTrue(archived[0]['deleted'])
        self.assertEqual(archived[0]['enabled'], 0)
        self.assertEqual(archived[0]['upload'] + archived[0]['download'], 579)
        self.assertEqual(self.snapshot(), before)
        for path, method, body in [('/api/users/1', 'PATCH', {'enabled': True}),
                ('/api/users/1/reset', 'POST', {}), ('/api/users/1/limits', 'GET', None)]:
            self.assertEqual(self.request(path, method=method, body=body, headers=self.auth)[0], 404)
        self.assertEqual(self.request('/sub/fixture-token/clash.yaml')[0], 404)
        overview = json.loads(self.request('/api/overview', headers=self.auth)[2])
        self.assertEqual((overview['upload'], overview['download'], overview['users']), (123, 456, 0))
        self.assertEqual(json.loads(self.request('/api/activity', headers=self.auth)[2])['total'], 1)
        self.stop()
        self.start()  # The compatible migration is repeatable and keeps the archive across restarts.
        self.assertEqual(len(json.loads(self.request('/api/users?deleted=true', headers=self.auth)[2])), 1)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.request('/api/users/1/restore', method='POST', headers=self.auth)[0], 200)
        restored = json.loads(self.request('/api/users', headers=self.auth)[2])[0]
        self.assertFalse(restored['deleted'])
        self.assertEqual(restored['enabled'], 0)
        self.assertEqual(self.request('/sub/fixture-token/clash.yaml')[0], 404)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.request('/api/users/1', method='PATCH', body={'enabled': True}, headers=self.auth)[0], 200)
        self.assertEqual(self.request('/sub/fixture-token/clash.yaml')[0], 200)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(json.loads(self.request('/api/users?deleted=true', headers=self.auth)[2]), [])

    def test_xray_failure_rolls_back_user_archive_and_configuration(self):
        before, config = self.snapshot(), self.xray.read_bytes()
        self.failure.touch()
        self.assertEqual(self.request('/api/users/1', method='DELETE', headers=self.auth)[0], 503)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.xray.read_bytes(), config)
        users = json.loads(self.request('/api/users', headers=self.auth)[2])
        self.assertEqual(len(users), 1)
        self.assertEqual(users[0]['enabled'], 1)
        self.assertEqual(json.loads(self.request('/api/users?deleted=true', headers=self.auth)[2]), [])
        self.assertEqual(self.request('/sub/fixture-token/clash.yaml')[0], 200)
