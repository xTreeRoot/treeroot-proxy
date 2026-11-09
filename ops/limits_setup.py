#!/usr/bin/env python3
"""Install the loopback limiter after publishing the application and taking a full backup."""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time

APP = Path('/opt/treeroot')
CONFIG = Path('/usr/local/etc/xray/config.json')
DB = Path('/var/lib/treeroot/panel.sqlite3')
UNIT = Path('/etc/systemd/system/treeroot-limits.service')
SERVICE = '''[Unit]
Description=TreeRoot per-account traffic and bandwidth limits
After=network-online.target treeroot-panel.service
Wants=network-online.target treeroot-panel.service

[Service]
User=treeroot
Group=treeroot
WorkingDirectory=/opt/treeroot
ExecStart=/opt/treeroot/venv/bin/python /opt/treeroot/limits.py
Restart=on-failure
RestartSec=2
TimeoutStopSec=10
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/treeroot
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
'''


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45, cwd=APP)
    if result.returncode:
        raise RuntimeError('Command failed: ' + ' '.join(args[:3]))
    return result.stdout


def validate(config):
    fd, filename = tempfile.mkstemp(dir=CONFIG.parent, suffix='.json')
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(config, stream)
        os.chown(filename, 0, CONFIG.stat().st_gid)
        os.chmod(filename, 0o640)
        run('runuser', '-u', 'xray', '--', '/usr/local/bin/xray', 'run', '-test', '-config', filename)
    finally:
        Path(filename).unlink(missing_ok=True)


def install(backup):
    if os.geteuid() != 0:
        raise RuntimeError('Root is required')
    with tarfile.open(backup) as archive:
        if not {'config/xray.json', 'data/panel.sqlite3', 'manifest.json'}.issubset(archive.getnames()):
            raise RuntimeError('A verified full TreeRoot backup is required')
        manifest = json.load(archive.extractfile('manifest.json'))
        for name, expected in manifest['sha256'].items():
            if hashlib.sha256(archive.extractfile(name).read()).hexdigest() != expected:
                raise RuntimeError('Full backup member checksum mismatch')
    sys.path.insert(0, str(APP))
    import limits
    previous = CONFIG.read_bytes()
    old_unit = UNIT.read_bytes() if UNIT.exists() else None
    old_enabled = subprocess.run(['systemctl', 'is-enabled', '--quiet', 'treeroot-limits']).returncode == 0
    old_active = subprocess.run(['systemctl', 'is-active', '--quiet', 'treeroot-limits']).returncode == 0
    changed = False
    stopped = False
    try:
        run('systemctl', 'stop', 'treeroot-panel')
        stopped = True
        run('runuser', '-u', 'treeroot', '--', str(APP / 'venv/bin/python'), '-c',
            'import app; app.collect_stats(); app.collect_access()')
        with closing(sqlite3.connect(DB)) as db:
            db.row_factory = sqlite3.Row
            users = db.execute('SELECT * FROM users').fetchall()
            limits.activate(db)
            db.commit()
        candidate = limits.xray_config(json.loads(previous), users)
        validate(candidate)
        UNIT.write_text(SERVICE)
        UNIT.chmod(0o644)
        run('systemctl', 'daemon-reload')
        changed = candidate != json.loads(previous)
        if changed:
            fd, filename = tempfile.mkstemp(dir=CONFIG.parent, suffix='.json')
            with os.fdopen(fd, 'w') as stream:
                json.dump(candidate, stream, indent=2)
            shutil.copystat(CONFIG, filename)
            os.chown(filename, CONFIG.stat().st_uid, CONFIG.stat().st_gid)
            os.replace(filename, CONFIG)
            run('systemctl', 'restart', 'xray')
        run('systemctl', 'start', 'treeroot-panel')
        stopped = False
        run('systemctl', 'enable', '--now', 'treeroot-limits')
        for _ in range(100):
            try:
                with socket.create_connection(('127.0.0.1', limits.PORT), timeout=.2):
                    break
            except OSError:
                time.sleep(.2)
        else:
            raise RuntimeError('Limiter did not become ready')
        print(json.dumps({'installed': True, 'listen': '127.0.0.1:' + str(limits.PORT),
                          'users': len(users), 'xray_restarted': changed, 'backup': str(backup)}))
    except BaseException:
        CONFIG.write_bytes(previous)
        if not limits.configured(json.loads(previous)):
            with closing(sqlite3.connect(DB)) as db:
                db.execute("DELETE FROM state WHERE key='limits_active'")
                db.commit()
        if old_unit is not None:
            UNIT.write_bytes(old_unit)
        else:
            subprocess.run(['systemctl', 'disable', '--now', 'treeroot-limits'], capture_output=True)
            UNIT.unlink(missing_ok=True)
        run('systemctl', 'daemon-reload')
        if changed:
            run('systemctl', 'restart', 'xray')
        run('systemctl', 'start', 'treeroot-panel')
        if old_unit is not None:
            run('systemctl', 'enable' if old_enabled else 'disable', 'treeroot-limits')
            run('systemctl', 'restart' if old_active else 'stop', 'treeroot-limits')
        raise
    finally:
        if stopped:
            run('systemctl', 'start', 'treeroot-panel')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--backup', type=Path, required=True)
    install(parser.parse_args().backup)
