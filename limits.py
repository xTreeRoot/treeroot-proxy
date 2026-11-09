"""Per-account quotas and a loopback SOCKS5 TCP/UDP bandwidth limiter."""
import asyncio
from contextlib import contextmanager
from datetime import datetime, timedelta
import hashlib
import hmac
import ipaddress
import json
import logging
import os
from pathlib import Path
import signal
import socket
import sqlite3
import struct
import time
from zoneinfo import ZoneInfo

ZONE = ZoneInfo('Asia/Shanghai')
PORT = 10002
PREFIX = 'treeroot-limit-'
logger = logging.getLogger('treeroot.limits')


def day_window(now=None):
    start = datetime.fromtimestamp(time.time() if now is None else now, ZONE).replace(hour=0, minute=0, second=0, microsecond=0)
    return start.date().isoformat(), int(start.timestamp()), int((start + timedelta(days=1)).timestamp())


def initialize(db):
    db.executescript('''
        CREATE TABLE IF NOT EXISTS user_limits(
          user_id INTEGER PRIMARY KEY REFERENCES users(id),total_bytes INTEGER NOT NULL DEFAULT 0,
          daily_bytes INTEGER NOT NULL DEFAULT 0,speed_bps INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS limit_usage(
          user_id INTEGER PRIMARY KEY REFERENCES users(id),total_bytes INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS limit_daily(
          user_id INTEGER NOT NULL REFERENCES users(id),day TEXT NOT NULL,bytes INTEGER NOT NULL,
          PRIMARY KEY(user_id,day));
    ''')
    seed_usage(db)


def seed_usage(db):
    day, start, end = day_window()
    db.execute('INSERT OR IGNORE INTO limit_usage SELECT id,upload+download FROM users')
    db.execute('''INSERT OR IGNORE INTO limit_daily
        SELECT u.id,?,COALESCE(SUM(t.upload+t.download),0) FROM users u LEFT JOIN traffic t
        ON t.user_id=u.id AND t.ts>=? AND t.ts<? GROUP BY u.id''', (day, start, end))


def snapshot(db):
    day, start, end = day_window()
    active = bool(db.execute("SELECT 1 FROM state WHERE key='limits_active'").fetchone())
    rows = db.execute('''WITH daily AS (
        SELECT user_id,SUM(upload+download) bytes FROM traffic WHERE ts>=? AND ts<? GROUP BY user_id)
        SELECT u.id,u.email,u.client_uuid,u.enabled,
          COALESCE(l.total_bytes,0) total_bytes,COALESCE(l.daily_bytes,0) daily_bytes,
          COALESCE(l.speed_bps,0) speed_bps,COALESCE(l.updated,0) updated,
          CASE WHEN ? THEN COALESCE(t.total_bytes,u.upload+u.download)
            ELSE MAX(u.upload+u.download,COALESCE(t.total_bytes,0)) END total_used,
          CASE WHEN ? THEN COALESCE(d.bytes,COALESCE(daily.bytes,0))
            ELSE MAX(COALESCE(d.bytes,0),COALESCE(daily.bytes,0)) END daily_used
        FROM users u LEFT JOIN user_limits l ON l.user_id=u.id
        LEFT JOIN limit_usage t ON t.user_id=u.id
        LEFT JOIN limit_daily d ON d.user_id=u.id AND d.day=?
        LEFT JOIN daily ON daily.user_id=u.id''', (start, end, active, active, day)).fetchall()
    return day, {row['id']: dict(row) for row in rows}


def activate(db):
    """Capture the final legacy counters once, before switching Xray to the gateway."""
    if db.execute("SELECT 1 FROM state WHERE key='limits_active'").fetchone():
        return
    seed_usage(db)
    day, rows = snapshot(db)
    for uid, row in rows.items():
        db.execute('UPDATE limit_usage SET total_bytes=? WHERE user_id=?', (row['total_used'], uid))
        db.execute('UPDATE limit_daily SET bytes=? WHERE user_id=? AND day=?', (row['daily_used'], uid, day))
    db.execute("INSERT INTO state VALUES('limits_active','1')")


def public_limits(row):
    blocked = 'total' if row['total_bytes'] and row['total_used'] >= row['total_bytes'] else (
        'daily' if row['daily_bytes'] and row['daily_used'] >= row['daily_bytes'] else None)
    return {key: row[key] for key in ('total_bytes', 'daily_bytes', 'speed_bps', 'total_used', 'daily_used')} | {'blocked': blocked}


def configured(config):
    return any(item.get('tag', '').startswith(PREFIX) for item in config.get('outbounds', []))


def xray_config(config, users):
    config = json.loads(json.dumps(config))
    services = config['api']['services']
    if 'RoutingService' not in services:
        services.append('RoutingService')
    config['outbounds'] = [item for item in config.get('outbounds', []) if not item.get('tag', '').startswith(PREFIX)]
    rules = [item for item in config['routing']['rules'] if not item.get('ruleTag', '').startswith(PREFIX)]
    for user in users:
        tag = PREFIX + str(user['id']) + '-' + hashlib.sha256(user['client_uuid'].encode()).hexdigest()[:10]
        config['outbounds'].append({'tag': tag, 'protocol': 'socks', 'settings': {'servers': [{
            'address': '127.0.0.1', 'port': PORT,
            'users': [{'user': user['email'], 'pass': user['client_uuid']}]}]}})
        rules.append({'type': 'field', 'ruleTag': tag, 'inboundTag': ['websocket-in'],
                      'user': [user['email']], 'outboundTag': tag})
    # Unknown or not-yet-synchronized users must never fall back to the direct outbound.
    rules.append({'type': 'field', 'ruleTag': PREFIX + 'fallback', 'inboundTag': ['websocket-in'], 'outboundTag': 'block'})
    config['routing']['rules'] = rules
    return config


class QuotaExceeded(Exception):
    pass


class Account:
    def __init__(self, row, day):
        self.lock = asyncio.Lock()
        self.writers = set()
        self.next_at = 0
        self.day = day
        self.row = row

    def close(self):
        for writer in tuple(self.writers):
            writer.close()


class Budget:
    def __init__(self, database):
        self.database = database
        self.accounts = {}
        self.by_email = {}
        self.loaded_at = 0
        self.write_lock = asyncio.Lock()
        self.started_at = time.time()

    def read(self):
        with self.database() as db:
            seed_usage(db)
            day, rows = snapshot(db)
            sampled = db.execute("SELECT value FROM state WHERE key='limits_stats_sample'").fetchone()
            return day, rows, float(sampled[0]) if sampled else 0

    async def reload(self):
        day, rows, sampled = await asyncio.to_thread(self.read)
        if not self.loaded_at and sampled < self.started_at:
            return False
        for uid, row in rows.items():
            account = self.accounts.get(uid)
            if account is None:
                account = self.accounts[uid] = Account(row, day)
            else:
                async with account.lock:
                    old = account.row
                    row['total_used'] = max(row['total_used'], old['total_used'])
                    if account.day == day:
                        row['daily_used'] = max(row['daily_used'], old['daily_used'])
                    if old['speed_bps'] != row['speed_bps']:
                        account.next_at = 0
                    if old['client_uuid'] != row['client_uuid']:
                        account.close()
                    account.row, account.day = row, day
            if not row['enabled'] or public_limits(row)['blocked']:
                account.close()
        for uid in self.accounts.keys() - rows.keys():
            self.accounts.pop(uid).close()
        self.by_email = {account.row['email']: account for account in self.accounts.values()}
        self.loaded_at = time.monotonic()
        return True

    def persist(self, values):
        with self.database() as db:
            for uid, day, total, daily in values:
                db.execute('INSERT INTO limit_usage VALUES(?,?) ON CONFLICT(user_id) DO UPDATE '
                           'SET total_bytes=MAX(total_bytes,excluded.total_bytes)', (uid, total))
                db.execute('INSERT INTO limit_daily VALUES(?,?,?) ON CONFLICT(user_id,day) DO UPDATE '
                           'SET bytes=MAX(bytes,excluded.bytes)', (uid, day, daily))
            db.execute("INSERT INTO state VALUES('limits_heartbeat',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                       (str(time.time()),))

    async def flush(self, accounts=None):
        values = [(a.row['id'], a.day, a.row['total_used'], a.row['daily_used'])
                  for a in (self.accounts.values() if accounts is None else accounts)]
        async with self.write_lock:
            await asyncio.to_thread(self.persist, values)

    async def take(self, account, requested, packet=False):
        while True:
            if time.monotonic() - self.loaded_at > 5:
                raise QuotaExceeded('Limiter configuration is unavailable')
            if account.day != day_window()[0]:
                await self.flush([account])
                await self.reload()
                continue
            async with account.lock:
                row = account.row
                if not row['enabled'] or public_limits(row)['blocked']:
                    raise QuotaExceeded('Quota exhausted or account disabled')
                remaining = min(row['total_bytes'] - row['total_used'] if row['total_bytes'] else requested,
                                row['daily_bytes'] - row['daily_used'] if row['daily_bytes'] else requested)
                if packet and remaining < requested:
                    raise QuotaExceeded('Insufficient datagram quota')
                rate = row['speed_bps'] / 8
                count = min(requested, remaining, max(1, int(rate / 10)) if rate and not packet else requested)
                delay = account.next_at - time.monotonic() if rate else 0
                if delay <= 0:
                    row['total_used'] += count
                    row['daily_used'] += count
                    account.next_at = time.monotonic() + count / rate if rate else 0
                    # Commit capped traffic before granting it, so a crash cannot replenish quota.
                    if row['total_bytes'] or row['daily_bytes']:
                        await self.flush([account])
                    return count
            await asyncio.sleep(min(delay, .1))

    async def maintain(self):
        while True:
            try:
                await self.flush()
                await self.reload()
            except (OSError, sqlite3.Error):
                logger.exception('Could not refresh account limits')
            await asyncio.sleep(1)


async def destination(host, port, kind):
    if not 1 <= port <= 65535:
        raise ValueError('Invalid destination port')
    addresses = await asyncio.wait_for(asyncio.get_running_loop().getaddrinfo(host, port, type=kind), 10)
    if not addresses or any(not ipaddress.ip_address(item[4][0]).is_global or
                            ipaddress.ip_address(item[4][0]).is_multicast for item in addresses):
        raise ValueError('Private destinations are not allowed')
    return addresses


async def connect(host, port):
    addresses = await destination(host, port, socket.SOCK_STREAM)
    for item in addresses:
        try:
            return await asyncio.wait_for(asyncio.open_connection(item[4][0], port, family=item[0]), 10)
        except OSError:
            continue
    raise OSError('Destination unavailable')


async def read_address(reader):
    kind = (await reader.readexactly(1))[0]
    if kind in (1, 4):
        return socket.inet_ntop(socket.AF_INET if kind == 1 else socket.AF_INET6,
                               await reader.readexactly(4 if kind == 1 else 16))
    if kind == 3:
        size = (await reader.readexactly(1))[0]
        return (await reader.readexactly(size)).decode('ascii')
    raise ValueError('Unsupported address')


def pack_address(host, port):
    ip = ipaddress.ip_address(host)
    return bytes([1 if ip.version == 4 else 4]) + ip.packed + struct.pack('!H', port)


class DatagramPeer(asyncio.DatagramProtocol):
    def __init__(self, association, host, port):
        self.association, self.host, self.port = association, host, port

    def datagram_received(self, data, address):
        self.association.spawn(self.association.reply(data, self.host, self.port))


class DatagramAssociation(asyncio.DatagramProtocol):
    def __init__(self, gateway, account):
        self.gateway, self.account = gateway, account
        self.transport = None
        self.source = None
        self.peers = {}
        self.tasks = set()
        self.closed = False

    def connection_made(self, transport):
        self.transport = transport

    def spawn(self, coroutine):
        if self.closed or len(self.tasks) >= 128:
            coroutine.close()
            return
        task = asyncio.create_task(coroutine)
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)

    def datagram_received(self, data, address):
        if address[0] != '127.0.0.1' or len(data) < 7 or data[:3] != b'\x00\x00\x00':
            return
        if self.source is None:
            self.source = address
        if address == self.source:
            self.spawn(self.send(data))

    async def send(self, data):
        try:
            reader = asyncio.StreamReader()
            reader.feed_data(data[3:])
            reader.feed_eof()
            host = await read_address(reader)
            port = struct.unpack('!H', await reader.readexactly(2))[0]
            payload = await reader.read()
            if not payload:
                return
            peer = self.peers.get((host, port))
            if peer is None:
                if len(self.peers) >= 64:
                    return
                addresses = await destination(host, port, socket.SOCK_DGRAM)
                item = addresses[0]
                transport, _ = await asyncio.get_running_loop().create_datagram_endpoint(
                    lambda: DatagramPeer(self, item[4][0], port), remote_addr=item[4], family=item[0])
                # Concurrent first packets may resolve the same target simultaneously.
                peer = self.peers.setdefault((host, port), transport)
                if peer is not transport:
                    transport.close()
            await self.gateway.budget.take(self.account, len(payload), packet=True)
            peer.sendto(payload)
        except QuotaExceeded:
            self.account.close()
        except (OSError, ValueError, asyncio.IncompleteReadError, asyncio.TimeoutError):
            pass

    async def reply(self, payload, host, port):
        try:
            await self.gateway.budget.take(self.account, len(payload), packet=True)
            if not self.closed and self.source:
                self.transport.sendto(b'\x00\x00\x00' + pack_address(host, port) + payload, self.source)
        except (QuotaExceeded, OSError):
            self.account.close()

    async def close(self):
        self.closed = True
        if self.transport:
            self.transport.close()
        for transport in self.peers.values():
            transport.close()
        tasks = tuple(self.tasks)
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)


class Gateway:
    def __init__(self, budget):
        self.budget = budget

    async def pipe(self, reader, writer, account):
        while data := await asyncio.wait_for(reader.read(65536), 300):
            offset = 0
            while offset < len(data):
                count = await self.budget.take(account, len(data) - offset)
                writer.write(data[offset:offset + count])
                await writer.drain()
                offset += count
        if writer.can_write_eof():
            writer.write_eof()

    async def handle(self, reader, writer):
        upstream, account, association = None, None, None
        tasks = []
        try:
            async with asyncio.timeout(15):
                version, size = await reader.readexactly(2)
                if version != 5 or 2 not in await reader.readexactly(size):
                    writer.write(b'\x05\xff')
                    await writer.drain()
                    return
                writer.write(b'\x05\x02')
                await writer.drain()
                version, size = await reader.readexactly(2)
                username = (await reader.readexactly(size)).decode('utf-8')
                size = (await reader.readexactly(1))[0]
                password = await reader.readexactly(size)
                account = self.budget.by_email.get(username)
                if (version != 1 or not account or not account.row['enabled'] or
                        time.monotonic() - self.budget.loaded_at > 5 or
                        not hmac.compare_digest(password, account.row['client_uuid'].encode()) or
                        public_limits(account.row)['blocked']):
                    writer.write(b'\x01\x01')
                    await writer.drain()
                    return
                writer.write(b'\x01\x00')
                await writer.drain()
                version, command, reserved = await reader.readexactly(3)
                host = await read_address(reader)
                port = struct.unpack('!H', await reader.readexactly(2))[0]
                if version != 5 or reserved != 0 or command not in (1, 3):
                    raise ValueError('Unsupported SOCKS command')
                account.writers.add(writer)
                if command == 1:
                    upstream_reader, upstream = await connect(host, port)
                    writer.write(b'\x05\x00\x00' + pack_address('127.0.0.1', 0))
                else:
                    association = DatagramAssociation(self, account)
                    transport, _ = await asyncio.get_running_loop().create_datagram_endpoint(
                        lambda: association, local_addr=('127.0.0.1', 0))
                    writer.write(b'\x05\x00\x00' + pack_address(*transport.get_extra_info('sockname')))
                await writer.drain()
            if association:
                await reader.read()
            else:
                tasks = [asyncio.create_task(self.pipe(reader, upstream, account)),
                         asyncio.create_task(self.pipe(upstream_reader, writer, account))]
                await asyncio.gather(*tasks)
        except (QuotaExceeded, OSError, ValueError, UnicodeError, asyncio.IncompleteReadError, asyncio.TimeoutError):
            pass
        finally:
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            if association:
                await association.close()
            if account:
                account.writers.discard(writer)
            if upstream:
                upstream.close()
            writer.close()
            try:
                await writer.wait_closed()
            except OSError:
                pass


async def serve(database):
    budget = Budget(database)
    while not await budget.reload():
        await asyncio.sleep(.5)
    task = asyncio.create_task(budget.maintain())
    gateway = Gateway(budget)
    stopped = asyncio.Event()
    for name in (signal.SIGINT, signal.SIGTERM):
        asyncio.get_running_loop().add_signal_handler(name, stopped.set)
    try:
        async with await asyncio.start_server(gateway.handle, '127.0.0.1', PORT):
            await stopped.wait()
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)
        for account in budget.accounts.values():
            account.close()
        await budget.flush()


def main():
    logging.basicConfig(level=logging.INFO)
    config = json.loads(Path(os.environ.get('TREEROOT_CONFIG', '/etc/treeroot/config.json')).read_text())
    path = Path(config.get('data_dir', '/var/lib/treeroot')) / 'panel.sqlite3'

    @contextmanager
    def database():
        db = sqlite3.connect(path, timeout=1)
        db.row_factory = sqlite3.Row
        try:
            with db:
                yield db
        finally:
            db.close()

    asyncio.run(serve(database))


if __name__ == '__main__':
    main()
