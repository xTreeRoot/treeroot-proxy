"""部署只允许反向转发的电脑出口 SSH 身份，不改变原有管理员 SSH 登录。"""
import grp
import json
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import time


def execute(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True)


def provision(host):
    if os.geteuid() != 0:
        raise RuntimeError('Root is required')
    directory = Path('/var/lib/treeroot-egress')
    try:
        group = grp.getgrnam('treeroot-egress')
    except KeyError:
        execute('groupadd', '--system', 'treeroot-egress')
        group = grp.getgrnam('treeroot-egress')
    try:
        pwd.getpwnam('treeroot-egress')
    except KeyError:
        execute('useradd', '--system', '--gid', 'treeroot-egress', '--shell', '/usr/sbin/nologin',
                '--no-create-home', '--home-dir', '/nonexistent', 'treeroot-egress')
    owner = pwd.getpwnam('treeroot')
    directory.mkdir(exist_ok=True)
    os.chown(directory, owner.pw_uid, group.gr_gid)
    directory.chmod(0o2750)
    for user in ('treeroot', 'socialcrawler'):
        try:
            pwd.getpwnam(user)
        except KeyError:
            continue
        execute('usermod', '-aG', 'treeroot-egress', user)

    helper = Path('/usr/local/libexec/treeroot-egress-authorize')
    helper.parent.mkdir(parents=True, exist_ok=True)
    helper.write_text('''#!/usr/bin/python3
import json, re, sys
from pathlib import Path
try:
    key=sys.argv[1]
    if not re.fullmatch(r'[A-Za-z0-9+/]+={0,2}',key): raise ValueError()
    data=json.loads(Path('/var/lib/treeroot-egress/registry.json').read_text())
    for row in data['devices']:
        public=row.get('publicKey') or ''
        if row.get('enabled') and public.split() == ['ssh-ed25519',key] and 21000 <= row['port'] < 22000:
            print('restrict,port-forwarding,permitlisten="127.0.0.1:%d" %s' % (row['port'],public))
except (OSError,ValueError,KeyError,IndexError,TypeError):
    pass
''')
    helper.chmod(0o755)
    snippet = Path('/etc/ssh/sshd_config.d/40-treeroot-egress.conf')
    content = '''Match User treeroot-egress
    PubkeyAuthentication yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AuthorizedKeysFile none
    AuthorizedKeysCommand /usr/local/libexec/treeroot-egress-authorize %k
    AuthorizedKeysCommandUser treeroot
    AllowTcpForwarding remote
    GatewayPorts no
    AllowStreamLocalForwarding no
    PermitTunnel no
    PermitTTY no
    X11Forwarding no
    ForceCommand /usr/sbin/nologin
Match all
'''
    backup = Path('/var/backups/treeroot') / ('egress-settings-' + str(time.time_ns()))
    backup.mkdir(mode=0o700, parents=True)
    config = Path('/etc/treeroot/config.json')
    shutil.copy2(config, backup / 'panel.json')
    previous = snippet.read_bytes() if snippet.exists() else None
    if previous:
        (backup / 'sshd.conf').write_bytes(previous)
    snippet.write_text(content)
    try:
        execute('/usr/sbin/sshd', '-t')
    except BaseException:
        if previous is None:
            snippet.unlink()
        else:
            snippet.write_bytes(previous)
        raise
    dropin = Path('/etc/systemd/system/treeroot-panel.service.d/egress.conf')
    dropin.parent.mkdir(parents=True, exist_ok=True)
    if dropin.exists():
        shutil.copy2(dropin, backup / 'panel-egress.conf')
    dropin.write_text('[Service]\nReadWritePaths=/var/lib/treeroot-egress\n')
    execute('systemctl', 'daemon-reload')
    settings = json.loads(config.read_text())
    host_key = Path('/etc/ssh/ssh_host_ed25519_key.pub').read_text().split()
    settings.update(egress_dir=str(directory), egress_ssh_host=host,
                    egress_known_hosts=host + ' ' + ' '.join(host_key[:2]))
    temporary = config.with_suffix('.egress-next')
    temporary.write_text(json.dumps(settings, indent=2))
    temporary.chmod(0o640)
    os.chown(temporary, config.stat().st_uid, config.stat().st_gid)
    os.replace(temporary, config)
    execute('systemctl', 'reload', 'ssh')
    print(json.dumps({'egress_ssh_ready': True, 'registry': str(directory / 'registry.json'),
                      'backup': str(backup), 'public_proxy_ports_opened': False}))


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', required=True)
    provision(parser.parse_args().host)
