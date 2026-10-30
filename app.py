import base64
from contextlib import asynccontextmanager, contextmanager
from datetime import datetime, timezone
import hashlib
import hmac
import json
import logging
import os
from pathlib import Path
import re
import secrets
import shutil
import sqlite3
import subprocess
import tempfile
import threading
import time
import uuid

from fastapi import Depends, FastAPI, HTTPException, Query, Request, Response
from fastapi.responses import FileResponse, JSONResponse, PlainTextResponse
from pydantic import BaseModel, Field

ROOT = Path(__file__).resolve().parent
CONFIG = json.loads(Path(os.environ.get('TREEROOT_CONFIG', '/etc/treeroot/config.json')).read_text())
DATA = Path(CONFIG.get('data_dir', '/var/lib/treeroot'))
DB = DATA / 'panel.sqlite3'
XRAY_CONFIG = Path(CONFIG.get('xray_config', '/usr/local/etc/xray/config.json'))
ACCESS_LOG = Path(CONFIG.get('access_log', '/var/log/treeroot/access.log'))
XRAY = CONFIG.get('xray_binary', '/usr/local/bin/xray')
API = CONFIG.get('xray_api', '127.0.0.1:10085')
BASE = CONFIG['base_url'].rstrip('/')
HOST = BASE.split('://', 1)[1]
LOCK = threading.RLock()
RATE_LOCK = threading.Lock()
RESOURCE_LOCK = threading.Lock()
RESOURCES = {'sample_time': None, 'cpu_times': None, 'data': None}
HASH_SLOTS = threading.BoundedSemaphore(2)
REPORT_SLOTS = threading.BoundedSemaphore(8)
DB_BUSY_TIMEOUT = 1.0
READ_TIMEOUT = 1.0
STOP = threading.Event()
STATUS = {'last_poll': None, 'last_error': None}
RATES = {'sample_time': None, 'run': None, 'users': {}}
logger = logging.getLogger('treeroot')


@contextmanager
def database(readonly=False):
    db = sqlite3.connect(DB.as_uri() + '?mode=ro' if readonly else DB, uri=readonly, timeout=DB_BUSY_TIMEOUT)
    db.row_factory = sqlite3.Row
    db.execute('PRAGMA foreign_keys=ON')
    if readonly:
        deadline = time.monotonic() + READ_TIMEOUT
        db.set_progress_handler(lambda: int(time.monotonic() > deadline), 10000)
    try:
        yield db
        db.commit()
    except BaseException:
        db.rollback()
        raise
    finally:
        db.close()


@contextmanager
def report_database():
    if not REPORT_SLOTS.acquire(blocking=False):
        raise HTTPException(503, '查询繁忙，请稍后重试', headers={'Retry-After': '1'})
    try:
        with database(readonly=True) as db:
            yield db
    finally:
        REPORT_SLOTS.release()


@contextmanager
def user_change_lock():
    if not LOCK.acquire(timeout=0.25):
        raise HTTPException(503, '正在处理用户或统计更新，请稍后重试', headers={'Retry-After': '1'})
    try:
        yield
    finally:
        LOCK.release()


def password_hash(password, salt=None):
    if not HASH_SLOTS.acquire(blocking=False):
        raise HTTPException(429, '登录或改密请求繁忙，请稍后重试', headers={'Retry-After': '1'})
    try:
        salt = salt or secrets.token_hex(16)
        digest = hashlib.pbkdf2_hmac('sha256', password.encode(), bytes.fromhex(salt), 600000).hex()
        return salt + ':' + digest
    finally:
        HASH_SLOTS.release()


def password_matches(password, stored):
    return hmac.compare_digest(password_hash(password, stored.split(':', 1)[0]), stored)


def initialize():
    DATA.mkdir(mode=0o700, parents=True, exist_ok=True)
    with database() as db:
        db.execute('PRAGMA journal_mode=WAL')
        db.executescript('''
        CREATE TABLE IF NOT EXISTS admin(id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password_hash TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS sessions(token_hash TEXT PRIMARY KEY, csrf TEXT NOT NULL, expires INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS login_attempts(ip TEXT NOT NULL, ts INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS users(
          id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, client_uuid TEXT NOT NULL UNIQUE,
          email TEXT NOT NULL UNIQUE, token TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1,
          created INTEGER NOT NULL, upload INTEGER NOT NULL DEFAULT 0, download INTEGER NOT NULL DEFAULT 0);
        CREATE TABLE IF NOT EXISTS counters(name TEXT PRIMARY KEY, run TEXT NOT NULL, value INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS traffic(ts INTEGER NOT NULL, user_id INTEGER NOT NULL REFERENCES users(id),
          upload INTEGER NOT NULL, download INTEGER NOT NULL, PRIMARY KEY(ts,user_id));
        CREATE TABLE IF NOT EXISTS domains(user_id INTEGER NOT NULL REFERENCES users(id), domain TEXT NOT NULL,
          connections INTEGER NOT NULL, last_seen INTEGER NOT NULL, PRIMARY KEY(user_id,domain));
        CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY, ts INTEGER NOT NULL,
          user_id INTEGER NOT NULL REFERENCES users(id), domain TEXT NOT NULL, port INTEGER NOT NULL, network TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS events_user_time ON events(user_id,ts DESC);
        CREATE INDEX IF NOT EXISTS events_time ON events(ts DESC);
        CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY, value TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY,ts INTEGER NOT NULL,action TEXT NOT NULL,user_id INTEGER);
        ''')
        if not db.execute('SELECT 1 FROM admin').fetchone():
            db.execute('INSERT INTO admin VALUES(1,?,?)', (CONFIG['admin_username'], CONFIG['admin_password_hash']))
        if not db.execute('SELECT 1 FROM users').fetchone():
            db.execute('INSERT INTO users(name,client_uuid,email,token,created) VALUES(?,?,?,?,?)',
                       ('我的设备', CONFIG['initial_uuid'], 'owner@treeroot.local', CONFIG['initial_token'], int(time.time())))
    os.chmod(DB, 0o600)


def xray_api(command, *args):
    result = subprocess.run([XRAY, 'api', command, '--server=' + API, *args],
                            capture_output=True, text=True, timeout=12)
    if result.returncode:
        raise RuntimeError('Xray API ' + command + ' failed')
    if command == 'adu' and 'Added 1 user(s)' not in result.stdout:
        raise RuntimeError('Xray rejected the user update')
    if command == 'rmu' and 'Removed 1 user(s)' not in result.stdout:
        raise RuntimeError('Xray did not remove the user')
    return result.stdout


def user_client(user):
    return {'id': user['client_uuid'], 'email': user['email'], 'level': 0, 'alterId': 0}


def add_runtime_user(user):
    existing = json.loads(XRAY_CONFIG.read_text())
    inbound = next(i for i in existing['inbounds'] if i.get('tag') == 'websocket-in')
    inbound['settings']['clients'] = [user_client(user)]
    config = {'inbounds': [inbound]}
    fd, path = tempfile.mkstemp(dir=DATA, suffix='.json')
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(config, f)
        xray_api('adu', path)
    finally:
        Path(path).unlink(missing_ok=True)


def save_xray_config(db):
    config = json.loads(XRAY_CONFIG.read_text())
    inbound = next(x for x in config['inbounds'] if x.get('tag') == 'websocket-in')
    inbound['settings']['clients'] = [user_client(u) for u in db.execute('SELECT * FROM users WHERE enabled=1')]
    production_log = config.get('log', {})
    config['log'] = {'access': 'none', 'loglevel': 'none'}
    fd, filename = tempfile.mkstemp(dir=XRAY_CONFIG.parent, suffix='.json')
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(config, f, indent=2)
        os.chown(filename, -1, XRAY_CONFIG.stat().st_gid)
        os.chmod(filename, 0o640)
        result = subprocess.run([XRAY, 'run', '-test', '-config', filename], capture_output=True, timeout=10)
        if result.returncode:
            raise RuntimeError('Xray configuration validation failed')
        config['log'] = production_log
        with open(filename, 'w') as f:
            json.dump(config, f, indent=2)
        os.replace(filename, XRAY_CONFIG)
    finally:
        Path(filename).unlink(missing_ok=True)


def change_user(user_id, changes=None, new_name=None):
    with user_change_lock(), database() as db:
        db.execute('BEGIN IMMEDIATE')
        old = db.execute('SELECT * FROM users WHERE id=?', (user_id,)).fetchone() if user_id else None
        if user_id and old is None:
            raise HTTPException(404, '用户不存在')
        old = dict(old) if old else None
        old_config = None
        removed = added = False
        try:
            if not user_id:
                client_id = str(uuid.uuid4())
                cursor = db.execute('INSERT INTO users(name,client_uuid,email,token,created) VALUES(?,?,?,?,?)',
                                    (new_name, client_id, 'u-' + uuid.uuid4().hex + '@treeroot.local',
                                     secrets.token_urlsafe(32), int(time.time())))
                user_id = cursor.lastrowid
            else:
                for key, value in changes.items():
                    if key not in ('name', 'enabled', 'client_uuid', 'token'):
                        raise ValueError('Invalid user field')
                    db.execute('UPDATE users SET ' + key + '=? WHERE id=?', (value, user_id))
            new = dict(db.execute('SELECT * FROM users WHERE id=?', (user_id,)).fetchone())
            credentials_changed = not old or old['enabled'] != new['enabled'] or old['client_uuid'] != new['client_uuid']
            if credentials_changed:
                old_config = XRAY_CONFIG.read_bytes()
                if old and old['enabled']:
                    xray_api('rmu', '-tag=websocket-in', old['email'])
                    removed = True
                if new['enabled']:
                    add_runtime_user(new)
                    added = True
                save_xray_config(db)
            action = 'create_user' if not old else ('reset_subscription' if old['token'] != new['token'] else 'update_user')
            db.execute('INSERT INTO audit(ts,action,user_id) VALUES(?,?,?)', (int(time.time()), action, user_id))
            return public_user(new)
        except BaseException:
            try:
                if added:
                    xray_api('rmu', '-tag=websocket-in', new['email'])
                if removed:
                    add_runtime_user(old)
                if old_config is not None:
                    XRAY_CONFIG.write_bytes(old_config)
            except Exception:
                logger.exception('Failed to restore Xray after a rejected user change')
            raise


def collect_stats():
    run_id = subprocess.check_output(['systemctl', 'show', '-p', 'InvocationID', '--value', 'xray'], text=True, timeout=5).strip()
    response = json.loads(xray_api('statsquery', '-pattern=user>>>'))
    run_after = subprocess.check_output(['systemctl', 'show', '-p', 'InvocationID', '--value', 'xray'], text=True, timeout=5).strip()
    if not run_id or run_after != run_id:
        return
    now = int(time.time())
    sample_time = time.monotonic()
    with LOCK, database() as db:
        users = {u['email']: u['id'] for u in db.execute('SELECT id,email FROM users')}
        counters = {row['name']: row for row in db.execute('SELECT name,run,value FROM counters')}
        elapsed = sample_time - RATES['sample_time'] if RATES['sample_time'] is not None else 0
        rates_ready = elapsed > 0 and RATES['run'] == run_id
        rates = {user_id: {'upload': 0, 'download': 0} for user_id in users.values()}
        for item in response.get('stat', []):
            match = re.fullmatch(r'user>>>(.+)>>>traffic>>>(uplink|downlink)', item['name'])
            if not match or match[1] not in users:
                continue
            value = int(item.get('value', 0))
            previous = counters.get(item['name'])
            delta = value if not previous or previous['run'] != run_id or value < previous['value'] else value - previous['value']
            if rates_ready and previous and previous['run'] == run_id and value >= previous['value']:
                direction = 'upload' if match[2] == 'uplink' else 'download'
                rates[users[match[1]]][direction] += delta / elapsed
            db.execute('INSERT INTO counters VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET run=excluded.run,value=excluded.value',
                       (item['name'], run_id, value))
            if delta:
                upload, download = (delta, 0) if match[2] == 'uplink' else (0, delta)
                user_id = users[match[1]]
                db.execute('UPDATE users SET upload=upload+?,download=download+? WHERE id=?', (upload, download, user_id))
                db.execute('INSERT INTO traffic VALUES(?,?,?,?) ON CONFLICT(ts,user_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download',
                           (now // 60 * 60, user_id, upload, download))
        db.execute('DELETE FROM traffic WHERE ts<?', (now - 31 * 86400,))
    with RATE_LOCK:
        RATES.update(sample_time=sample_time, run=run_id, users=rates if rates_ready else {})
        STATUS['last_poll'] = now


ACCESS_PATTERN = re.compile(r'accepted\s+(tcp|udp):(.+):(\d+)\s+\[.*?\].*?email:\s*(\S+)')


def collect_access():
    if not ACCESS_LOG.exists():
        return
    with LOCK:
        with database(readonly=True) as db:
            saved = db.execute("SELECT value FROM state WHERE key='log_cursor'").fetchone()
            users = {u['email']: u['id'] for u in db.execute('SELECT id,email FROM users')}
        inode, offset = json.loads(saved[0]) if saved else (0, 0)
        files = []
        current_inode = ACCESS_LOG.stat().st_ino
        if inode and inode != current_inode:
            previous = ACCESS_LOG.with_name('access.log.1')
            if previous.exists() and previous.stat().st_ino == inode:
                files.append((previous, offset))
            offset = 0
        if ACCESS_LOG.stat().st_size < offset:
            offset = 0
        files.append((ACCESS_LOG, offset))
        now = int(time.time())
        rows, domains = [], {}
        remaining = 2000
        for path, start in files:
            with path.open('rb') as stream:
                stream.seek(start)
                for _ in range(remaining):
                    position = stream.tell()
                    line = stream.readline()
                    if not line or not line.endswith(b'\n'):
                        stream.seek(position)
                        break
                    remaining -= 1
                    text = line.decode('utf-8', errors='replace')
                    match = ACCESS_PATTERN.search(text)
                    if not match or match[4] not in users:
                        continue
                    domain = match[2].strip('[]').lower()[:253]
                    user_id = users[match[4]]
                    prefix = re.match(r'^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})', text)
                    event_time = int(datetime.strptime(prefix[1], '%Y/%m/%d %H:%M:%S').replace(tzinfo=timezone.utc).timestamp()) if prefix else now
                    rows.append((event_time, user_id, domain, int(match[3]), match[1]))
                    count, last_seen = domains.get((user_id, domain), (0, 0))
                    domains[user_id, domain] = (count + 1, max(last_seen, event_time))
                cursor = [os.fstat(stream.fileno()).st_ino, stream.tell()]
                # Drain the rotated file before moving the cursor to the new file.
                if stream.peek(1):
                    break
        with database() as db:
            db.execute('BEGIN IMMEDIATE')
            db.executemany('INSERT INTO events(ts,user_id,domain,port,network) VALUES(?,?,?,?,?)', rows)
            db.executemany('INSERT INTO domains VALUES(?,?,?,?) ON CONFLICT(user_id,domain) DO UPDATE SET connections=connections+excluded.connections,last_seen=MAX(last_seen,excluded.last_seen)',
                           [(user_id, domain, count, last_seen) for (user_id, domain), (count, last_seen) in domains.items()])
            db.execute("INSERT INTO state VALUES('log_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (json.dumps(cursor),))
            db.execute('DELETE FROM events WHERE ts<?', (now - 7 * 86400,))
            db.execute('DELETE FROM sessions WHERE expires<?', (now,))
            db.execute('DELETE FROM login_attempts WHERE ts<?', (now - 900,))


def collector():
    while not STOP.is_set():
        try:
            collect_stats()
            collect_access()
            with RATE_LOCK:
                STATUS['last_error'] = None
        except Exception as exc:
            with RATE_LOCK:
                STATUS['last_error'] = type(exc).__name__
            logger.exception('Collector failed')
        STOP.wait(5)


@asynccontextmanager
async def lifespan(app):
    initialize()
    thread = threading.Thread(target=collector, daemon=True)
    thread.start()
    yield
    STOP.set()
    thread.join(timeout=15)


app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)


@app.exception_handler(sqlite3.OperationalError)
async def database_error(request, error):
    code = getattr(error, 'sqlite_errorcode', 0) & 0xff
    if code in (sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_INTERRUPT):
        return JSONResponse({'detail': '数据库繁忙或查询超时，请稍后重试'}, status_code=503, headers={'Retry-After': '1'})
    logger.error('Database operation failed', exc_info=(type(error), error, error.__traceback__))
    return JSONResponse({'detail': '数据库暂时不可用'}, status_code=500)


@app.middleware('http')
async def security(request, call_next):
    if request.method not in ('GET', 'HEAD', 'OPTIONS'):
        origin = request.headers.get('origin')
        if origin and origin != BASE:
            return JSONResponse({'detail': '请求来源不匹配'}, status_code=403)
        if request.headers.get('content-type', '').split(';', 1)[0] != 'application/json':
            return JSONResponse({'detail': '请使用 JSON 请求'}, status_code=415)
    response = await call_next(request)
    response.headers['Cache-Control'] = 'public, max-age=86400' if request.url.path.startswith('/assets/') and response.status_code == 200 else 'no-store'
    response.headers['X-Content-Type-Options'] = 'nosniff'
    response.headers['Referrer-Policy'] = 'no-referrer'
    response.headers['Content-Security-Policy'] = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
    return response


def authenticated(request: Request):
    token = request.cookies.get('__Host-treeroot', '')
    digest = hashlib.sha256(token.encode()).hexdigest()
    with database(readonly=True) as db:
        session = db.execute('SELECT * FROM sessions WHERE token_hash=? AND expires>?', (digest, int(time.time()))).fetchone()
    if not session:
        raise HTTPException(401, '请先登录')
    if request.method not in ('GET', 'HEAD') and not hmac.compare_digest(request.headers.get('x-csrf-token', ''), session['csrf']):
        raise HTTPException(403, '会话校验失败，请刷新后重试')
    return dict(session)


class Login(BaseModel):
    username: str = Field(max_length=64)
    password: str


@app.post('/api/login')
def login(body: Login, request: Request, response: Response):
    ip = request.client.host if request.client else 'unknown'
    now = int(time.time())
    with database() as db:
        attempts = db.execute('SELECT COUNT(*) FROM login_attempts WHERE ip=? AND ts>?', (ip, now - 900)).fetchone()[0]
        if attempts >= 8:
            raise HTTPException(429, '尝试次数过多，请 15 分钟后重试')
        admin = db.execute('SELECT * FROM admin WHERE id=1').fetchone()
        if body.username != admin['username'] or not password_matches(body.password, admin['password_hash']):
            db.execute('INSERT INTO login_attempts VALUES(?,?)', (ip, now))
            db.commit()
            raise HTTPException(401, '账号或密码不正确')
        token, csrf = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        db.execute('INSERT INTO sessions VALUES(?,?,?)', (hashlib.sha256(token.encode()).hexdigest(), csrf, now + 12 * 3600))
    response.set_cookie('__Host-treeroot', token, max_age=12 * 3600, httponly=True, secure=True, samesite='strict', path='/')
    return {'csrf': csrf, 'username': body.username}


@app.get('/api/me')
def me(request: Request):
    try:
        session = authenticated(request)
    except HTTPException as error:
        if error.status_code == 401:
            return {'authenticated': False}
        raise
    with database(readonly=True) as db:
        username = db.execute('SELECT username FROM admin WHERE id=1').fetchone()[0]
    return {'authenticated': True, 'csrf': session['csrf'], 'username': username, 'base_url': BASE}


@app.post('/api/logout')
def logout(response: Response, session=Depends(authenticated)):
    with database() as db:
        db.execute('DELETE FROM sessions WHERE token_hash=?', (session['token_hash'],))
    response.delete_cookie('__Host-treeroot', secure=True, httponly=True, samesite='strict', path='/')
    return {'ok': True}


class PasswordChange(BaseModel):
    current: str
    password: str


@app.post('/api/password')
def update_password(body: PasswordChange, session=Depends(authenticated)):
    with database() as db:
        current = db.execute('SELECT password_hash FROM admin WHERE id=1').fetchone()[0]
        if not password_matches(body.current, current):
            raise HTTPException(400, '原密码不正确')
        db.execute('UPDATE admin SET password_hash=? WHERE id=1', (password_hash(body.password),))
        db.execute('DELETE FROM sessions WHERE token_hash!=?', (session['token_hash'],))
    return {'ok': True}


def public_user(user):
    return {k: user[k] for k in ('id', 'name', 'enabled', 'created', 'upload', 'download')} | {
        'clash_url': BASE + '/sub/' + user['token'] + '/clash.yaml',
        'shadowrocket_url': BASE + '/sub/' + user['token'] + '/shadowrocket',
    }


@app.get('/api/users')
def users(session=Depends(authenticated)):
    with report_database() as db:
        rows = db.execute('''SELECT u.*,COALESCE(d.domains,0) domains,COALESCE(d.connections,0) connections
                             FROM users u LEFT JOIN
                             (SELECT user_id,COUNT(*) domains,SUM(connections) connections FROM domains GROUP BY user_id) d
                             ON d.user_id=u.id ORDER BY u.id''').fetchall()
    return [public_user(user) | {'domains': user['domains'], 'connections': user['connections']} for user in rows]


class NewUser(BaseModel):
    name: str = Field(min_length=1, max_length=64)


class UserUpdate(BaseModel):
    name: str | None = Field(default=None, min_length=1, max_length=64)
    enabled: bool | None = None


@app.post('/api/users')
def create_user(body: NewUser, session=Depends(authenticated)):
    if not body.name.strip():
        raise HTTPException(422, '请输入用户名称')
    try:
        return change_user(None, new_name=body.name.strip())
    except (RuntimeError, subprocess.SubprocessError):
        logger.exception('Could not create proxy user')
        raise HTTPException(503, '代理服务更新失败，改动已回滚')


@app.patch('/api/users/{user_id}')
def update_user(user_id: int, body: UserUpdate, session=Depends(authenticated)):
    changes = body.model_dump(exclude_none=True)
    if 'name' in changes:
        changes['name'] = changes['name'].strip()
        if not changes['name']:
            raise HTTPException(422, '请输入用户名称')
    try:
        return change_user(user_id, changes)
    except (RuntimeError, subprocess.SubprocessError):
        logger.exception('Could not update proxy user')
        raise HTTPException(503, '代理服务更新失败，改动已回滚')


@app.post('/api/users/{user_id}/reset')
def reset_subscription(user_id: int, session=Depends(authenticated)):
    try:
        return change_user(user_id, {'client_uuid': str(uuid.uuid4()), 'token': secrets.token_urlsafe(32)})
    except (RuntimeError, subprocess.SubprocessError):
        logger.exception('Could not reset subscription')
        raise HTTPException(503, '订阅重置失败，改动已回滚')


def rate_snapshot(user_id=None):
    with RATE_LOCK:
        collector_status = dict(STATUS)
        rate_users = [rate for uid, rate in RATES['users'].items() if not user_id or uid == user_id]
        fresh = STATUS['last_poll'] and time.time() - STATUS['last_poll'] < 30 and not STATUS['last_error']
        speed_upload = sum(rate['upload'] for rate in rate_users) if fresh and RATES['users'] else None
        speed_download = sum(rate['download'] for rate in rate_users) if fresh and RATES['users'] else None
    return {'collector': collector_status, 'speed_upload': speed_upload, 'speed_download': speed_download}


@app.get('/api/rates')
def rates(user_id: int | None = None, session=Depends(authenticated)):
    return rate_snapshot(user_id)


def server_resource_snapshot():
    with RESOURCE_LOCK:
        sample_time = time.monotonic()
        if RESOURCES['data'] is not None and sample_time - RESOURCES['sample_time'] < 4:
            return RESOURCES['data']
        data = {'sampled_at': int(time.time()), 'cpu_percent': None, 'cpu_cores': os.cpu_count(),
                'load_average': None, 'memory': None, 'disk': None, 'uptime_seconds': None}
        try:
            # Guest time is already included in user/nice; exclude those duplicate fields.
            fields = [int(value) for value in Path('/proc/stat').read_text().splitlines()[0].split()[1:9]]
            total, idle = sum(fields), fields[3] + fields[4]
            previous = RESOURCES['cpu_times']
            if previous is not None and total > previous[0] and idle >= previous[1]:
                data['cpu_percent'] = round(max(0, min(100, 100 * (1 - (idle - previous[1]) / (total - previous[0])))), 1)
            RESOURCES['cpu_times'] = (total, idle)
        except (OSError, ValueError, IndexError):
            RESOURCES['cpu_times'] = None
        try:
            data['load_average'] = list(os.getloadavg())
        except OSError:
            pass
        try:
            memory = {key: int(value.split()[0]) * 1024 for key, value in
                      (line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())}
            total = memory['MemTotal']
            available = min(total, memory['MemAvailable'])
            data['memory'] = {'total': total, 'used': total - available, 'percent': round((total - available) / total * 100, 1)}
        except (OSError, ValueError, KeyError, IndexError, ZeroDivisionError):
            pass
        try:
            disk = shutil.disk_usage(DATA)
            data['disk'] = {'total': disk.total, 'used': disk.used, 'percent': round(disk.used / disk.total * 100, 1)}
        except (OSError, ZeroDivisionError):
            pass
        try:
            data['uptime_seconds'] = int(float(Path('/proc/uptime').read_text().split()[0]))
        except (OSError, ValueError, IndexError):
            pass
        RESOURCES.update(sample_time=sample_time, data=data)
        return data


@app.get('/api/resources')
def resources(session=Depends(authenticated)):
    return server_resource_snapshot()


@app.get('/api/overview')
def overview(user_id: int | None = None, session=Depends(authenticated)):
    since = int(time.time()) - 24 * 3600
    where, args = (' WHERE id=?', [user_id]) if user_id else ('', [])
    with report_database() as db:
        totals = db.execute('SELECT COALESCE(SUM(upload),0) upload,COALESCE(SUM(download),0) download,COUNT(*) users,COALESCE(SUM(enabled),0) active FROM users' + where, args).fetchone()
        condition, values = (' AND user_id=?', [user_id]) if user_id else ('', [])
        series = [dict(r) for r in db.execute('SELECT CAST(ts/3600 AS INTEGER)*3600 ts,SUM(upload) upload,SUM(download) download FROM traffic WHERE ts>=?' + condition + ' GROUP BY CAST(ts/3600 AS INTEGER) ORDER BY ts', [since, *values])]
        domains = [dict(r) for r in db.execute('SELECT domain,SUM(connections) connections,MAX(last_seen) last_seen FROM domains' + (' WHERE user_id=?' if user_id else '') + ' GROUP BY domain ORDER BY connections DESC LIMIT 12', values)]
        connections = db.execute('SELECT COUNT(*) FROM events WHERE ts>=?' + condition, [since, *values]).fetchone()[0]
    return dict(totals) | {'day_upload': sum(row['upload'] for row in series), 'day_download': sum(row['download'] for row in series),
                           'connections': connections, 'series': series, 'domains': domains} | rate_snapshot(user_id)


@app.get('/api/activity')
def activity(user_id: int | None = None, search: str = '', page: int = Query(default=1, ge=1),
             page_size: int = Query(default=25, ge=1, le=100),
             before_id: int | None = Query(default=None, ge=1), session=Depends(authenticated)):
    clauses, args = ['e.ts>=?'], [int(time.time()) - 7 * 86400]
    if user_id:
        clauses.append('e.user_id=?')
        args.append(user_id)
    if search:
        clauses.append('e.domain LIKE ?')
        args.append('%' + search[:253] + '%')
    with report_database() as db:
        if before_id is None:
            before_id = db.execute('SELECT MAX(e.id) FROM events e WHERE ' + ' AND '.join(clauses), args).fetchone()[0]
        clauses.append('e.id<=?')
        args.append(before_id or 0)
        where = ' WHERE ' + ' AND '.join(clauses)
        total = db.execute('SELECT COUNT(*) FROM events e' + where, args).fetchone()[0]
        pages = max(1, (total + page_size - 1) // page_size)
        page = min(page, pages)
        # Apply the offset before joining users, so skipped rows do not perform joins.
        items = [dict(r) for r in db.execute('SELECT e.id,e.ts,e.domain,e.port,e.network,e.user_id,u.name FROM (SELECT e.id,e.ts,e.domain,e.port,e.network,e.user_id FROM events e' + where + ' ORDER BY e.id DESC LIMIT ? OFFSET ?) e JOIN users u ON u.id=e.user_id ORDER BY e.id DESC', [*args, page_size, (page - 1) * page_size])]
    return {'items': items, 'total': total, 'page': page, 'page_size': page_size, 'pages': pages, 'before_id': before_id}


def subscription_user(token):
    with database(readonly=True) as db:
        user = db.execute('SELECT * FROM users WHERE token=? AND enabled=1', (token,)).fetchone()
    if not user:
        raise HTTPException(404, '订阅不存在或已禁用')
    return dict(user)


@app.get('/sub/{token}/{kind}')
def subscription(token: str, kind: str):
    if kind not in ('clash.yaml', 'shadowrocket'):
        raise HTTPException(404)
    user = subscription_user(token)
    name = '美国-圣何塞 · ' + user['name']
    node = {'name': name, 'type': 'vmess', 'server': HOST, 'port': 443, 'uuid': user['client_uuid'],
            'alterId': 0, 'cipher': 'auto', 'udp': True, 'tls': True, 'servername': HOST,
            'skip-cert-verify': False, 'network': 'ws', 'ws-opts': {'path': CONFIG['ws_path'], 'headers': {'Host': HOST}}}
    headers = {'profile-update-interval': '6',
               'profile-title': 'base64:' + base64.b64encode(('TreeRoot · ' + user['name']).encode()).decode(),
               'subscription-userinfo': f"upload={user['upload']}; download={user['download']}; total=0; expire=0"}
    if kind == 'clash.yaml':
        template = json.loads((ROOT / 'subscription-template.json').read_text())
        template['proxies'] = [node]
        template['proxy-groups'] = [{'name': '美国自建', 'type': 'select', 'proxies': [name]}]
        headers['Content-Disposition'] = 'attachment; filename=TreeRoot.yaml'
        return Response(json.dumps(template, ensure_ascii=False, indent=2), media_type='text/yaml', headers=headers)
    vmess = {'v': '2', 'ps': name, 'add': HOST, 'port': '443', 'id': user['client_uuid'], 'aid': '0',
             'scy': 'auto', 'net': 'ws', 'type': 'none', 'host': HOST, 'path': CONFIG['ws_path'],
             'tls': 'tls', 'sni': HOST, 'alpn': 'http/1.1'}
    uri = 'vmess://' + base64.b64encode(json.dumps(vmess, ensure_ascii=False).encode()).decode()
    return PlainTextResponse(base64.b64encode((uri + '\n').encode()).decode(), headers=headers)


@app.get('/{token}/clash.yaml', include_in_schema=False)
def legacy_clash(token: str):
    return subscription(token, 'clash.yaml')


@app.get('/{token}/shadowrocket', include_in_schema=False)
def legacy_shadowrocket(token: str):
    return subscription(token, 'shadowrocket')


@app.get('/')
@app.get('/admin')
def index():
    return FileResponse(ROOT / 'index.html')


@app.get('/assets/{name}')
def asset(name: str):
    if name not in ('app.js', 'style.css', 'echarts.min.js'):
        raise HTTPException(404)
    return FileResponse(ROOT / name)
