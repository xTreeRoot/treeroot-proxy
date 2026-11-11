#!/usr/bin/env python3
"""Build pinned BBRx and enable it with a timed, data-preserving rollback."""
import argparse
import base64
from contextlib import closing
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import tempfile
import urllib.request

from remote import digest, safe_extract

ROOT = Path('/root/treeroot-ops/bbrx')
SETTINGS = Path('/root/treeroot-ops/bbrx.json')
INSTALLED = ROOT / 'installed.json'
UNIT = Path('/etc/systemd/system/treeroot-bbrx.service')
BACKUPS = Path('/var/backups/treeroot')
CC = 'net.ipv4.tcp_congestion_control'
SERVICE = '''[Unit]
Description=TreeRoot pinned BBRx congestion control
After=systemd-modules-load.service systemd-sysctl.service
Before=xray.service caddy.service treeroot-panel.service

[Service]
Type=oneshot
ExecStart=/usr/bin/python3 /root/treeroot-ops/bbrx.py boot
RemainAfterExit=yes
UMask=0077

[Install]
WantedBy=multi-user.target
'''


def run(*args, timeout=60, **kwargs):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, **kwargs)
    if result.returncode:
        raise RuntimeError('Command failed: ' + ' '.join(args) + '\n' +
                           (result.stderr + result.stdout)[-4000:])
    return result.stdout.strip()


def atomic(path, content, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(content)
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


def save(path, value):
    atomic(path, (json.dumps(value, indent=2) + '\n').encode())


def status():
    return {'kernel': os.uname().release, 'congestion_control': run('sysctl', '-n', CC),
            'available': run('sysctl', '-n', 'net.ipv4.tcp_available_congestion_control').split(),
            'installed': json.loads(INSTALLED.read_text()) if INSTALLED.exists() else None,
            'boot_enabled': subprocess.run(['systemctl', 'is-enabled', '--quiet', 'treeroot-bbrx'],
                                           capture_output=True).returncode == 0}


def build(settings):
    config = json.loads(settings.read_text())
    commit = config['commit']
    if (config['repository'] != 'https://github.com/nivrrex/bbrx'
            or not re.fullmatch(r'[0-9a-f]{40}', commit)
            or set(config['sha256']) != {'Makefile', 'src/tcp_bbrx.c', 'LICENSE'}
            or not 1 <= config['floor_mbps'] <= 2):
        raise RuntimeError('Invalid pinned BBRx manifest')
    kernel = os.uname().release
    if not (Path('/lib/modules') / kernel / 'build').is_dir():
        raise RuntimeError('Install headers for the running kernel before building BBRx')
    folder = ROOT / kernel / commit
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    for name, expected in config['sha256'].items():
        target = folder / name
        url = 'https://raw.githubusercontent.com/nivrrex/bbrx/' + commit + '/' + name
        with urllib.request.urlopen(url, timeout=30) as response:
            atomic(target, response.read())
        if digest(target) != expected:
            raise RuntimeError('Pinned BBRx source checksum mismatch: ' + name)
    # Do not use the upstream installer or replace the running kernel.
    run('make', '-j2', 'PWD=' + str(folder), cwd=folder, timeout=180)
    module = folder / 'tcp_bbrx.ko'
    if (run('modinfo', '-F', 'name', str(module)) != 'tcp_bbrx'
            or run('modinfo', '-F', 'vermagic', str(module)).split()[0] != kernel):
        raise RuntimeError('Built module does not match the running kernel')
    record = {'kernel': kernel, 'commit': commit, 'module': str(module),
              'module_sha256': digest(module), 'floor_mbps': config['floor_mbps']}
    save(folder / 'build.json', record)
    return record


def verified_backup(archive):
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        safe_extract(archive, root)
        manifest = json.loads((root / 'manifest.json').read_text())
        required = {'config/panel.json', 'config/xray.json', 'data/panel.sqlite3'}
        if manifest.get('format') != 1 or not required.issubset(manifest['sha256']):
            raise RuntimeError('A complete TreeRoot backup is required')
        for name, expected in manifest['sha256'].items():
            path = Path(name)
            if path.is_absolute() or '..' in path.parts or digest(root / path) != expected:
                raise RuntimeError('Full backup member checksum mismatch')
        with closing(sqlite3.connect('file:' + str(root / 'data/panel.sqlite3') + '?mode=ro', uri=True)) as db:
            if db.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
                raise RuntimeError('Invalid SQLite backup')
    return digest(archive)


def load(record):
    module = Path(record['module'])
    if record['kernel'] != os.uname().release or digest(module) != record['module_sha256']:
        raise RuntimeError('BBRx kernel/module checksum mismatch; rebuild for this kernel')
    available = run('sysctl', '-n', 'net.ipv4.tcp_available_congestion_control').split()
    if 'bbrx' not in available:
        run('insmod', str(module), 'bbrx_floor_mbps=' + str(record['floor_mbps']))
    version = Path('/sys/module/tcp_bbrx/srcversion')
    expected = run('modinfo', '-F', 'srcversion', str(module))
    if not expected or version.read_text().strip() != expected:
        raise RuntimeError('A different BBRx module is already loaded; keep the current default')
    parameter = Path('/sys/module/tcp_bbrx/parameters/bbrx_floor_mbps')
    if parameter.read_text().strip() != str(record['floor_mbps']):
        raise RuntimeError('Loaded BBRx parameters differ from the pinned configuration')
    run('sysctl', '-w', CC + '=bbrx')
    if run('sysctl', '-n', CC) != 'bbrx':
        raise RuntimeError('BBRx did not become the default congestion control')


def rollback(snapshot, pending_only=False):
    state = json.loads(snapshot.read_text())
    if state['phase'] == 'rolled_back' or pending_only and state['phase'] != 'pending':
        return {'changed': False, 'phase': state['phase']}
    # Established sockets retain their chosen CC; never force-close user connections.
    run('sysctl', '-w', CC + '=' + state['previous_cc'])
    if UNIT.exists():
        run('systemctl', 'disable', '--now', 'treeroot-bbrx')
    for key, path in [('unit', UNIT), ('installed', INSTALLED)]:
        old = state['files'][key]
        if old is None:
            path.unlink(missing_ok=True)
        else:
            atomic(path, base64.b64decode(old['content']), old['mode'])
    run('systemctl', 'daemon-reload')
    if state['files']['unit'] is not None:
        run('systemctl', 'enable' if state['was_enabled'] else 'disable', 'treeroot-bbrx')
        if state.get('was_active'):
            run('systemctl', 'start', '--no-block', 'treeroot-bbrx')
    subprocess.run(['systemctl', 'stop', state['timer'] + '.timer'], capture_output=True)
    state['phase'] = 'rolled_back'
    save(snapshot, state)
    return {'changed': True, 'congestion_control': state['previous_cc']}


def trial(record, backup):
    checksum = verified_backup(backup)
    BACKUPS.mkdir(mode=0o700, parents=True, exist_ok=True)
    if any(json.loads(p.read_text())['phase'] == 'pending' for p in BACKUPS.glob('bbrx-*/state.json')):
        raise RuntimeError('Confirm or roll back the pending BBRx trial first')
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    folder = BACKUPS / ('bbrx-' + stamp)
    folder.mkdir(mode=0o700)
    snapshot = folder / 'state.json'
    before = status()
    files = {}
    for key, path in [('unit', UNIT), ('installed', INSTALLED)]:
        if path.is_symlink():
            raise RuntimeError('Refusing to replace a symlink: ' + str(path))
        files[key] = {'content': base64.b64encode(path.read_bytes()).decode(),
                      'mode': path.stat().st_mode & 0o777} if path.exists() else None
    state = {'phase': 'pending', 'previous_cc': before['congestion_control'],
             'was_enabled': before['boot_enabled'], 'was_active': subprocess.run(
                 ['systemctl', 'is-active', '--quiet', 'treeroot-bbrx'], capture_output=True).returncode == 0,
             'files': files, 'build': record,
             'backup': str(backup), 'backup_sha256': checksum, 'timer': 'treeroot-bbrx-rollback-' + stamp}
    save(snapshot, state)
    try:
        run('systemd-run', '--unit=' + state['timer'], '--on-active=10m',
            '--timer-property=AccuracySec=1s', '/usr/bin/python3', str(Path(__file__).resolve()),
            'rollback', '--snapshot', str(snapshot), '--pending-only')
        load(record)
    except BaseException:
        rollback(snapshot)
        raise
    return {'snapshot': str(snapshot), 'rollback_after_seconds': 600, 'status': status()}


def confirm(snapshot):
    state = json.loads(snapshot.read_text())
    if state['phase'] != 'pending':
        raise RuntimeError('Only a pending BBRx trial can be confirmed')
    try:
        load(state['build'])
        save(INSTALLED, state['build'])
        atomic(UNIT, SERVICE.encode(), 0o644)
        run('systemd-analyze', 'verify', str(UNIT))
        run('systemctl', 'daemon-reload')
        run('systemctl', 'enable', 'treeroot-bbrx')
        run('systemctl', 'stop', state['timer'] + '.timer')
        state['phase'] = 'confirmed'
        save(snapshot, state)
        # The boot command takes the same lock. Queue it so it runs after this
        # confirmation exits rather than waiting on our own lock.
        run('systemctl', 'start', '--no-block', 'treeroot-bbrx')
    except BaseException:
        rollback(snapshot)
        raise
    return status()


def boot():
    try:
        load(json.loads(INSTALLED.read_text()))
        return {'applied': True, 'congestion_control': 'bbrx'}
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        # An unbuilt kernel keeps its normal default; web/proxy services still start.
        return {'applied': False, 'reason': str(error)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    sub.add_parser('status')
    p = sub.add_parser('build')
    p.add_argument('--settings', type=Path, default=SETTINGS)
    p = sub.add_parser('trial')
    p.add_argument('--build', type=Path, required=True)
    p.add_argument('--backup', type=Path, required=True)
    for name in ('confirm', 'rollback'):
        p = sub.add_parser(name)
        p.add_argument('--snapshot', type=Path, required=True)
        if name == 'rollback':
            p.add_argument('--pending-only', action='store_true')
    sub.add_parser('boot')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('Root is required')
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (ROOT / '.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if args.command == 'status':
            result = status()
        elif args.command == 'build':
            result = build(args.settings)
        elif args.command == 'trial':
            result = trial(json.loads(args.build.read_text()), args.backup)
        elif args.command == 'confirm':
            result = confirm(args.snapshot)
        elif args.command == 'rollback':
            result = rollback(args.snapshot, args.pending_only)
        else:
            result = boot()
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
