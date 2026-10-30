#!/usr/bin/env python3
"""Local SSH helper. Secrets stay in private/, never in release archives."""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops'))
from remote import FILES


def load_configuration():
    return json.loads((ROOT / 'deploy/server.json').read_text())


def ssh_options(config, interactive=False):
    return ['-i', str(ROOT / 'private/ssh/id_ed25519'),
            '-o', 'IdentitiesOnly=yes',
            '-o', 'UserKnownHostsFile=' + str(ROOT / 'private/ssh/known_hosts'),
            '-o', 'StrictHostKeyChecking=' + ('ask' if interactive else 'yes'),
            '-o', 'BatchMode=' + ('no' if interactive else 'yes'), '-o', 'ConnectTimeout=15']


def ssh(config, command, interactive=False):
    return ['ssh', *ssh_options(config, interactive), '-p', str(config['ssh_port']),
            config['ssh_user'] + '@' + config['host'], command]


def scp(config, source, destination):
    return ['scp', '-q', *ssh_options(config), '-P', str(config['ssh_port']), source, destination]


def package(destination):
    with tarfile.open(destination, 'w:gz') as archive:
        for name in FILES:
            path = ROOT / name
            if not path.is_file() or path.is_symlink():
                raise RuntimeError('Missing or unsafe application file: ' + name)
            archive.add(path, arcname=name, recursive=False)


def upload_helper(config):
    remote = '/root/treeroot-ops'
    subprocess.run(ssh(config, 'install -d -m 700 ' + remote), check=True)
    target = config['ssh_user'] + '@' + config['host'] + ':' + remote + '/remote.py'
    subprocess.run(scp(config, str(ROOT / 'ops/remote.py'), target), check=True)
    return remote


def pull_backup(config, metadata):
    folder = ROOT / 'private/backups'
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    destination = folder / Path(metadata['archive']).name
    remote = config['ssh_user'] + '@' + config['host'] + ':' + metadata['archive']
    subprocess.run(scp(config, remote, str(destination)), check=True)
    destination.chmod(0o600)
    actual = hashlib.sha256(destination.read_bytes()).hexdigest()
    if actual != metadata['sha256']:
        raise RuntimeError('Backup checksum mismatch')
    checksum = destination.with_suffix(destination.suffix + '.sha256')
    checksum.write_text(actual + '  ' + destination.name + '\n')
    checksum.chmod(0o600)
    return str(destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', help='Override target without modifying production config')
    parser.add_argument('--dry-run', action='store_true')
    sub = parser.add_subparsers(dest='command', required=True)
    sub.add_parser('status')
    sub.add_parser('update')
    p = sub.add_parser('backup')
    p.add_argument('--stop-services', action='store_true', help='Migration maintenance: stop old HTTPS/proxy/panel and leave them stopped')
    sub.add_parser('authorize', help='Verify a new SSH host interactively and add this project public key')
    p = sub.add_parser('bootstrap', help='Restore full backup to a new Ubuntu 24.04 server; does not change DNS')
    p.add_argument('--backup', required=True)
    p.add_argument('--replace', action='store_true', help='Explicitly replace target data; target is backed up first')
    p = sub.add_parser('dns', help='Update only the existing proxy A record')
    p.add_argument('--ip', required=True)
    args = parser.parse_args()
    config = load_configuration()
    if args.host:
        config['host'] = args.host
    if args.dry_run:
        print(json.dumps({'command': args.command, 'target': config['host'],
                          'release_files': list(FILES), 'changes_dns': args.command == 'dns',
                          'stops_proxy': bool(getattr(args, 'stop_services', False)),
                          'preserves_database_on_update': True}, ensure_ascii=False, indent=2))
        return
    if args.command == 'dns':
        subprocess.run(['aliyun', 'alidns', 'UpdateDomainRecord', '--profile', config['aliyun_profile'],
                        '--RecordId', config['aliyun_record_id'], '--RR', 'proxy', '--Type', 'A',
                        '--Value', args.ip, '--Line', 'default', '--TTL', str(config['dns_ttl'])], check=True)
        print('DNS updated. deploy/server.json is unchanged; update its host after verification.')
        return
    if args.command == 'authorize':
        public_key = (ROOT / 'private/ssh/id_ed25519.pub').read_bytes()
        command = "set -eu; umask 077; mkdir -p /root/.ssh; touch /root/.ssh/authorized_keys; IFS= read -r treeroot_key; grep -Fqx -- \"$treeroot_key\" /root/.ssh/authorized_keys || printf '%s\\n' \"$treeroot_key\" >> /root/.ssh/authorized_keys; install -d -m 755 /etc/ssh/sshd_config.d; printf '%s\\n' 'PubkeyAuthentication yes' > /etc/ssh/sshd_config.d/00-treeroot-maintenance.conf; sshd -t; systemctl reload ssh"
        subprocess.run(ssh(config, command, interactive=True), input=public_key, check=True)
        subprocess.run(ssh(config, 'true'), check=True)
        print('Project maintenance key installed; SSH key authentication enabled. Password login unchanged.')
        return
    directory = upload_helper(config)
    if args.command == 'bootstrap':
        archive = Path(args.backup).expanduser().resolve()
        if not archive.is_file():
            raise RuntimeError('Backup archive missing')
        remote_archive = directory + '/restore.tar.gz'
        for source, target in [(archive, remote_archive), (ROOT / 'ops/bootstrap.py', directory + '/bootstrap.py'),
                               (ROOT / 'deploy/server.json', directory + '/versions.json')]:
            subprocess.run(scp(config, str(source), config['ssh_user'] + '@' + config['host'] + ':' + target), check=True)
        command = ['python3', directory + '/bootstrap.py', '--backup', remote_archive,
                   '--public-ip', config['host'], '--versions', directory + '/versions.json']
        if args.replace:
            command.append('--replace')
        subprocess.run(ssh(config, shlex.join(command)), check=True)
        return
    command = ['python3', directory + '/remote.py', args.command, '--public-ip', config['host']]
    if args.command == 'update':
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / 'release.tar.gz'
            package(archive)
            remote_archive = directory + '/release-' + datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '.tar.gz'
            subprocess.run(scp(config, str(archive), config['ssh_user'] + '@' + config['host'] + ':' + remote_archive), check=True)
        command += ['--archive', remote_archive]
    if getattr(args, 'stop_services', False):
        command.append('--stop-services')
    result = subprocess.run(ssh(config, shlex.join(command)), check=True, capture_output=True, text=True)
    metadata = json.loads(result.stdout)
    if args.command == 'backup':
        metadata['local_backup'] = pull_backup(config, metadata)
    elif args.command == 'update':
        metadata['local_backup'] = pull_backup(config, metadata['backup'])
    print(json.dumps(metadata, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
