"""Restore a TreeRoot full backup on a clean Ubuntu 24.04 x86_64 VPS."""
import argparse
from contextlib import closing
import grp
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import pwd
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile

from remote import APP, DATA, FILES, UNITS, backup, digest, safe_extract


def execute(*command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def ensure_user(name, home=None):
    try:
        return pwd.getpwnam(name)
    except KeyError:
        command = ['useradd', '--system', '--user-group', '--shell', '/usr/sbin/nologin']
        command += ['--create-home', '--home-dir', home] if home else ['--no-create-home']
        execute(*command, name)
        return pwd.getpwnam(name)


def directory(path, owner, group, mode):
    Path(path).mkdir(parents=True, exist_ok=True)
    os.chown(path, owner, group)
    os.chmod(path, mode)


def download(url, destination, expected=None):
    with urllib.request.urlopen(url, timeout=60) as response, open(destination, 'wb') as out:
        shutil.copyfileobj(response, out)
    if expected and digest(destination) != expected:
        raise RuntimeError('Download checksum mismatch')


def prepare_database(path):
    """Keep identity/history; discard only machine-specific baselines and sessions."""
    with closing(sqlite3.connect(path)) as db, db:
        if db.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise RuntimeError('Invalid SQLite backup')
        db.execute('DELETE FROM counters')
        db.execute('DELETE FROM sessions')
        db.execute('DELETE FROM login_attempts')
        db.execute("DELETE FROM state WHERE key='log_cursor'")
        return db.execute('SELECT COUNT(*) FROM users').fetchone()[0]


def install(args):
    if os.geteuid() != 0:
        raise RuntimeError('Root is required')
    os_release = Path('/etc/os-release').read_text()
    if 'ID=ubuntu' not in os_release or 'VERSION_ID="24.04"' not in os_release:
        raise RuntimeError('This installer is verified for Ubuntu 24.04 only')
    if os.uname().machine != 'x86_64' or ipaddress.ip_address(args.public_ip).version != 4:
        raise RuntimeError('x86_64 and a public IPv4 are required')
    versions = json.loads(Path(args.versions).read_text())
    exists = (DATA / 'panel.sqlite3').exists()
    if exists and not args.replace:
        raise RuntimeError('Target already has data. --replace is required; no data was changed.')
    if not exists and any(Path(p).exists() for p in ['/etc/caddy/Caddyfile', '/usr/local/etc/xray/config.json']):
        raise RuntimeError('Target has another proxy/web configuration; use a clean VPS')
    with tempfile.TemporaryDirectory(prefix='treeroot-restore-') as temporary:
        root = Path(temporary)
        safe_extract(args.backup, root)
        manifest = json.loads((root / 'manifest.json').read_text())
        if manifest.get('format') != 1 or manifest['domain'] != versions['domain']:
            raise RuntimeError('Backup format/domain mismatch')
        for name, expected in manifest['sha256'].items():
            path = Path(name)
            if path.is_absolute() or '..' in path.parts or digest(root / path) != expected:
                raise RuntimeError('Backup file checksum mismatch')
        if exists:
            prior = backup(args.public_ip)
            print('Target rollback backup:', prior['archive'], flush=True)
        execute('apt-get', 'update')
        execute('apt-get', 'install', '-y', 'python3-venv', 'ca-certificates', 'logrotate')
        for service in ['treeroot-backup.timer', 'treeroot-backup.service', 'treeroot-panel', 'xray', 'caddy']:
            subprocess.run(['systemctl', 'stop', service], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        xray = ensure_user('xray')
        treeroot = ensure_user('treeroot')
        caddy = ensure_user('caddy', '/var/lib/caddy')
        execute('usermod', '-aG', 'xray', 'treeroot')
        APP.mkdir(parents=True, exist_ok=True)
        archive = root / 'xray.zip'
        download(versions['xray_url'], archive, versions['xray_sha256'])
        with zipfile.ZipFile(archive) as stream:
            Path('/usr/local/bin/xray').write_bytes(stream.read('xray'))
        Path('/usr/local/bin/xray').chmod(0o755)
        archive = root / 'caddy.tar.gz'
        download(versions['caddy_url'], archive, versions['caddy_sha256'])
        with tarfile.open(archive) as stream:
            Path('/usr/local/bin/caddy').write_bytes(stream.extractfile('caddy').read())
        Path('/usr/local/bin/caddy').chmod(0o755)
        for name in FILES:
            # Backups predating the chart bundle still restore their original UI.
            if name == 'echarts.min.js' and not (root / 'app' / name).exists() and 'echarts.min.js' not in (root / 'app/index.html').read_text():
                continue
            shutil.copyfile(root / 'app' / name, APP / name)
            os.chown(APP / name, 0, 0)
            os.chmod(APP / name, 0o644)
        if not (APP / 'venv/bin/pip').exists():
            execute('python3', '-m', 'venv', str(APP / 'venv'))
        execute(str(APP / 'venv/bin/pip'), 'install', '--disable-pip-version-check', '-r', str(APP / 'requirements.lock'))
        directory(DATA, treeroot.pw_uid, treeroot.pw_gid, 0o700)
        for name in ['panel.sqlite3-wal', 'panel.sqlite3-shm']:
            (DATA / name).unlink(missing_ok=True)
        destination = DATA / 'panel.sqlite3'
        shutil.copyfile(root / 'data/panel.sqlite3', destination)
        count = prepare_database(destination)
        os.chown(destination, treeroot.pw_uid, treeroot.pw_gid)
        os.chmod(destination, 0o600)
        directory('/etc/treeroot', 0, treeroot.pw_gid, 0o750)
        panel = Path('/etc/treeroot/config.json')
        shutil.copyfile(root / 'config/panel.json', panel)
        os.chown(panel, 0, treeroot.pw_gid)
        os.chmod(panel, 0o640)
        directory('/usr/local/etc/xray', treeroot.pw_uid, xray.pw_gid, 0o2750)
        configuration = json.loads((root / 'config/xray.json').read_text())
        inbound = next(i for i in configuration['inbounds'] if i.get('tag') == 'websocket-in')
        with closing(sqlite3.connect(destination)) as db:
            db.row_factory = sqlite3.Row
            inbound['settings']['clients'] = [
                {'id': u['client_uuid'], 'email': u['email'], 'alterId': 0, 'level': 0}
                for u in db.execute('SELECT * FROM users WHERE enabled=1')]
        xray_path = Path('/usr/local/etc/xray/config.json')
        xray_path.write_text(json.dumps(configuration, indent=2))
        os.chown(xray_path, treeroot.pw_uid, xray.pw_gid)
        os.chmod(xray_path, 0o640)
        directory('/var/log/treeroot', xray.pw_uid, treeroot.pw_gid, 0o2750)
        log = Path('/var/log/treeroot/access.log')
        log.write_bytes(b'')
        os.chown(log, xray.pw_uid, treeroot.pw_gid)
        os.chmod(log, 0o640)
        if Path('/var/lib/caddy').exists():
            shutil.rmtree('/var/lib/caddy')
        shutil.copytree(root / 'caddy', '/var/lib/caddy')
        for path in [Path('/var/lib/caddy'), *Path('/var/lib/caddy').rglob('*')]:
            os.chown(path, caddy.pw_uid, caddy.pw_gid)
        Path('/etc/caddy').mkdir(exist_ok=True)
        caddyfile = (root / 'config/Caddyfile').read_text().replace(manifest['public_ip'], args.public_ip)
        Path('/etc/caddy/Caddyfile').write_text(caddyfile)
        for name in UNITS:
            shutil.copyfile(root / 'services' / name, Path('/etc/systemd/system') / name)
        copy = root / 'logrotate/treeroot'
        if copy.exists():
            Path('/etc/logrotate.d').mkdir(exist_ok=True)
            shutil.copyfile(copy, '/etc/logrotate.d/treeroot')
        execute('/usr/local/bin/xray', 'run', '-test', '-config', str(xray_path))
        execute('/usr/local/bin/caddy', 'validate', '--config', '/etc/caddy/Caddyfile')
        execute('systemctl', 'daemon-reload')
        execute('systemctl', 'enable', '--now', 'xray', 'caddy', 'treeroot-panel', 'treeroot-backup.timer')
        print(json.dumps({'restored': True, 'users': count, 'domain': manifest['domain'],
                          'new_ip': args.public_ip, 'dns_changed': False}, ensure_ascii=False))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--backup', required=True)
    parser.add_argument('--public-ip', required=True)
    parser.add_argument('--versions', required=True)
    parser.add_argument('--replace', action='store_true')
    install(parser.parse_args())
