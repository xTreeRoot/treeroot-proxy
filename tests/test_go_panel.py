import base64
from concurrent.futures import ThreadPoolExecutor
import http.client
import io
import json
import os
from pathlib import Path
import select
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
import zipfile
from contextlib import closing


ROOT = Path(__file__).resolve().parents[1]
GO_PANEL = os.environ.get('TREEROOT_GO_PANEL_BIN')
BASE = 'https://testserver'


@unittest.skipUnless(GO_PANEL, 'set TREEROOT_GO_PANEL_BIN to run Go/Python integration tests')
class GoPanelTests(unittest.TestCase):
    @classmethod
    def start(cls, args, env):
        process = subprocess.Popen(args, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=None)
        cls.addClassCleanup(process.stdout.close)

        def stop():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()

        cls.addClassCleanup(stop)
        if not select.select([process.stdout], [], [], 15)[0]:
            raise RuntimeError('Test server startup timed out')
        address = process.stdout.readline().decode().strip()
        if not address.startswith('127.0.0.1:'):
            raise RuntimeError('Test server failed to start')
        return address

    @classmethod
    def setUpClass(cls):
        temporary = tempfile.TemporaryDirectory()
        cls.addClassCleanup(temporary.cleanup)
        data = Path(temporary.name)
        config = data / 'config.json'
        config.write_text(json.dumps(dict(base_url=BASE, data_dir=str(data), admin_username='admin',
                                          admin_password_hash='', initial_uuid='fixture-uuid',
                                          initial_token='fixture-token', ws_path='/fixture-ws',
                                          egress_ssh_host='192.0.2.1', egress_known_hosts='fixture-host-key')))
        cls.database = data / 'panel.sqlite3'
        env = dict(os.environ, TREEROOT_CONFIG=str(config), TREEROOT_WRITE_QUEUE='')
        cls.python = cls.start([sys.executable, '-c', '''
import os
import socket
import app
import uvicorn

app.CONFIG['admin_password_hash'] = app.password_hash('fixture-password')
app.initialize()
with socket.create_server(('127.0.0.1', 0)) as listener:
    print('127.0.0.1:' + str(listener.getsockname()[1]), flush=True)
    uvicorn.Server(uvicorn.Config(app.app, lifespan='off', access_log=False, log_level='warning')).run(sockets=[listener])
'''], env)
        cls.go = cls.start([str(Path(GO_PANEL).resolve()), '-listen', '127.0.0.1:0',
                            '-collect=false', '-limits='], env)

    def request(self, address, path, *, method='GET', body=None, headers=None):
        headers = dict(headers or {})
        if body is not None:
            body = json.dumps(body).encode()
            headers['Content-Type'] = 'application/json'
        with closing(http.client.HTTPConnection(address, timeout=4)) as client:
            client.request(method, path, body, headers)
            response = client.getresponse()
            return response.status, dict((key.lower(), value) for key, value in response.getheaders()), response.read()

    def login(self):
        status, headers, body = self.request(self.python, '/api/login', method='POST',
                                             body={'username': 'admin', 'password': 'fixture-password'})
        self.assertEqual(status, 200)
        return {'Cookie': headers['set-cookie'].split(';', 1)[0], 'X-CSRF-Token': json.loads(body)['csrf'], 'Origin': BASE}

    def test_native_read_routes_match_python_and_work_without_upstream(self):
        headers = self.login()
        now = int(time.time())
        with closing(sqlite3.connect(self.database)) as db, db:
            db.executemany('INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,1,?,443,?)',
                           [(now, 'example.test', 'tcp'), (now, 'other.test', 'udp'),
                            (now - 8 * 86400, 'expired.test', 'tcp')])
        for path in ('/api/me', '/api/activity', '/api/activity?search=example&user_id=1',
                     '/api/activity?search=missing', '/api/activity?page=99&page_size=1',
                     '/api/activity?page=2&before_id=1', '/api/activity?page=2&page=1',
                     '/api/activity?search=has%20space'):
            for auth in ({}, headers):
                with self.subTest(path=path, authenticated=bool(auth)):
                    reference = self.request(self.python, path, headers=auth)
                    actual = self.request(self.go, path, headers=auth)
                    self.assertEqual(actual[0], reference[0])
                    self.assertEqual(json.loads(actual[2]), json.loads(reference[2]))
                    for name in ('content-type', 'cache-control', 'content-security-policy', 'referrer-policy',
                                 'x-content-type-options'):
                        self.assertEqual(actual[1][name], reference[1][name])
        for path in ('/api/activity?page_size=101', '/api/activity?search=has;semicolon'):
            reference = self.request(self.python, path, headers=headers)
            actual = self.request(self.go, path, headers=headers)
            self.assertEqual(actual[0], reference[0])

    def test_existing_connector_pairing_heartbeat_retry_and_disable(self):
        admin = self.login()
        status, _, payload = self.request(self.python, '/api/egress', method='POST',
                                          body={'name': 'Previously installed test connector'}, headers=admin)
        self.assertEqual(status, 200)
        path = '/api/egress/' + json.loads(payload)['id']
        status, headers, payload = self.request(self.go, path + '/setup', method='POST', body={}, headers=admin)
        self.assertEqual(status, 200)
        self.assertEqual(headers['content-type'], 'application/zip')
        self.assertIn('TreeRoot-Connector.zip', headers['content-disposition'])
        with zipfile.ZipFile(io.BytesIO(payload)) as archive:
            self.assertTrue({'连接.command', '连接.cmd'}.issubset(archive.namelist()))
            for name in ('connector.py', 'connector_version.py', 'egress_proxy.py'):
                self.assertEqual(archive.read(name), (ROOT / name).read_bytes())
            setup = json.loads(archive.read('setup.json'))
        public_key = b'\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20' + b'a' * 32
        pair = {'token': setup['token'], 'public_key': 'ssh-ed25519 ' + base64.b64encode(public_key).decode()}
        status, _, payload = self.request(self.python, path + '/pair', method='POST', body=pair)
        self.assertEqual(status, 200)
        agent = {'Authorization': 'Bearer ' + json.loads(payload)['agent_token']}
        self.assertEqual(self.request(self.go, path + '/pair', method='POST', body=pair)[0], 403)
        record = {'event_key': 'a' * 32, 'ts': int(time.time()), 'domain': 'example.test', 'port': 443,
                  'method': 'CONNECT', 'outcome': 'success', 'upload': 17, 'download': 29}
        legacy = {'records': [record]}
        modern = dict(legacy, driver_version=setup['driver_version'],
                      traffic={'run_id': 'b' * 32, 'sampled_at': 10, 'upload': 17, 'download': 29})
        heartbeat = path + '/heartbeat'
        for address, data in ((self.python, legacy), (self.go, legacy), (self.go, modern), (self.python, modern)):
            status, _, payload = self.request(address, heartbeat, method='POST', body=data, headers=agent)
            self.assertEqual(status, 200)
            response = json.loads(payload)
            self.assertEqual(response['accepted'], [record['event_key']])
            self.assertIn('accounts.google.com', response['blacklist'])
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute('SELECT COUNT(*) FROM egress_events WHERE event_key=?',
                                        (record['event_key'],)).fetchone()[0], 1)
        self.assertEqual(self.request(self.go, heartbeat, method='POST', body=legacy, headers=agent)[0], 200)
        status, _, payload = self.request(self.go, '/api/egress/records?kind=request&search=example', headers=admin)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(payload)['total'], 1)
        self.assertEqual(self.request(self.go, path, method='PATCH', body={'enabled': False}, headers=admin)[0], 200)
        self.assertEqual(self.request(self.go, heartbeat, method='POST', body=modern, headers=agent)[0], 403)

    def test_forwards_security_headers_origin_and_client_ip(self):
        headers = {'Origin': 'https://invalid.example', 'X-Forwarded-For': '192.0.2.23', 'X-Forwarded-Proto': 'https'}
        login = {'username': 'admin', 'password': 'wrong-fixture-password'}
        self.assertEqual(self.request(self.go, '/api/login', method='POST', body=login, headers=headers)[0], 403)
        headers['Origin'] = BASE
        self.assertEqual(self.request(self.go, '/api/login', method='POST', body=login, headers=headers)[0], 401)
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute('SELECT ip FROM login_attempts').fetchone()[0], '192.0.2.23')
        for path, mime in (('/assets/app.js', 'javascript'), ('/assets/style.css', 'text/css')):
            status, headers, _ = self.request(self.go, path)
            self.assertEqual(status, 200)
            self.assertIn(mime, headers['content-type'])

    def test_writer_queue_serializes_parallel_writes_and_keeps_reads_available(self):
        admin = self.login()
        endpoint = '/api/egress/blacklist'
        def insert(index):
            return self.request(self.go, endpoint, method='POST', headers=admin,
                                body={'host': f'queued-{index}.example.test'})[0]
        with ThreadPoolExecutor(max_workers=16) as pool:
            with closing(sqlite3.connect(self.database)) as db:
                db.execute('BEGIN IMMEDIATE')
                futures = [pool.submit(insert, index) for index in range(24)]
                self.assertEqual(self.request(self.go, '/api/me', headers=admin)[0], 200)
                db.rollback()
            self.assertEqual([future.result() for future in futures], [200] * 24)
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute("SELECT COUNT(*) FROM egress_blacklist WHERE host LIKE 'queued-%'").fetchone()[0], 24)

    def test_publish_clash_template(self):
        path = '/api/vps/subscription'
        self.assertEqual(self.request(self.go, path)[0], 401)
        status, headers, login = self.request(self.go, '/api/login', method='POST',
                                             body={'username': 'admin', 'password': 'fixture-password'})
        self.assertEqual(status, 200)
        cookie = {'Cookie': headers['set-cookie'].split(';', 1)[0]}
        auth = dict(cookie, **{'X-CSRF-Token': json.loads(login)['csrf']})
        original = json.loads(self.request(self.go, path, headers=auth)[2])
        self.assertNotIn('fixture-uuid', original['source'])
        source = original['source'].replace('DOMAIN,localhost,DIRECT', 'DOMAIN,fixture.test,REJECT')
        edit = dict(original, source=source)
        public = '/sub/fixture-token/clash.yaml'
        before = json.loads(self.request(self.go, public)[2])
        self.assertEqual(self.request(self.go, path, method='PUT', body=edit, headers=cookie)[0], 403)
        for bad in [source.replace('MATCH,美国自建', 'MATCH,missing'), source + '\n---\n{}',
                    source + '\nproxies: []', source.replace('127.0.0.0/8', 'invalid')]:
            self.assertEqual(self.request(self.go, path, method='PUT', body=dict(edit, source=bad), headers=auth)[0], 422)
        self.assertEqual(self.request(self.go, path + '/preview', method='POST', body=edit, headers=auth)[0], 200)
        self.assertEqual(json.loads(self.request(self.go, public)[2]), before)
        status, _, published = self.request(self.go, path, method='PUT', body=edit, headers=auth)
        self.assertEqual(status, 200)
        try:
            self.assertEqual(self.request(self.go, path, method='PUT', body=original, headers=auth)[0], 409)
            after = json.loads(self.request(self.go, public)[2])
            self.assertEqual(after['proxies'], before['proxies'])
            self.assertIn('DOMAIN,fixture.test,REJECT', after['rules'])
            self.assertEqual(after, json.loads(self.request(self.go, '/fixture-token/clash.yaml')[2]))
            self.assertEqual(json.loads(self.request(self.go, path, headers=auth)[2])['source'], source)
        finally:
            body = dict(original, revision=json.loads(published)['revision'])
            self.assertEqual(self.request(self.go, path, method='PUT', body=body, headers=auth)[0], 200)

    def test_native_login_and_subscriptions(self):
        status, headers, payload = self.request(self.go, '/api/login', method='POST',
                                                body={'username': 'admin', 'password': 'fixture-password'})
        self.assertEqual(status, 200)
        self.assertIn('Secure', headers['set-cookie'])
        auth = {'Cookie': headers['set-cookie'].split(';', 1)[0], 'X-CSRF-Token': json.loads(payload)['csrf']}
        for route in ('/api/users', '/api/server', '/api/resources', '/api/overview', '/api/egress/overview',
                      '/api/egress/rates', '/api/users/1/limits'):
            with self.subTest(route=route):
                self.assertEqual(self.request(self.go, route, headers=auth)[0], 200)
                self.assertEqual(self.request(self.go, route)[0], 401)
        for kind in ('clash.yaml', 'shadowrocket'):
            responses = [self.request(self.go, path) for path in (f'/sub/fixture-token/{kind}', f'/fixture-token/{kind}')]
            self.assertEqual([response[0] for response in responses], [200, 200])
            self.assertEqual(responses[0][2], responses[1][2])
            if kind == 'clash.yaml':
                config = json.loads(responses[0][2])
                self.assertEqual(config['mode'], 'rule')
                rules = config['rules']
                for domain in ('douyinpic.com', 'xiaomi.net', 'xiaomixiaoai.com', 'kuaishou.com', 'toutiao.com'):
                    self.assertIn(f'DOMAIN-SUFFIX,{domain},DIRECT', rules[:-2])
                self.assertEqual(rules[-2:], ['GEOIP,CN,DIRECT', 'MATCH,美国自建'])
        self.assertEqual(self.request(self.go, '/api/logout', method='POST', body={}, headers=auth)[0], 200)
        self.assertEqual(json.loads(self.request(self.go, '/api/me', headers=auth)[2]), {'authenticated': False})
