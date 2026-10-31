#!/usr/bin/env python3
"""电脑端出口连接器：本机保存私钥，通过受限 SSH 反向隧道提供平台网络出口。"""
import argparse
import asyncio
from contextlib import closing
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import ssl
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request

from egress_proxy import ForwardProxy

HOME = Path.home() / '.treeroot-connector'
HTTPS_CONTEXT = None


class RecordQueue:
    """磁盘队列在断网或重启后继续上报；服务器按 event_key 去重。"""
    def __init__(self, path):
        self.path = path
        with closing(sqlite3.connect(path)) as db, db:
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE IF NOT EXISTS records(id INTEGER PRIMARY KEY,event_key TEXT UNIQUE,ts INTEGER,payload TEXT)')
        path.chmod(0o600)

    def append(self, record):
        try:
            with closing(sqlite3.connect(self.path)) as db, db:
                db.execute('INSERT OR IGNORE INTO records(event_key,ts,payload) VALUES(?,?,?)',
                           (record['event_key'], record['ts'], json.dumps(record)))
                db.execute('DELETE FROM records WHERE ts<? OR id<=(SELECT MAX(id)-10000 FROM records)',
                           (int(time.time()) - 7 * 86400,))
        except sqlite3.Error:
            print('访问记录暂时无法写入本机队列。', flush=True)

    def batch(self):
        with closing(sqlite3.connect(self.path)) as db, db:
            return [json.loads(row[0]) for row in db.execute('SELECT payload FROM records ORDER BY id LIMIT 100')]

    def acknowledge(self, keys):
        with closing(sqlite3.connect(self.path)) as db, db:
            db.executemany('DELETE FROM records WHERE event_key=?', [(key,) for key in keys])


def save_private(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    path.write_text(value)
    path.chmod(0o600)


def tls_context():
    """Python 未配置 CA 时读取 macOS 内置根证书，始终保持证书和主机名校验。"""
    global HTTPS_CONTEXT
    if HTTPS_CONTEXT is None:
        context = ssl.create_default_context()
        if sys.platform == 'darwin' and not context.cert_store_stats()['x509_ca']:
            certificates = subprocess.run(['/usr/bin/security', 'find-certificate', '-a', '-p',
                '/System/Library/Keychains/SystemRootCertificates.keychain'],
                check=True, capture_output=True, text=True, timeout=15).stdout
            context.load_verify_locations(cadata=certificates)
        HTTPS_CONTEXT = context
    return HTTPS_CONTEXT


def api(config, path, data, token=None):
    url = config['base_url'].rstrip('/') + '/api/egress/' + config['id'] + path
    if not url.startswith('https://'):
        raise ValueError('连接器只接受 HTTPS 管理地址')
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request(url, data=json.dumps(data).encode(), headers=headers, method='POST')
    with urllib.request.urlopen(request, timeout=15, context=tls_context()) as response:
        return json.load(response)


def folder_for(device_id):
    if not re.fullmatch('[a-f0-9]{32}', device_id):
        raise ValueError('设备编号无效')
    return HOME / device_id


def install(setup_path):
    setup = json.loads(Path(setup_path).read_text())
    folder = folder_for(setup['id'])
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    key = folder / 'id_ed25519'
    if not key.exists():
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-C', 'treeroot-egress', '-f', str(key)], check=True)
    fields = key.with_suffix('.pub').read_text().split()
    paired = api(setup, '/pair', {'token': setup['token'], 'public_key': ' '.join(fields[:2])})
    save_private(folder / 'config.json', json.dumps(paired))
    save_private(folder / 'known_hosts', paired['known_hosts'] + '\n')
    for name in ('connector.py', 'egress_proxy.py'):
        target = folder / name
        if Path(__file__).resolve().parent / name != target:
            shutil.copyfile(Path(__file__).resolve().parent / name, target)
    Path(setup_path).unlink(missing_ok=True)
    command = [sys.executable, str(folder / 'connector.py'), 'run', '--id', setup['id']]
    if sys.platform == 'darwin':
        target = Path.home() / 'Library/LaunchAgents' / ('top.treeroot.egress.' + setup['id'] + '.plist')
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(plistlib.dumps({
            'Label': 'top.treeroot.egress.' + setup['id'], 'ProgramArguments': command,
            'RunAtLoad': True, 'KeepAlive': True, 'ThrottleInterval': 15,
            'StandardOutPath': str(folder / 'connector.log'), 'StandardErrorPath': str(folder / 'connector.log'),
        }))
        target.chmod(0o600)
        domain = 'gui/' + str(os.getuid())
        subprocess.run(['launchctl', 'bootout', domain, str(target)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['launchctl', 'bootstrap', domain, str(target)], check=True)
    elif sys.platform == 'win32':
        subprocess.run(['schtasks', '/Create', '/SC', 'ONLOGON', '/TN', 'TreeRoot-egress-' + setup['id'],
                        '/TR', subprocess.list2cmdline(command), '/F'], check=True, capture_output=True)
        subprocess.run(['schtasks', '/Run', '/TN', 'TreeRoot-egress-' + setup['id']], check=True, capture_output=True)
    else:
        print('请以当前用户的后台服务持续运行：', ' '.join(command))
        return
    print('连接器已安装，当前用户登录系统后自动连接。设备编号：' + setup['id'])


async def run(device_id):
    folder = folder_for(device_id)
    config = json.loads((folder / 'config.json').read_text())
    queue = RecordQueue(folder / 'records.sqlite3')
    proxy = ForwardProxy(config['proxy_username'], config['proxy_password'], on_record=queue.append)
    server = await proxy.listen()
    port = server.sockets[0].getsockname()[1]
    arguments = [
        'ssh', '-N', '-T', '-i', str(folder / 'id_ed25519'), '-p', str(config['ssh_port']),
        '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes',
        '-o', 'UserKnownHostsFile=' + str(folder / 'known_hosts'), '-o', 'ExitOnForwardFailure=yes',
        '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3', '-o', 'ConnectTimeout=15',
        '-R', f'127.0.0.1:{config["remote_port"]}:127.0.0.1:{port}',
        config['ssh_user'] + '@' + config['ssh_host'],
    ]
    async with server:
        while True:
            process = await asyncio.create_subprocess_exec(*arguments, stdout=asyncio.subprocess.DEVNULL,
                                                            stderr=asyncio.subprocess.DEVNULL)
            try:
                while process.returncode is None:
                    records = []
                    try:
                        records = await asyncio.to_thread(queue.batch)
                        payload = {'records': records, 'traffic': proxy.traffic_snapshot()}
                        result = await asyncio.to_thread(api, config, '/heartbeat', payload, config['agent_token'])
                        await asyncio.to_thread(queue.acknowledge, result.get('accepted', []))
                    except urllib.error.HTTPError as error:
                        if error.code in (403, 404):
                            print('出口已停用，等待管理员重新启用。', flush=True)
                            break
                    except (OSError, ValueError, sqlite3.Error):
                        pass
                    try:
                        await asyncio.wait_for(process.wait(), 5)
                    except asyncio.TimeoutError:
                        continue
            finally:
                if process.returncode is None:
                    process.terminate()
                    await process.wait()
            await asyncio.sleep(15)


def uninstall(device_id):
    folder_for(device_id)
    if sys.platform == 'darwin':
        target = Path.home() / 'Library/LaunchAgents' / ('top.treeroot.egress.' + device_id + '.plist')
        subprocess.run(['launchctl', 'bootout', 'gui/' + str(os.getuid()), str(target)],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        target.unlink(missing_ok=True)
    elif sys.platform == 'win32':
        subprocess.run(['schtasks', '/End', '/TN', 'TreeRoot-egress-' + device_id], capture_output=True)
        subprocess.run(['schtasks', '/Delete', '/TN', 'TreeRoot-egress-' + device_id, '/F'], capture_output=True)
    print('已取消自动启动。私钥与配置保留在 ' + str(folder_for(device_id)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('install').add_argument('--setup', required=True)
    commands.add_parser('run').add_argument('--id', required=True)
    commands.add_parser('uninstall').add_argument('--id', required=True)
    args = parser.parse_args()
    if args.command == 'install':
        install(args.setup)
    elif args.command == 'run':
        try:
            asyncio.run(run(args.id))
        except KeyboardInterrupt:
            pass
    else:
        uninstall(args.id)
