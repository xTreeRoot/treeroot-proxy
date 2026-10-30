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
from typing import Literal
import uuid
import zipfile

from fastapi import Depends, HTTPException, Query, Request, Response
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


class ConnectionRecord(BaseModel):
    event_key: str = Field(pattern=r'^[a-f0-9]{32}$')
    ts: int = Field(ge=0)
    domain: str = Field(default='', max_length=253, pattern=r'^[a-zA-Z0-9.:%_-]*$')
    port: int | None = Field(default=None, ge=1, le=65535)
    method: str = Field(default='', max_length=16, pattern=r'^[A-Z]*$')
    outcome: Literal['success', 'failed']
    upload: int = Field(default=0, ge=0, le=2**53 - 1)
    download: int = Field(default=0, ge=0, le=2**53 - 1)
    duration_ms: int = Field(default=0, ge=0, le=2**53 - 1)
    detail: Literal['', 'invalid_request', 'target_rejected', 'connection_failed', 'timeout', 'interrupted'] = ''


class HeartbeatEgress(BaseModel):
    records: list[ConnectionRecord] = Field(default_factory=list, max_length=100)


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
            db.execute('''CREATE TABLE IF NOT EXISTS egress_events(
              id INTEGER PRIMARY KEY AUTOINCREMENT,event_key TEXT NOT NULL,
              device_id TEXT NOT NULL REFERENCES egress_devices(id),ts INTEGER NOT NULL,
              kind TEXT NOT NULL,action TEXT NOT NULL,outcome TEXT NOT NULL,
              domain TEXT NOT NULL DEFAULT '',port INTEGER,method TEXT NOT NULL DEFAULT '',
              upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,
              duration_ms INTEGER NOT NULL DEFAULT 0,exit_ip TEXT NOT NULL DEFAULT '',
              detail TEXT NOT NULL DEFAULT '',UNIQUE(device_id,event_key))''')
            db.execute('CREATE INDEX IF NOT EXISTS egress_events_device_id ON egress_events(device_id,id DESC)')
            db.execute('CREATE INDEX IF NOT EXISTS egress_events_time ON egress_events(ts)')
        self.publish()

    def event(self, db, row, action, outcome='success', detail='', ts=None, event_key=None):
        db.execute('''INSERT OR IGNORE INTO egress_events
            (event_key,device_id,ts,kind,action,outcome,exit_ip,detail) VALUES(?,?,?,'device',?,?,?,?)''',
            (event_key or uuid.uuid4().hex, row['id'], ts if ts is not None else int(time.time()),
             action, outcome, row['last_ip'] or '', detail))

    def disconnected(self, db, row, now):
        if row['last_seen'] and now - row['last_seen'] >= 65:
            self.event(db, row, 'disconnected', ts=row['last_seen'] + 65,
                       event_key='offline:' + str(row['last_seen']))

    def maintain(self):
        now = int(time.time())
        with self.database() as db:
            for row in db.execute('''SELECT * FROM egress_devices WHERE enabled=1 AND deleted=0
                                     AND public_key IS NOT NULL AND last_seen<=?''', (now - 65,)):
                self.disconnected(db, row, now)
            db.execute('DELETE FROM egress_events WHERE ts<?', (now - 7 * 86400,))

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


def install_routes(app, database, authenticated, config, data, root, report_database):
    manager = Egress(database, config, data, root)

    @app.get('/api/egress')
    def devices(deleted: bool = False, session=Depends(authenticated)):
        with database(readonly=True) as db:
            return [manager.summary(row) for row in db.execute('SELECT * FROM egress_devices WHERE deleted=? ORDER BY created,id', (deleted,))]

    @app.get('/api/egress/records')
    def records(device_id: str = '', kind: Literal['', 'request', 'device'] = '',
                outcome: Literal['', 'success', 'failed'] = '', search: str = Query(default='', max_length=253),
                start: int | None = Query(default=None, ge=0), end: int | None = Query(default=None, ge=0),
                page: int = Query(default=1, ge=1), page_size: int = Query(default=25, ge=1, le=100),
                before_id: int | None = Query(default=None, ge=1), session=Depends(authenticated)):
        if start is not None and end is not None and start > end:
            raise HTTPException(422, '开始时间不能晚于结束时间')
        clauses, args = ['e.ts>=?'], [max(start or 0, int(time.time()) - 7 * 86400)]
        for column, value in [('device_id', device_id), ('kind', kind), ('outcome', outcome)]:
            if value:
                clauses.append('e.' + column + '=?')
                args.append(value)
        if end is not None:
            clauses.append('e.ts<=?')
            args.append(end)
        if search.strip():
            clauses.append("(e.domain LIKE ? ESCAPE '\\' OR e.exit_ip LIKE ? ESCAPE '\\')")
            pattern = '%' + search.strip().replace('\\', '\\\\').replace('%', '\\%').replace('_', '\\_') + '%'
            args.extend([pattern, pattern])
        with report_database() as db:
            if before_id is None:
                before_id = db.execute('SELECT MAX(e.id) FROM egress_events e WHERE ' + ' AND '.join(clauses), args).fetchone()[0]
            clauses.append('e.id<=?')
            args.append(before_id or 0)
            where = ' WHERE ' + ' AND '.join(clauses)
            total = db.execute('SELECT COUNT(*) FROM egress_events e' + where, args).fetchone()[0]
            pages = max(1, (total + page_size - 1) // page_size)
            page = min(page, pages)
            items = [dict(row) for row in db.execute('''SELECT e.*,d.name FROM
                (SELECT * FROM egress_events e''' + where + ''' ORDER BY e.id DESC LIMIT ? OFFSET ?) e
                JOIN egress_devices d ON d.id=e.device_id ORDER BY e.id DESC''',
                [*args, page_size, (page - 1) * page_size])]
        for item in items:
            item.pop('event_key')
        return {'items': items, 'total': total, 'page': page, 'page_size': page_size,
                'pages': pages, 'before_id': before_id}

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
            row = manager.row(device_id, db)
            manager.event(db, row, 'created')
            result = manager.summary(row)
        manager.publish()
        return result

    @app.patch('/api/egress/{device_id}')
    def update(device_id: str, body: UpdateEgress, session=Depends(authenticated)):
        with database() as db:
            row = manager.row(device_id, db)
            if bool(row['enabled']) != body.enabled:
                manager.event(db, row, 'enabled' if body.enabled else 'disabled')
            db.execute('UPDATE egress_devices SET enabled=? WHERE id=?', (body.enabled, device_id))
            result = manager.summary(manager.row(device_id, db))
        manager.publish()
        return result

    @app.delete('/api/egress/{device_id}')
    def remove(device_id: str, session=Depends(authenticated)):
        with database() as db:
            row = manager.row(device_id, db)
            manager.event(db, row, 'deleted')
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
            manager.event(db, manager.row(device_id, db), 'restored')
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
            manager.event(db, row, 'setup_downloaded')
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
            manager.event(db, row, 'paired')
            result = {'id': device_id, 'base_url': config['base_url'], 'ssh_host': config['egress_ssh_host'],
                      'ssh_port': 22, 'ssh_user': 'treeroot-egress', 'remote_port': row['port'],
                      'known_hosts': config['egress_known_hosts'], 'proxy_username': device_id,
                      'proxy_password': row['proxy_password'], 'agent_token': agent_token}
        manager.publish()
        return result

    @app.post('/api/egress/{device_id}/heartbeat')
    def heartbeat(device_id: str, body: HeartbeatEgress, request: Request):
        now = int(time.time())
        with database() as db:
            db.execute('BEGIN IMMEDIATE')
            row = manager.authenticate_agent(device_id, request, db)
            if not row['last_seen'] or now - row['last_seen'] >= 65:
                manager.disconnected(db, row, now)
                manager.event(db, row, 'connected')
            for record in body.records:
                if now - 7 * 86400 <= record.ts <= now + 300:
                    db.execute('''INSERT OR IGNORE INTO egress_events
                        (event_key,device_id,ts,kind,action,outcome,domain,port,method,upload,download,duration_ms,exit_ip,detail)
                        VALUES(?,?,?,'request','connection',?,?,?,?,?,?,?,?,?)''',
                        (record.event_key, device_id, record.ts, record.outcome, record.domain, record.port,
                         record.method, record.upload, record.download, record.duration_ms, row['last_ip'] or '', record.detail))
            db.execute('UPDATE egress_devices SET last_seen=? WHERE id=?', (now, device_id))
        manager.publish()
        return {'ok': True, 'accepted': [record.event_key for record in body.records]}

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
            with database() as db:
                manager.event(db, row, 'ip_test', 'failed', 'connection_failed')
            raise HTTPException(503, '出口检测失败，请检查电脑网络与连接器状态')
        with database() as db:
            db.execute('UPDATE egress_devices SET last_ip=?,last_test=? WHERE id=?', (address, int(time.time()), device_id))
            manager.event(db, manager.row(device_id, db), 'ip_test')
        return {'ip': address, 'tested_at': int(time.time())}

    return manager
