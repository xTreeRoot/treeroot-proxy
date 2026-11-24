"""Root-side operations. Uploaded by manage.py; never served over HTTP."""
import argparse
from contextlib import closing
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import secrets
import select
import socket
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.request

APP = Path('/opt/treeroot')
DATA = Path('/var/lib/treeroot')
ARCHIVES = Path('/var/backups/treeroot')
FILES = ('app.py', 'app.js', 'index.html', 'style.css', 'echarts.min.js', 'backup.py',
         'subscription-template.json', 'requirements.txt', 'requirements.lock',
         'egress.py', 'egress.js', 'egress.css', 'connector.py', 'connector_version.py',
         'egress_proxy.py', 'limits.py')
# 保留新增文件前生成的源码归档，旧包仍须通过逐文件清单和哈希校验。
LEGACY_OPTIONAL_SOURCE_FILES = {'connector_version.py', 'limits.py'}
GO_BINARY = 'treeroot-go'
GO_ASSETS = ('index.html', 'app.js', 'style.css', 'egress.js', 'egress.css',
             'echarts.min.js', 'subscription-template.json')
CONNECTOR = ('connector.py', 'connector_version.py', 'egress_proxy.py')
GO_PACKAGE = (GO_BINARY, *GO_ASSETS, *CONNECTOR)
UNITS_DIR = Path('/etc/systemd/system')
UNITS = ('xray.service', 'caddy.service', 'treeroot-panel.service',
         'treeroot-backup.service', 'treeroot-backup.timer', 'treeroot-limits.service')


def run(*args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs)


def copy_file(source, destination):
    source, destination = Path(source), Path(destination)
    if source.exists():
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)


def go_active():
    unit = UNITS_DIR / 'treeroot-panel.service'
    return unit.exists() and f'ExecStart={APP / GO_BINARY}' in unit.read_text()


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def safe_extract(archive, destination, allow=None):
    with tarfile.open(archive) as stream:
        for member in stream.getmembers():
            path = Path(member.name)
            if path.is_absolute() or '..' in path.parts or member.issym() or member.islnk() or member.isdev():
                raise ValueError('Unsafe archive member')
            if allow is not None and (member.name not in allow or not member.isfile()):
                raise ValueError('Unexpected application file: ' + member.name)
        stream.extractall(destination, filter='data')


def backup(public_ip, stop_services=False):
    if os.geteuid() != 0:
        raise RuntimeError('Root SSH account required')
    if not stop_services:
        return create_backup(public_ip, False)
    services = ('caddy', 'treeroot-panel', 'xray')
    if Path('/etc/systemd/system/treeroot-limits.service').exists():
        services += ('treeroot-limits',)
    active = [name for name in services if subprocess.run(
        ['systemctl', 'is-active', '--quiet', name]).returncode == 0]
    try:
        # Close public connections before the last statistics poll.
        run('systemctl', 'stop', 'caddy', 'treeroot-panel')
        if go_active():
            run('runuser', '-u', 'treeroot', '--', str(APP / GO_BINARY), '-final-poll', cwd=APP)
        else:
            run('runuser', '-u', 'treeroot', '--', str(APP / 'venv/bin/python'), '-c',
                'import app; app.collect_stats(); app.collect_access()', cwd=APP)
        run('systemctl', 'stop', 'xray')
        if 'treeroot-limits' in active:
            run('systemctl', 'stop', 'treeroot-limits')
        return create_backup(public_ip, True)
    except BaseException:
        if active:
            run('systemctl', 'start', *reversed(active))
        raise


def create_backup(public_ip, stop_services):
    ARCHIVES.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(ARCHIVES, 0o700)
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    target = ARCHIVES / ('treeroot-' + stamp + '.tar.gz')
    with tempfile.TemporaryDirectory(dir=ARCHIVES) as temporary:
        root = Path(temporary)
        (root / 'data').mkdir()
        # Prevent a user reset from committing between the DB snapshot and config copies.
        with closing(sqlite3.connect(DATA / 'panel.sqlite3', timeout=30)) as lock, lock:
            lock.execute('BEGIN IMMEDIATE')
            with closing(sqlite3.connect(DATA / 'panel.sqlite3')) as source, closing(sqlite3.connect(root / 'data/panel.sqlite3')) as destination:
                source.backup(destination)
                if destination.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
                    raise RuntimeError('SQLite backup validation failed')
            copy_file('/etc/treeroot/config.json', root / 'config/panel.json')
            copy_file('/usr/local/etc/xray/config.json', root / 'config/xray.json')
            copy_file('/etc/caddy/Caddyfile', root / 'config/Caddyfile')
            for unit in UNITS:
                copy_file('/etc/systemd/system/' + unit, root / 'services' / unit)
            copy_file('/etc/logrotate.d/treeroot', root / 'logrotate/treeroot')
            copy_file('/etc/ssh/sshd_config.d/40-treeroot-egress.conf', root / 'egress/sshd.conf')
            copy_file('/etc/systemd/system/treeroot-panel.service.d/egress.conf', root / 'egress/panel-service.conf')
            copy_file('/usr/local/libexec/treeroot-egress-authorize', root / 'egress/authorize.py')
            copy_file('/var/lib/treeroot-egress/registry.json', root / 'egress/registry.json')
        # Kernel-specific BBRx is archived for audit/rebuild, never blindly restored
        # onto a different kernel by bootstrap.py.
        for source, name in [('/etc/systemd/system/treeroot-bbrx.service', 'bbrx.service'),
                             ('/root/treeroot-ops/bbrx.py', 'bbrx.py'),
                             ('/root/treeroot-ops/bbrx.json', 'bbrx.json'),
                             ('/root/treeroot-ops/bbrx/installed.json', 'installed.json')]:
            copy_file(source, root / 'network' / name)
        for name in (*FILES, GO_BINARY):
            copy_file(APP / name, root / 'app' / name)
        if not (root / 'app/requirements.lock').exists():
            freeze = run(str(APP / 'venv/bin/pip'), 'freeze').stdout.splitlines()
            excluded = ('playwright==', 'greenlet==', 'pyee==')
            (root / 'app/requirements.lock').write_text('\n'.join(v for v in freeze if not v.lower().startswith(excluded)) + '\n')
        shutil.copytree('/var/lib/caddy', root / 'caddy', ignore=shutil.ignore_patterns('locks', '*.lock'))
        configuration = json.loads((root / 'config/panel.json').read_text())
        manifest = {
            'format': 1, 'created_at_utc': stamp, 'public_ip': public_ip,
            'domain': configuration['base_url'].split('://', 1)[1].rstrip('/'),
            'stopped_for_migration': stop_services,
            'xray': run('/usr/local/bin/xray', 'version').stdout.splitlines()[0],
            'caddy': run('/usr/local/bin/caddy', 'version').stdout.strip(),
            'sha256': {str(p.relative_to(root)): digest(p) for p in root.rglob('*') if p.is_file()},
        }
        (root / 'manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2))
        with tarfile.open(target, 'w:gz') as stream:
            for path in sorted(root.iterdir()):
                stream.add(path, arcname=path.name)
    os.chmod(target, 0o600)
    return {'archive': str(target), 'sha256': digest(target), 'stopped': stop_services}


def health(address='127.0.0.1:19991', assets=None, limits=True):
    origin = 'http://' + address
    response = urllib.request.urlopen(origin + '/api/me', timeout=3)
    if response.status != 200:
        raise RuntimeError('Panel health check failed')
    resources = [('style.css', 'text/css'), ('app.js', 'javascript')]
    html = ((assets or APP) / 'index.html').read_text()
    for name, mime in [('echarts.min.js', 'javascript'), ('egress.js', 'javascript'), ('egress.css', 'text/css')]:
        if name in html:
            resources.append((name, mime))
    for name, content_type in resources:
        response = urllib.request.urlopen(origin + '/assets/' + name, timeout=3)
        if response.status != 200 or content_type not in response.headers.get('Content-Type', ''):
            raise RuntimeError('Static resource check failed: ' + name)
    if limits and (UNITS_DIR / 'treeroot-limits.service').exists():
        with socket.create_connection(('127.0.0.1', 10002), timeout=2):
            pass


def update(archive, public_ip):
    if go_active():
        raise RuntimeError('The Go service is active; use go-update')
    with tempfile.TemporaryDirectory(prefix='treeroot-release-') as temporary:
        staging = Path(temporary)
        safe_extract(archive, staging, allow=FILES)
        if {p.name for p in staging.iterdir()} != set(FILES):
            raise RuntimeError('Incomplete application package')
        for name in (name for name in FILES if name.endswith('.py')):
            compile((staging / name).read_text(), name, 'exec')
        requirements = (staging / 'requirements.lock').read_text().splitlines()
        code = 'import importlib.metadata,json;print(json.dumps({v:importlib.metadata.version(v) for v in ' + repr([line.split('==')[0] for line in requirements if '==' in line]) + '}))'
        versions = json.loads(run(str(APP / 'venv/bin/python'), '-c', code).stdout)
        for line in requirements:
            if '==' in line:
                package, expected = line.split('==', 1)
                if versions[package] != expected:
                    raise RuntimeError('Dependency change needs a separately verified environment: ' + package)
        snapshot = backup(public_ip)
        rollback = staging / 'rollback'
        rollback.mkdir()
        for name in FILES:
            copy_file(APP / name, rollback / name)
        restart_limits = (Path('/etc/systemd/system/treeroot-limits.service').exists() and
                          (not (APP / 'limits.py').exists() or digest(APP / 'limits.py') != digest(staging / 'limits.py')))
        try:
            run('systemctl', 'stop', 'treeroot-panel')
            for name in FILES:
                shutil.copyfile(staging / name, APP / name)
                os.chown(APP / name, 0, 0)
                os.chmod(APP / name, 0o644)
            run('systemctl', 'start', 'treeroot-panel')
            if restart_limits:
                run('systemctl', 'restart', 'treeroot-limits')
            for attempt in range(20):
                try:
                    health()
                    break
                except Exception:
                    if attempt == 19:
                        raise
                    time.sleep(.5)
        except BaseException:
            run('systemctl', 'stop', 'treeroot-panel')
            for name in FILES:
                if (rollback / name).exists():
                    shutil.copyfile(rollback / name, APP / name)
                elif (APP / name).exists():
                    (APP / name).unlink()
            run('systemctl', 'start', 'treeroot-panel')
            if restart_limits:
                run('systemctl', 'restart', 'treeroot-limits')
            raise
        return {'updated': True, 'backup': snapshot, 'xray_and_caddy_restarted': False,
                'limiter_restarted': restart_limits,
                'sha256': {name: digest(APP / name) for name in FILES}}


def status():
    health()
    services = {}
    for service in ('xray', 'caddy', 'treeroot-panel', 'treeroot-backup.timer'):
        services[service] = run('systemctl', 'is-active', service).stdout.strip()
    if go_active():
        services['treeroot-limits'] = 'integrated'
    elif (UNITS_DIR / 'treeroot-limits.service').exists():
        services['treeroot-limits'] = run('systemctl', 'is-active', 'treeroot-limits').stdout.strip()
    with closing(sqlite3.connect(DATA / 'panel.sqlite3')) as db:
        integrity = db.execute('PRAGMA quick_check').fetchone()[0]
        users, enabled = db.execute('SELECT COUNT(*),SUM(enabled) FROM users').fetchone()
    return {'services': services, 'sqlite': integrity, 'users': users, 'enabled': enabled, 'static_resources': 'ok'}


def check_candidate(staging):
    configuration = json.loads(Path('/etc/treeroot/config.json').read_text())
    isolated = staging / 'test-data'
    isolated.mkdir(mode=0o700)
    path = isolated / 'panel.sqlite3'
    with closing(sqlite3.connect(DATA / 'panel.sqlite3')) as live, closing(sqlite3.connect(path)) as copy:
        live.backup(copy)
    token, csrf = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    identity_sql = ("SELECT id,username,password_hash FROM admin ORDER BY id",
                    "SELECT token_hash,csrf,expires FROM sessions ORDER BY token_hash",
                    "SELECT id,name,client_uuid,email,token,enabled,created,upload,download FROM users ORDER BY id",
                    "SELECT id,name,port,enabled,deleted,created,browser,access_code,proxy_password,pair_hash,"
                    "pair_expires,public_key,agent_hash,last_seen,last_ip,last_test,driver_version FROM egress_devices ORDER BY id",
                    "SELECT name,run,value FROM counters ORDER BY name", "SELECT key,value FROM state ORDER BY key",
                    "SELECT user_id,total_bytes FROM limit_usage ORDER BY user_id",
                    "SELECT user_id,day,bytes FROM limit_daily ORDER BY user_id,day")
    with closing(sqlite3.connect(path)) as copy, copy:
        copy.execute('INSERT INTO sessions VALUES(?,?,?)',
                     (hashlib.sha256(token.encode()).hexdigest(), csrf, int(time.time()) + 60))
        before = [copy.execute(sql).fetchall() for sql in identity_sql]
    configuration.update(data_dir=str(isolated), egress_dir=str(isolated / 'egress'))
    config_file = isolated / 'config.json'
    config_file.write_text(json.dumps(configuration))
    run('chown', '-R', 'treeroot:treeroot', str(isolated))
    config_file.chmod(0o600)
    with subprocess.Popen(['runuser', '-u', 'treeroot', '--', 'env', 'TREEROOT_CONFIG=' + str(config_file),
                           str(staging / GO_BINARY), '-root', str(staging), '-listen', '127.0.0.1:0',
                           '-limits=', '-collect=false'], stdout=subprocess.PIPE, text=True) as process:
        try:
            if not select.select([process.stdout], [], [], 15)[0]:
                raise RuntimeError('Go panel preflight startup timed out')
            address = process.stdout.readline().strip()
            if not address.startswith('127.0.0.1:'):
                raise RuntimeError('Go panel preflight startup failed')
            health(address, staging, limits=False)
            for route in ('me', 'users', 'server', 'resources', 'overview', 'activity', 'egress',
                          'egress/overview', 'egress/records?search=example', 'egress/blacklist'):
                request = urllib.request.Request(f'http://{address}/api/{route}',
                                                 headers={'Cookie': '__Host-treeroot=' + token})
                with urllib.request.urlopen(request, timeout=3) as response:
                    json.load(response)
            run('runuser', '-u', 'treeroot', '--', 'env', 'TREEROOT_CONFIG=' + str(config_file),
                str(staging / GO_BINARY), '-backup')
            with closing(sqlite3.connect(path)) as checked:
                if (checked.execute('PRAGMA quick_check').fetchone() != ('ok',) or
                        [checked.execute(sql).fetchall() for sql in identity_sql] != before):
                    raise RuntimeError('Go preflight database or identity validation failed')
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)


def go_update(archive, public_ip):
    ARCHIVES.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (ARCHIVES / '.go-release.lock').open('a') as lock, tempfile.TemporaryDirectory(prefix='treeroot-go-') as temporary:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        staging = Path(temporary)
        safe_extract(archive, staging, allow=GO_PACKAGE)
        if {path.name for path in staging.iterdir()} != set(GO_PACKAGE):
            raise RuntimeError('Incomplete Go package')
        for name in CONNECTOR:
            if digest(staging / name) != digest(APP / name):
                raise RuntimeError('Connector bytes differ from production: ' + name)
        staging.chmod(0o755)
        (staging / GO_BINARY).chmod(0o755)
        check_candidate(staging)
        snapshot = backup(public_ip)
        saved = ARCHIVES / ('rollback-' + Path(snapshot['archive']).name.removesuffix('.tar.gz'))
        saved.mkdir(mode=0o700)
        panel_unit = UNITS_DIR / 'treeroot-panel.service'
        backup_unit = UNITS_DIR / 'treeroot-backup.service'
        for path in (panel_unit, backup_unit, *(APP / name for name in (GO_BINARY, *GO_ASSETS))):
            copy_file(path, saved / path.name)
        previous_go = go_active()
        limiter = 'treeroot-limits.service'
        web = 'treeroot-panel.service'
        old_limit_enabled = subprocess.run(['systemctl', 'is-enabled', '--quiet', limiter]).returncode == 0
        if old_limit_enabled:
            (saved / 'enable-legacy-limiter').touch()
        try:
            run('systemctl', 'stop', web, limiter)
            for name in (GO_BINARY, *GO_ASSETS):
                run('install', '-o', 'root', '-g', 'root', '-m', '0755' if name == GO_BINARY else '0644',
                    str(staging / name), str(APP / name))
            panel_unit.write_text(re.sub(r'^ExecStart=.*$', 'ExecStart=' + str(APP / GO_BINARY),
                                        (saved / panel_unit.name).read_text(), flags=re.M))
            backup_unit.write_text(re.sub(r'^ExecStart=.*$', 'ExecStart=' + str(APP / GO_BINARY) + ' -backup',
                                         (saved / backup_unit.name).read_text(), flags=re.M))
            run('systemd-analyze', 'verify', str(panel_unit), str(backup_unit))
            if old_limit_enabled:
                run('systemctl', 'disable', limiter)
            run('systemctl', 'daemon-reload')
            run('systemctl', 'start', web)
            for attempt in range(20):
                try:
                    health()
                    break
                except OSError:
                    if attempt == 19:
                        raise
                    time.sleep(0.25)
            result = status()
            if result['sqlite'] != 'ok':
                raise RuntimeError('Production SQLite quick_check failed')
        except BaseException:
            run('systemctl', 'stop', web)
            for path in (panel_unit, backup_unit, *(APP / name for name in (GO_BINARY, *GO_ASSETS))):
                if (saved / path.name).exists():
                    shutil.copy2(saved / path.name, path)
                elif path.name == GO_BINARY:
                    path.unlink(missing_ok=True)
            run('systemctl', 'daemon-reload')
            run('systemctl', 'start', web)
            if old_limit_enabled:
                run('systemctl', 'enable', '--now', limiter)
            elif not previous_go and (UNITS_DIR / limiter).exists():
                run('systemctl', 'start', limiter)
            raise
        return {'backup': snapshot, 'rollback_files': str(saved), 'runtime': 'go',
                'connector_files_unchanged': True, **result}


def source_backups(directory):
    """Verify the two code backups before any obsolete deployment package is removed."""
    directory = Path(directory)
    items = json.loads((directory / 'manifest.json').read_text())
    if len(items) != 2 or len({item['version'] for item in items}) != 2:
        raise ValueError('Two distinct source backup versions are required')
    keep = {'manifest.json'}
    for item in items:
        if not re.fullmatch(r'v\d{4}\.\d{2}\.\d{2}\.\d+', item['version']):
            raise ValueError('Invalid source version')
        name = 'treeroot-' + item['version'] + '.tar.gz'
        if item['archive'] != name:
            raise ValueError('Unexpected source archive name')
        path = directory / name
        if path.is_symlink() or digest(path) != item['sha256']:
            raise ValueError('Source backup checksum mismatch')
        if (directory / (name + '.sha256')).read_text().split() != [item['sha256'], name]:
            raise ValueError('Source checksum file mismatch')
        with tarfile.open(path) as archive:
            names = []
            for member in archive.getmembers():
                parts = Path(member.name).parts
                if (not member.isfile() or not parts or '..' in parts or Path(member.name).is_absolute()
                        or parts[0] in ('private', '.git', '.venv', 'work', 'releases')):
                    raise ValueError('Unsafe source backup member')
                names.append(member.name)
            manifest = json.load(archive.extractfile('release-manifest.json'))
            if (manifest['version'] != item['version'] or manifest['commit'] != item['commit']
                    or set(manifest['files']) != set(names) - {'release-manifest.json'}
                    or len(names) != len(set(names))
                    or not (set(FILES) - LEGACY_OPTIONAL_SOURCE_FILES).issubset(names)):
                raise ValueError('Source backup manifest mismatch')
            for name_in_archive, expected in manifest['files'].items():
                if hashlib.sha256(archive.extractfile(name_in_archive).read()).hexdigest() != expected:
                    raise ValueError('Source backup file mismatch')
        keep.update((name, name + '.sha256'))
    return keep


def cleanup(apply=False):
    health()
    directory = Path('/root/treeroot-ops')
    versions = directory / 'releases'
    keep = source_backups(versions)
    candidates = [p for p in versions.iterdir() if p.name not in keep and p.is_file() and not p.is_symlink()
                  and re.fullmatch(r'treeroot-v\d{4}\.\d{2}\.\d{2}\.\d+\.tar\.gz(?:\.sha256)?', p.name)]
    candidates += [p for p in directory.iterdir() if p.is_file() and not p.is_symlink() and
                   re.fullmatch(r'(?:release-\d{8}T\d{6}Z\.tar\.gz|layout-[a-f0-9]{7,40}\.(?:tar\.gz|py))', p.name)]
    cache = Path('/root/.cache/pip')
    before = shutil.disk_usage('/').free
    result = {'applied': apply, 'obsolete_code_packages': [str(p) for p in candidates],
              'code_bytes': sum(p.stat().st_size for p in candidates),
              'keep_source_files': sorted(keep), 'operational_backups_preserved': True,
              'cache_cleanup': ['/var/cache/apt/archives', str(cache)]}
    if apply:
        for path in candidates:
            path.unlink()
        run('apt-get', 'clean')
        if cache.is_dir() and not cache.is_symlink():
            shutil.rmtree(cache)
        health()
        result['freed_bytes'] = shutil.disk_usage('/').free - before
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['status', 'backup', 'update', 'go-update', 'cleanup'])
    parser.add_argument('--apply', action='store_true', help='Apply the inspected cleanup plan')
    parser.add_argument('--public-ip', default='186.244.200.45')
    parser.add_argument('--archive')
    parser.add_argument('--stop-services', action='store_true')
    args = parser.parse_args()
    if args.command == 'status':
        result = status()
    elif args.command == 'cleanup':
        result = cleanup(args.apply)
    elif args.command == 'backup':
        result = backup(args.public_ip, args.stop_services)
    elif args.command == 'go-update':
        result = go_update(args.archive, args.public_ip)
    else:
        result = update(args.archive, args.public_ip)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
