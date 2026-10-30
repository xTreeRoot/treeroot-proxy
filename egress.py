"""管理电脑出口配对、受限 SSH 身份及供本机采集服务读取的出口登记。"""
import base64
import hashlib
import hmac
import io
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import time
import uuid
import zipfile

from fastapi import Depends, HTTPException, Request, Response
from pydantic import BaseModel, Field


class BrowserInfo(BaseModel):
    """用户网页浏览器公开的设备信息，不接收 Cookie、可执行路径或代理地址。"""
    userAgent: str = Field(default='', max_length=512)
    platform: str = Field(default='', max_length=64)
    language: str = Field(default='zh-CN', max_length=35)
    timezone: str = Field(default='Asia/Shanghai', max_length=80)
    platformName: str = Field(default='', max_length=32)
    platformVersion: str = Field(default='', max_length=32)
    architecture: str = Field(default='', max_length=16)
    bitness: str = Field(default='', max_length=8)
    width: int = Field(default=1280, ge=320, le=7680)
    height: int = Field(default=900, ge=240, le=4320)


class NewEgress(BaseModel):
    name: str = Field(min_length=1, max_length=64)
    browser: BrowserInfo = Field(default_factory=BrowserInfo)


class PairEgress(BaseModel):
    token: str = Field(min_length=20, max_length=128)
    public_key: str = Field(min_length=40, max_length=256)


class UpdateEgress(BaseModel):
    enabled: bool


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


class Egress:
    def __init__(self, database, config, data, root):
        self.database, self.config, self.root = database, config, root
        self.directory = Path(config.get('egress_dir', str(data / 'egress')))

    def initialize(self, data=None):
        """仅新增独立表，既有代理身份、会话及统计不做迁移或重置。"""
        if data is not None and not self.config.get('egress_dir'):
            self.directory = Path(data) / 'egress'
        with self.database() as db:
            db.execute('''CREATE TABLE IF NOT EXISTS egress_devices(
              id TEXT PRIMARY KEY,name TEXT NOT NULL,port INTEGER NOT NULL UNIQUE,
              enabled INTEGER NOT NULL DEFAULT 1,deleted INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,
              browser TEXT NOT NULL,access_code TEXT NOT NULL,proxy_password TEXT NOT NULL,
              pair_hash TEXT,pair_expires INTEGER,public_key TEXT,agent_hash TEXT,
              last_seen INTEGER,last_ip TEXT,last_test INTEGER)''')
            if 'deleted' not in {row[1] for row in db.execute('PRAGMA table_info(egress_devices)')}:
                db.execute('ALTER TABLE egress_devices ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0')
        self.publish()

    def publish(self):
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o750)
        with self.database(readonly=True) as db:
            rows = db.execute('SELECT * FROM egress_devices WHERE deleted=0').fetchall()
        devices = [{
            'id': r['id'], 'name': r['name'], 'enabled': bool(r['enabled']),
            'accessHash': digest(r['access_code']), 'lastSeen': r['last_seen'],
            'browser': json.loads(r['browser']),
            'proxy': {'server': f"http://127.0.0.1:{r['port']}",
                      'username': r['id'], 'password': r['proxy_password']},
            'publicKey': r['public_key'], 'port': r['port'],
        } for r in rows]
        target = self.directory / 'registry.json'
        temporary = self.directory / ('registry-' + secrets.token_hex(8) + '.tmp')
        try:
            with open(temporary, 'x') as out:
                os.chmod(temporary, 0o640)
                json.dump({'version': 1, 'devices': devices}, out, ensure_ascii=False)
            os.replace(temporary, target)
        finally:
            temporary.unlink(missing_ok=True)

    def row(self, device_id, db):
        row = db.execute('SELECT * FROM egress_devices WHERE id=?', (device_id,)).fetchone()
        if not row or row['deleted']:
            raise HTTPException(404, '出口设备不存在')
        return row

    def summary(self, row):
        online = bool(row['enabled'] and row['public_key'] and row['last_seen'] and time.time() - row['last_seen'] < 65)
        return {key: row[key] for key in ('id', 'name', 'created', 'last_seen', 'last_ip', 'last_test')} | {
            'enabled': bool(row['enabled']), 'paired': bool(row['public_key']), 'online': online,
            'browser': json.loads(row['browser']),
        }

    def authenticate_agent(self, device_id, request, db):
        row = self.row(device_id, db)
        token = request.headers.get('authorization', '').removeprefix('Bearer ')
        if not row['enabled'] or not row['agent_hash'] or not hmac.compare_digest(row['agent_hash'], digest(token)):
            raise HTTPException(403, '出口连接已停用或凭据无效')
        return row


def install_routes(app, database, authenticated, config, data, root):
    manager = Egress(database, config, data, root)

    @app.get('/api/egress')
    def devices(deleted: bool = False, session=Depends(authenticated)):
        with database(readonly=True) as db:
            return [manager.summary(row) for row in db.execute('SELECT * FROM egress_devices WHERE deleted=? ORDER BY created,id', (deleted,))]

    @app.post('/api/egress')
    def create(body: NewEgress, session=Depends(authenticated)):
        name = body.name.strip()
        if not name:
            raise HTTPException(422, '请输入电脑名称')
        with database() as db:
            db.execute('BEGIN IMMEDIATE')
            ports = {row[0] for row in db.execute('SELECT port FROM egress_devices')}
            port = None
            for candidate in range(21000, 22000):
                if candidate in ports:
                    continue
                with socket.socket() as probe:
                    try:
                        probe.bind(('127.0.0.1', candidate))
                    except OSError:
                        continue
                port = candidate
                break
            if port is None:
                raise HTTPException(409, '出口连接数量已达上限')
            device_id = uuid.uuid4().hex
            db.execute('''INSERT INTO egress_devices(id,name,port,created,browser,access_code,proxy_password)
                          VALUES(?,?,?,?,?,?,?)''', (device_id, name, port, int(time.time()),
                          body.browser.model_dump_json(), secrets.token_urlsafe(32), secrets.token_urlsafe(32)))
            result = manager.summary(manager.row(device_id, db))
        manager.publish()
        return result

    @app.patch('/api/egress/{device_id}')
    def update(device_id: str, body: UpdateEgress, session=Depends(authenticated)):
        with database() as db:
            manager.row(device_id, db)
            db.execute('UPDATE egress_devices SET enabled=? WHERE id=?', (body.enabled, device_id))
            result = manager.summary(manager.row(device_id, db))
        manager.publish()
        return result

    @app.delete('/api/egress/{device_id}')
    def remove(device_id: str, session=Depends(authenticated)):
        with database() as db:
            manager.row(device_id, db)
            db.execute("""UPDATE egress_devices SET deleted=1,enabled=0,public_key=NULL,
                agent_hash=NULL,pair_hash=NULL,pair_expires=NULL,last_seen=NULL WHERE id=?""", (device_id,))
        manager.publish()
        return {'ok': True}

    @app.post('/api/egress/{device_id}/restore')
    def restore(device_id: str, session=Depends(authenticated)):
        with database() as db:
            result = db.execute('UPDATE egress_devices SET deleted=0,enabled=0 WHERE id=? AND deleted=1', (device_id,))
            if not result.rowcount:
                raise HTTPException(404, '已删除设备不存在')
        manager.publish()
        return {'ok': True}

    @app.get('/api/egress/{device_id}/access-code')
    def access_code(device_id: str, session=Depends(authenticated)):
        with database(readonly=True) as db:
            return {'code': manager.row(device_id, db)['access_code']}

    @app.post('/api/egress/{device_id}/setup')
    def setup(device_id: str, session=Depends(authenticated)):
        if not config.get('egress_ssh_host') or not config.get('egress_known_hosts'):
            raise HTTPException(503, '服务器出口连接尚未完成部署')
        token = secrets.token_urlsafe(32)
        with database() as db:
            row = manager.row(device_id, db)
            if not row['enabled']:
                raise HTTPException(409, '请先启用出口设备')
            db.execute('UPDATE egress_devices SET pair_hash=?,pair_expires=? WHERE id=?',
                       (digest(token), int(time.time()) + 600, device_id))
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
            for name in ('connector.py', 'egress_proxy.py'):
                bundle.write(root / name, name)
            bundle.writestr('连接.command', '#!/bin/bash\ncd "$(dirname "$0")"\npython3 connector.py install --setup setup.json\nresult=$?\nif [ "$result" -ne 0 ]; then echo "连接失败，请查看上面的提示后重新下载连接包。"; fi\nread -r -p "按回车关闭窗口"\nexit "$result"\n')
            bundle.writestr('连接.cmd', '@echo off\r\ncd /d "%~dp0"\r\npy -3 connector.py install --setup setup.json\r\npause\r\n')
            bundle.writestr('setup.json', json.dumps({'base_url': config['base_url'], 'id': device_id, 'token': token}))
            bundle.writestr('使用说明.txt', '请在本机解压，10 分钟内运行：\nmacOS：双击 连接.command\nWindows：双击 连接.cmd\n需要 Python 3.10+ 和 OpenSSH 客户端。安装后当前用户登录系统时自动连接。\n卸载：python3 connector.py uninstall --id ' + device_id + '\n')
            for name in ('连接.command',):
                bundle.getinfo(name).external_attr = 0o100755 << 16
        return Response(archive.getvalue(), media_type='application/zip',
                        headers={'Content-Disposition': 'attachment; filename=TreeRoot-Connector.zip'})

    @app.post('/api/egress/{device_id}/pair')
    def pair(device_id: str, body: PairEgress):
        fields = body.public_key.strip().split()
        if len(fields) != 2 or fields[0] != 'ssh-ed25519' or not re.fullmatch(r'[A-Za-z0-9+/]+={0,2}', fields[1]):
            raise HTTPException(422, '只接受新生成的 Ed25519 公钥')
        try:
            blob = base64.b64decode(fields[1], validate=True)
            if len(blob) != 51 or blob[:19] != b'\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20':
                raise ValueError()
        except ValueError:
            raise HTTPException(422, '公钥内容无效')
        agent_token = secrets.token_urlsafe(32)
        with database() as db:
            db.execute('BEGIN IMMEDIATE')
            row = manager.row(device_id, db)
            if not row['enabled'] or not row['pair_hash'] or row['pair_expires'] < time.time() or not hmac.compare_digest(row['pair_hash'], digest(body.token)):
                raise HTTPException(403, '配对包已过期或已使用，请重新下载')
            db.execute('''UPDATE egress_devices SET public_key=?,agent_hash=?,pair_hash=NULL,
                          pair_expires=NULL,last_seen=NULL WHERE id=?''', (' '.join(fields), digest(agent_token), device_id))
            result = {'id': device_id, 'base_url': config['base_url'], 'ssh_host': config['egress_ssh_host'],
                      'ssh_port': 22, 'ssh_user': 'treeroot-egress', 'remote_port': row['port'],
                      'known_hosts': config['egress_known_hosts'], 'proxy_username': device_id,
                      'proxy_password': row['proxy_password'], 'agent_token': agent_token}
        manager.publish()
        return result

    @app.post('/api/egress/{device_id}/heartbeat')
    def heartbeat(device_id: str, request: Request):
        with database() as db:
            manager.authenticate_agent(device_id, request, db)
            db.execute('UPDATE egress_devices SET last_seen=? WHERE id=?', (int(time.time()), device_id))
        manager.publish()
        return {'ok': True}

    @app.post('/api/egress/{device_id}/test')
    def test(device_id: str, session=Depends(authenticated)):
        with database(readonly=True) as db:
            row = manager.row(device_id, db)
            if not manager.summary(row)['online']:
                raise HTTPException(409, '电脑尚未连接，请先安装并启动连接器')
        configuration = (f'proxy = "http://127.0.0.1:{row["port"]}"\n'
                         f'proxy-user = "{device_id}:{row["proxy_password"]}"\n'
                         'url = "https://myip.ipip.net"\nsilent\nshow-error\nfail\nmax-time = 15\nnoproxy = ""\n')
        try:
            result = subprocess.run(['/usr/bin/curl', '--config', '-'], input=configuration,
                                    capture_output=True, text=True, timeout=18, check=True)
            match = re.search(r'(?:[0-9]{1,3}\.){3}[0-9]{1,3}', result.stdout)
            if not match:
                raise ValueError('IP check response invalid')
            address = str(ipaddress.ip_address(match.group()))
        except (OSError, ValueError, subprocess.SubprocessError):
            raise HTTPException(503, '出口检测失败，请检查电脑网络与连接器状态')
        with database() as db:
            db.execute('UPDATE egress_devices SET last_ip=?,last_test=? WHERE id=?', (address, int(time.time()), device_id))
        return {'ip': address, 'tested_at': int(time.time())}

    return manager
