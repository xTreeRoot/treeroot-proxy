import asyncio
from contextlib import contextmanager
from datetime import timedelta
import json
from pathlib import Path
import socket
import sqlite3
import struct
import tempfile
import time
import unittest
from unittest.mock import patch

import limits


class LimitTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.path = Path(self.folder.name) / 'panel.sqlite3'
        with self.database() as db:
            db.executescript('''CREATE TABLE users(id INTEGER PRIMARY KEY,email TEXT,client_uuid TEXT,
                enabled INTEGER,upload INTEGER,download INTEGER);
                CREATE TABLE traffic(ts INTEGER,user_id INTEGER,upload INTEGER,download INTEGER);
                CREATE TABLE state(key TEXT PRIMARY KEY,value TEXT);
                INSERT INTO users VALUES(1,'fixture@example.test','fixture-key',1,0,0);''')
            limits.initialize(db)

    @contextmanager
    def database(self):
        db = sqlite3.connect(self.path, timeout=1)
        db.row_factory = sqlite3.Row
        try:
            with db:
                yield db
        finally:
            db.close()

    async def budget(self, total=0, daily=0, speed=0):
        with self.database() as db:
            db.execute('INSERT OR REPLACE INTO user_limits VALUES(1,?,?,?,?)', (total, daily, speed, int(time.time())))
        bank = limits.Budget(self.database)
        bank.started_at = 0
        await bank.reload()
        return bank, bank.accounts[1]

    async def test_initialization_is_additive_and_usage_does_not_reset(self):
        _, start, _ = limits.day_window()
        with self.database() as db:
            db.execute('UPDATE users SET upload=123,download=456 WHERE id=1')
            db.execute('INSERT INTO traffic VALUES(?,1,40,60)', (start,))
            identity = tuple(db.execute('SELECT * FROM users').fetchone())
            limits.initialize(db)
            limits.initialize(db)
            row = limits.snapshot(db)[1][1]
            self.assertEqual((row['total_used'], row['daily_used']), (579, 100))
            self.assertEqual(tuple(db.execute('SELECT * FROM users').fetchone()), identity)
        bank, account = await self.budget(total=1000)
        await bank.take(account, 200)
        restarted, account = await self.budget(total=1000)
        self.assertEqual(account.row['total_used'], 779)

    async def test_concurrent_connections_share_durable_quota(self):
        bank, account = await self.budget(total=65536)
        granted = await asyncio.gather(bank.take(account, 40000), bank.take(account, 40000))
        self.assertEqual(sum(granted), 65536)
        with self.assertRaises(limits.QuotaExceeded):
            await bank.take(account, 1)
        with self.database() as db:
            self.assertEqual(db.execute('SELECT total_bytes FROM limit_usage').fetchone()[0], 65536)
        bank, account = await self.budget(total=65536)
        with self.assertRaises(limits.QuotaExceeded):
            await bank.take(account, 1)
        with self.database() as db:
            db.execute('UPDATE user_limits SET total_bytes=0 WHERE user_id=1')
        await bank.reload()
        self.assertEqual(await bank.take(account, 1), 1)

    async def test_xray_buffered_bytes_are_not_double_counted_after_activation(self):
        with self.database() as db:
            db.execute('UPDATE users SET upload=100 WHERE id=1')
            limits.activate(db)
        bank, account = await self.budget(total=1000)
        await bank.take(account, 100)
        with self.database() as db:
            # Xray may count input while the limiter still holds the buffered payload.
            db.execute('UPDATE users SET upload=500 WHERE id=1')
        await bank.reload()
        self.assertEqual(account.row['total_used'], 200)
        await bank.take(account, 100)
        await bank.flush()
        with self.database() as db:
            limits.activate(db)
            self.assertEqual(limits.snapshot(db)[1][1]['total_used'], 300)

    async def test_daily_quota_uses_beijing_midnight_and_does_not_clear_total(self):
        bank, account = await self.budget(daily=100)
        await bank.take(account, 100)
        with self.assertRaises(limits.QuotaExceeded):
            await bank.take(account, 1)
        day, start, end = limits.day_window()
        next_day = (limits.datetime.fromtimestamp(start, limits.ZONE) + timedelta(days=1)).date().isoformat()
        with patch.object(limits, 'day_window', return_value=(next_day, end, end + 86400)):
            self.assertEqual(await bank.take(account, 80), 80)
            self.assertEqual(account.row['total_used'], 180)
        with self.database() as db:
            self.assertEqual([row[0] for row in db.execute('SELECT bytes FROM limit_daily ORDER BY day')], [100, 80])

    async def test_shared_upload_download_rate_and_hot_reload(self):
        bank, account = await self.budget(speed=128000)
        async def transfer():
            remaining = 8000
            while remaining:
                remaining -= await bank.take(account, remaining)
        started = time.monotonic()
        await asyncio.gather(transfer(), transfer())
        self.assertGreaterEqual(time.monotonic() - started, .85)
        with self.database() as db:
            db.execute('UPDATE user_limits SET speed_bps=0')
        await bank.reload()
        self.assertEqual(await bank.take(account, 100000), 100000)
        bank.loaded_at -= 6
        with self.assertRaises(limits.QuotaExceeded):
            await bank.take(account, 1)

    async def socks(self, port, command, host='127.0.0.1', target_port=1, password=b'fixture-key'):
        reader, writer = await asyncio.open_connection('127.0.0.1', port)
        self.addAsyncCleanup(self.close_writer, writer)
        writer.write(b'\x05\x01\x02')
        await writer.drain()
        self.assertEqual(await reader.readexactly(2), b'\x05\x02')
        name = b'fixture@example.test'
        writer.write(bytes([1, len(name)]) + name + bytes([len(password)]) + password)
        await writer.drain()
        auth = await reader.readexactly(2)
        if auth != b'\x01\x00':
            return reader, writer, auth
        writer.write(bytes([5, command, 0]) + limits.pack_address(host, target_port))
        await writer.drain()
        reply = await reader.readexactly(3)
        address = await limits.read_address(reader)
        bound = struct.unpack('!H', await reader.readexactly(2))[0]
        return reader, writer, (reply, address, bound)

    async def close_writer(self, writer):
        writer.close()
        await writer.wait_closed()

    async def test_socks_tcp_requires_authentication_and_stops_active_stream(self):
        async def echo(reader, writer):
            try:
                while data := await reader.read(1000):
                    writer.write(data)
                    await writer.drain()
            finally:
                writer.close()
        echo_server = await asyncio.start_server(echo, '127.0.0.1', 0)
        self.addAsyncCleanup(echo_server.wait_closed)
        self.addCleanup(echo_server.close)
        echo_port = echo_server.sockets[0].getsockname()[1]
        bank, account = await self.budget(total=10)
        gateway = limits.Gateway(bank)
        server = await asyncio.start_server(gateway.handle, '127.0.0.1', 0)
        self.addAsyncCleanup(server.wait_closed)
        self.addCleanup(server.close)
        port = server.sockets[0].getsockname()[1]
        _, _, auth = await self.socks(port, 1, password=b'bad')
        self.assertEqual(auth, b'\x01\x01')
        original = asyncio.open_connection
        async def connect_fixture(host, port):
            return await original('127.0.0.1', echo_port)
        with patch.object(limits, 'connect', side_effect=connect_fixture):
            reader, writer, reply = await self.socks(port, 1)
            self.assertEqual(reply[0], b'\x05\x00\x00')
            writer.write(b'12345')
            await writer.drain()
            self.assertEqual(await reader.readexactly(5), b'12345')
            writer.write(b'extra')
            await writer.drain()
            self.assertEqual(await asyncio.wait_for(reader.read(), 2), b'')
        self.assertEqual(account.row['total_used'], 10)

    async def test_socks_udp_is_metered_and_fragments_are_rejected(self):
        class Echo(asyncio.DatagramProtocol):
            def connection_made(self, transport):
                self.transport = transport
            def datagram_received(self, data, address):
                self.transport.sendto(data, address)
        loop = asyncio.get_running_loop()
        echo, _ = await loop.create_datagram_endpoint(Echo, local_addr=('127.0.0.1', 0))
        self.addCleanup(echo.close)
        target_port = echo.get_extra_info('sockname')[1]
        bank, account = await self.budget(total=20)
        gateway = limits.Gateway(bank)
        server = await asyncio.start_server(gateway.handle, '127.0.0.1', 0)
        self.addAsyncCleanup(server.wait_closed)
        self.addCleanup(server.close)
        reader, writer, reply = await self.socks(server.sockets[0].getsockname()[1], 3)
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sock.setblocking(False)
        self.addCleanup(sock.close)
        response = b'\x00\x00\x00' + limits.pack_address('127.0.0.1', target_port) + b'1234567890'
        fixture_address = [(socket.AF_INET, socket.SOCK_DGRAM, 17, '', ('127.0.0.1', target_port))]
        with patch.object(limits, 'destination', return_value=fixture_address):
            await loop.sock_sendto(sock, b'\x00\x00\x01' + response[3:], ('127.0.0.1', reply[2]))
            with self.assertRaises(asyncio.TimeoutError):
                await asyncio.wait_for(loop.sock_recvfrom(sock, 1000), .05)
            await loop.sock_sendto(sock, response, ('127.0.0.1', reply[2]))
            data, _ = await asyncio.wait_for(loop.sock_recvfrom(sock, 1000), 2)
            self.assertEqual(data[-10:], b'1234567890')
            await loop.sock_sendto(sock, response, ('127.0.0.1', reply[2]))
            self.assertEqual(await asyncio.wait_for(reader.read(), 2), b'')
        self.assertEqual(account.row['total_used'], 20)

    async def test_private_destination_and_missing_fresh_statistics_fail_closed(self):
        with self.assertRaises(ValueError):
            await limits.destination('127.0.0.1', 443, socket.SOCK_STREAM)
        fresh = limits.Budget(self.database)
        self.assertFalse(await fresh.reload())


class RoutingTests(unittest.TestCase):
    def test_gateway_routing_preserves_api_private_blocks_and_identity(self):
        original = {'api': {'services': ['HandlerService']}, 'outbounds': [
            {'tag': 'direct', 'protocol': 'freedom'}, {'tag': 'block', 'protocol': 'blackhole'}],
            'routing': {'rules': [{'inboundTag': ['api-in'], 'outboundTag': 'api'},
                                  {'ip': ['127.0.0.1/8'], 'outboundTag': 'block'}]}}
        user = {'id': 1, 'email': 'fixture@example.test', 'client_uuid': 'keep-uuid'}
        result = limits.xray_config(original, [user])
        self.assertEqual(result['routing']['rules'][:2], original['routing']['rules'])
        self.assertEqual(result['outbounds'][:2], original['outbounds'])
        self.assertEqual(result['outbounds'][2]['settings']['servers'][0]['port'], 10002)
        self.assertEqual(result['routing']['rules'][-1]['outboundTag'], 'block')
        self.assertEqual(limits.xray_config(result, [user]), result)
        self.assertNotIn('RoutingService', original['api']['services'])
