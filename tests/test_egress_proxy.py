"""验证本机代理拒绝未认证和私网连接，并能透明转发 TLS 隧道字节。"""
import asyncio
import socket
import unittest
from unittest.mock import AsyncMock, patch
from egress_proxy import ForwardProxy, public_connection


class ProxyTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.proxy = ForwardProxy('fixture', 'secret')
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
        finally:
            echo_server.close()
            await echo_server.wait_closed()
