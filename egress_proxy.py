"""仅监听本机、验证凭据并拒绝私网目标的 HTTP CONNECT 出口，不解密平台 TLS。"""
import asyncio
import base64
import hmac
import ipaddress
import socket
from urllib.parse import urlsplit


async def public_connection(host, port):
    if port not in (80, 443):
        raise ValueError('Unsupported port')
    addresses = await asyncio.get_running_loop().getaddrinfo(host, port, type=socket.SOCK_STREAM)
    if not addresses or any(not ipaddress.ip_address(item[4][0]).is_global for item in addresses):
        raise ValueError('Private destinations are not allowed')
    # 公网地址先验证，再按实际 IP 并发连接，避免双栈网络中逐个等待不可达地址。
    selected = []
    for family in (socket.AF_INET, socket.AF_INET6):
        selected.extend([item for item in addresses if item[0] == family][:2])
    tasks = [asyncio.create_task(asyncio.open_connection(item[4][0], port, family=item[0])) for item in selected]
    winner = None
    try:
        for attempt in asyncio.as_completed(tasks, timeout=15):
            try:
                winner = await attempt
                return winner
            except OSError:
                continue
        raise OSError('Destination unavailable')
    finally:
        for task in tasks:
            if not task.done():
                task.cancel()
        results = await asyncio.gather(*tasks, return_exceptions=True)
        for result in results:
            if isinstance(result, tuple) and result is not winner:
                result[1].close()



class ForwardProxy:
    def __init__(self, username, password):
        self.authorization = 'Basic ' + base64.b64encode((username + ':' + password).encode()).decode()
        self.slots = asyncio.Semaphore(64)

    async def pipe(self, reader, writer):
        try:
            while data := await asyncio.wait_for(reader.read(65536), 300):
                writer.write(data)
                await writer.drain()
        except (OSError, asyncio.TimeoutError):
            pass
        finally:
            writer.close()

    async def handle(self, reader, writer):
        upstream = None
        established = False
        try:
            async with self.slots:
                head = await asyncio.wait_for(reader.readuntil(b'\r\n\r\n'), 10)
                lines = head.decode('iso-8859-1').split('\r\n')
                method, target, version = lines[0].split(' ')
                headers = {}
                for line in lines[1:]:
                    if line:
                        key, value = line.split(':', 1)
                        headers[key.lower()] = value.strip()
                if not hmac.compare_digest(headers.get('proxy-authorization', '').encode('iso-8859-1'), self.authorization.encode('ascii')):
                    writer.write(b'HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="TreeRoot"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
                    await writer.drain()
                    return
                destination = urlsplit('//' + target if method == 'CONNECT' else target)
                if destination.username or destination.password or not destination.hostname:
                    raise ValueError('Invalid destination')
                port = destination.port or (443 if method == 'CONNECT' else 80)
                if method != 'CONNECT' and destination.scheme != 'http':
                    raise ValueError('Unsupported protocol')
                upstream_reader, upstream = await public_connection(destination.hostname, port)
                if method == 'CONNECT':
                    writer.write(b'HTTP/1.1 200 Connection Established\r\n\r\n')
                    await writer.drain()
                else:
                    path = destination.path or '/'
                    if destination.query:
                        path += '?' + destination.query
                    forwarded = [f'{method} {path} {version}', *[
                        line for line in lines[1:] if line and not line.lower().startswith(('proxy-authorization:', 'proxy-connection:'))]]
                    upstream.write(('\r\n'.join(forwarded) + '\r\n\r\n').encode('iso-8859-1'))
                    await upstream.drain()
                established = True
                await asyncio.gather(self.pipe(reader, upstream), self.pipe(upstream_reader, writer))
        except (OSError, ValueError, asyncio.TimeoutError, asyncio.IncompleteReadError, asyncio.LimitOverrunError):
            if not established:
                writer.write(b'HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
                try:
                    await writer.drain()
                except OSError:
                    pass
        finally:
            if upstream:
                upstream.close()
            writer.close()

    async def listen(self):
        return await asyncio.start_server(self.handle, '127.0.0.1', 0, limit=65536)
