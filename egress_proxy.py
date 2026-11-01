"""仅监听本机、验证凭据并拒绝私网目标的 HTTP CONNECT 出口，不解密平台 TLS。"""
import asyncio
import base64
import hmac
import ipaddress
import re
import socket
import time
import uuid
from urllib.parse import urlsplit


# Chromium 后台同步和自动填充请求当前不属于 SocialCrawlerHub 的平台链路；
# 在电脑出口无法访问这些精确主机时直接失败，避免每次等待完整的目标建连超时。
FAST_FAIL_HOSTS = frozenset({
    'accounts.google.com',
    'www.google.com',
    'content-autofill.googleapis.com',
})


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
    def __init__(self, username, password, on_record=None, blacklist=None):
        self.authorization = 'Basic ' + base64.b64encode((username + ':' + password).encode()).decode()
        self.slots = asyncio.Semaphore(64)
        self.on_record = on_record
        self.run_id = uuid.uuid4().hex
        self.traffic = {'upload': 0, 'download': 0}
        self.blacklist = set(FAST_FAIL_HOSTS if blacklist is None else ())
        if blacklist is not None:
            self.set_blacklist(blacklist)

    def set_blacklist(self, hosts):
        """Replace the exact host rules received from the server after a heartbeat."""
        valid = set()
        for value in hosts:
            host = str(value).strip().lower()
            if len(host) <= 253 and re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+', host):
                valid.add(host)
        self.blacklist = valid

    def traffic_snapshot(self):
        return {'run_id': self.run_id, 'sampled_at': time.monotonic(), **self.traffic}

    async def pipe(self, reader, writer, record, direction):
        try:
            while data := await asyncio.wait_for(reader.read(65536), 300):
                writer.write(data)
                await writer.drain()
                record[direction] += len(data)
                self.traffic[direction] += len(data)
        except asyncio.TimeoutError:
            record.update(outcome='failed', detail='timeout')
        except OSError:
            record.update(outcome='failed', detail='connection_failed')
        finally:
            writer.close()

    async def handle(self, reader, writer):
        upstream = None
        established = False
        authorized = False
        started = time.monotonic()
        record = {'event_key': uuid.uuid4().hex, 'ts': int(time.time()), 'domain': '', 'port': None,
                  'method': '', 'outcome': 'failed', 'upload': 0, 'download': 0, 'detail': 'invalid_request'}
        try:
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
            authorized = True
            if not method.isascii() or not method.isalpha() or not method.isupper() or len(method) > 16:
                raise ValueError('Invalid method')
            record['method'] = method
            destination = urlsplit('//' + target if method == 'CONNECT' else target)
            if destination.username or destination.password or not destination.hostname:
                raise ValueError('Invalid destination')
            port = destination.port or (443 if method == 'CONNECT' else 80)
            if method != 'CONNECT' and destination.scheme != 'http':
                raise ValueError('Unsupported protocol')
            host = destination.hostname.encode('idna').decode('ascii').lower().rstrip('.')
            if len(host) > 253 or any(char not in 'abcdefghijklmnopqrstuvwxyz0123456789.:%_-' for char in host):
                raise ValueError('Invalid hostname')
            record.update(domain=host, port=port, detail='target_rejected')
            if host in self.blacklist:
                record['detail'] = 'target_blacklisted'
                raise ValueError('Target host is blacklisted')
            # 只限制目标建连并发；已建立的 TLS 隧道不再长期占用建连槽，
            # 避免某个不可达目标的 15 秒失败把后续抖音请求排队到超时。
            async with self.slots:
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
                payload = ('\r\n'.join(forwarded) + '\r\n\r\n').encode('iso-8859-1')
                upstream.write(payload)
                await upstream.drain()
                record['upload'] += len(payload)
                self.traffic['upload'] += len(payload)
            established = True
            record.update(outcome='success', detail='')
            await asyncio.gather(self.pipe(reader, upstream, record, 'upload'), self.pipe(upstream_reader, writer, record, 'download'))
        except asyncio.CancelledError:
            record.update(outcome='failed', detail='interrupted')
            raise
        except (OSError, ValueError, asyncio.TimeoutError, asyncio.IncompleteReadError, asyncio.LimitOverrunError) as error:
            if isinstance(error, asyncio.TimeoutError):
                record['detail'] = 'timeout'
            elif isinstance(error, OSError):
                record['detail'] = 'connection_failed'
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
            if authorized and self.on_record:
                record['ts'] = int(time.time())
                record['duration_ms'] = max(0, int((time.monotonic() - started) * 1000))
                # 记录落盘会打开 SQLite 并等待文件锁；不能在代理事件循环中同步执行，
                # 否则一条连接结束时会阻塞其他请求解析 CONNECT，黑名单请求也会被拖到数秒后才失败。
                await asyncio.to_thread(self.on_record, record)

    async def listen(self):
        return await asyncio.start_server(self.handle, '127.0.0.1', 0, limit=65536)
