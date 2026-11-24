"""Disposable Go UI fixture on localhost:19994, admin / preview-only.

Only temporary data under work/ is used. Build work/treeroot-gopanel first.
"""
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))


def main():
    (ROOT / 'work').mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='react-ui-', dir=ROOT / 'work') as directory:
        data = Path(directory)
        fake = data / 'xray-fixture'
        fake.write_text('#!' + sys.executable + '\nimport sys\nprint("Added 1 user(s)" if "adu" in sys.argv else "Removed 1 user(s)" if "rmu" in sys.argv else "{}")\n')
        fake.chmod(0o700)
        xray = data / 'xray.json'
        xray.write_text(json.dumps({'inbounds': [{'tag': 'websocket-in', 'settings': {'clients': []}}], 'outbounds': [], 'routing': {'rules': []}, 'log': {}}))
        config = data / 'config.json'
        config.write_text(json.dumps({'base_url': 'http://localhost:19994', 'admin_username': 'admin',
            'admin_password_hash': '', 'data_dir': str(data), 'initial_uuid': 'preview-only',
            'initial_token': 'preview-only', 'ws_path': '/preview-only', 'xray_config': str(xray),
            'xray_binary': str(fake), 'access_log': str(data / 'access.log'),
            'egress_ssh_host': '192.0.2.1', 'egress_known_hosts': 'fixture-host-key'}))
        os.environ['TREEROOT_CONFIG'] = str(config)
        import app
        app.CONFIG['admin_password_hash'] = app.password_hash('preview-only')
        app.initialize()
        now = int(time.time())
        domains = ['github.com', 'api.openai.com', 'youtube.com', 'google.com', 'cloudflare.com', 'apple.com', 'npmjs.org', 'vercel.com']
        with app.database() as db:
            db.execute("UPDATE users SET name='我的 Mac',upload=?,download=? WHERE id=1", (8 * 1024**3, 76 * 1024**3))
            for i in range(2, 15):
                db.execute('INSERT INTO users(name,client_uuid,email,token,created,upload,download,enabled) VALUES(?,?,?,?,?,?,?,?)',
                    (['工作设备', '手机', '备用设备', '测试用户'][i % 4] + (f' {i}' if i > 5 else ''), f'fixture-uuid-{i}', f'fixture-{i}@example.test', f'fixture-token-{i}', now-i*86400, i*1024**3, i*3*1024**3, i % 5 != 0))
            for i in range(14 * 24):
                amount = int((math.sin(i * .7) + 1.5) * 32 * 1024**2)
                db.execute('INSERT INTO traffic VALUES(?,?,?,?)', (now // 3600 * 3600-i*3600, 1, amount//5, amount))
            for i, domain in enumerate(domains):
                db.execute('INSERT INTO domains VALUES(?,?,?,?)', (1, domain, (8-i)*293, now-i*30))
            for i in range(137):
                db.execute('INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,?,?,?,?)', (now-i*30, i % 4+1, domains[i % 8], 443, 'tcp'))
            for i, name in enumerate(['MacBook Pro', 'Windows 工作站', '备用电脑']):
                db.execute('''INSERT INTO egress_devices(id,name,port,created,browser,access_code,proxy_password,enabled,last_seen,public_key,last_ip,driver_version)
                    VALUES(?,?,?,?,?,?,?,?,?,?,?,?)''', (f'fixture-device-{i}', name, 21990+i, now-i*86400,
                    json.dumps({'platformName': 'macOS' if i == 0 else 'Windows', 'platformVersion': '15.0' if i == 0 else '11'}),
                    f'fixture-code-{i}', f'fixture-password-{i}', i != 2, now if i == 0 else now-3600, 'fixture-public-key', f'203.0.113.{i+10}', 'v2026.11.24.1'))
            for i in range(177):
                db.execute('''INSERT INTO egress_events(event_key,device_id,ts,kind,action,outcome,domain,port,method,upload,download,duration_ms,exit_ip,detail)
                    VALUES(?,?,?,'request','connection',?,?,443,'CONNECT',?,?,?,'203.0.113.10',?)''',
                    (f'fixture-event-{i}', f'fixture-device-{i % 2}', now-i*600, 'failed' if i % 13 == 0 else 'success', domains[i % 8], (i+1)*98765, (i+1)*456789, 800+i*29, 'timeout' if i % 13 == 0 else ''))
        process = subprocess.Popen([str(ROOT / 'work/treeroot-gopanel'), '-listen', '127.0.0.1:19994', '-collect=false', '-limits=', '-root', str(ROOT)], cwd=ROOT)
        def stop(*_):
            if process.poll() is None:
                process.terminate()
        signal.signal(signal.SIGTERM, stop)
        try:
            process.wait()
        except KeyboardInterrupt:
            stop()
            process.wait(timeout=5)


if __name__ == '__main__':
    main()
