"""验证本机代理拒绝未认证和私网连接，并能透明转发 TLS 隧道字节。"""
import asyncio
import socket
import unittest
from unittest.mock import AsyncMock, patch
from egress_proxy import ForwardProxy, public_connection


class ProxyTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.records = []
        self.recorded = asyncio.Event()
        def record(item):
            self.records.append(item)
            self.recorded.set()
        self.proxy = ForwardProxy('fixture', 'secret', on_record=record)
        self.server = await self.proxy.listen()
        self.port = self.server.sockets[0].getsockname()[1]

    async def asyncTearDown(self):
        self.server.close()
        await self.server.wait_closed()

    async def test_no_authentication_never_connects(self):
        with patch('egress_proxy.public_connection', new_callable=AsyncMock) as connect:
            reader, writer = await asyncio.open_connection('127.0.0.1', self.port)
            writer.write(b'CONNECT public.test:443 HTTP/1.1\r\n\r\n')
            await writer.drain()
            self.assertIn(b'407', await reader.read())
            writer.close()
            await writer.wait_closed()
            connect.assert_not_called()
            self.assertEqual(self.records, [])

    async def test_private_dns_answers_and_other_ports_are_rejected(self):
        for host in ('127.0.0.1', '::1', '192.168.1.1', '169.254.169.254'):
            with self.assertRaises(ValueError):
                await public_connection(host, 443)
        with self.assertRaises(ValueError):
            await public_connection('example.org', 22)

    async def test_authenticated_connect_transfers_bytes_without_tls_termination(self):
        async def echo(reader, writer):
            writer.write(await reader.read(128))
            await writer.drain()
            writer.close()
        echo_server = await asyncio.start_server(echo, '127.0.0.1', 0)
        port = echo_server.sockets[0].getsockname()[1]
        async def connect(host, destination_port):
            self.assertEqual((host, destination_port), ('public.test', 443))
            return await asyncio.open_connection('127.0.0.1', port)
        try:
            with patch('egress_proxy.public_connection', side_effect=connect):
                reader, writer = await asyncio.open_connection('127.0.0.1', self.port)
                writer.write(('CONNECT public.test:443 HTTP/1.1\r\nProxy-Authorization: ' + self.proxy.authorization + '\r\n\r\n').encode())
                await writer.drain()
                self.assertIn(b'200', await reader.readuntil(b'\r\n\r\n'))
                writer.write(b'opaque-tls-bytes')
                await writer.drain()
                self.assertEqual(await reader.read(128), b'opaque-tls-bytes')
                writer.close()
                await writer.wait_closed()
                await asyncio.wait_for(self.recorded.wait(), 2)
                record = self.records[0]
                self.assertEqual((record['domain'], record['port'], record['method']), ('public.test', 443, 'CONNECT'))
                self.assertEqual((record['upload'], record['download']), (16, 16))
                self.assertEqual(record['outcome'], 'success')
                self.assertNotIn('opaque-tls-bytes', str(record))
                self.assertNotIn(self.proxy.authorization, str(record))
        finally:
            echo_server.close()
            await echo_server.wait_closed()

    async def test_rejected_target_is_recorded_with_no_traffic(self):
        reader, writer = await asyncio.open_connection('127.0.0.1', self.port)
        writer.write(('CONNECT 127.0.0.1:443 HTTP/1.1\r\nProxy-Authorization: ' + self.proxy.authorization + '\r\n\r\n').encode())
        await writer.drain()
        self.assertIn(b'502', await reader.read())
        writer.close()
        await writer.wait_closed()
        await asyncio.wait_for(self.recorded.wait(), 2)
        self.assertEqual((self.records[0]['outcome'], self.records[0]['detail']), ('failed', 'target_rejected'))
        self.assertEqual((self.records[0]['upload'], self.records[0]['download']), (0, 0))
