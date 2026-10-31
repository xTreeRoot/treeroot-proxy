#!/usr/bin/env python3
"""Keep two complete, non-secret source backups in the repository's releases/ folder."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile

from remote import FILES

ROOT = Path(__file__).resolve().parents[1]
FOLDER = ROOT / 'releases'
VERSION = re.compile(r'v(\d{4})\.(\d{2})\.(\d{2})\.(\d+)')
TOP_LEVEL = set(FILES) | {'AGENTS.md', 'README.md', '.gitignore'}
SOURCE_DIRS = {'ops', 'docs', 'tests', 'deploy'}


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, text=True).strip()


def version_key(tag):
    match = VERSION.fullmatch(tag)
    if not match:
        raise ValueError('Version must look like v2026.10.31.2')
    return tuple(map(int, match.groups()))


def source_allowed(name):
    path = Path(name)
    if path.is_absolute() or '..' in path.parts or not path.parts:
        return False
    if len(path.parts) == 1:
        return name in TOP_LEVEL
    return (path.parts[0] in SOURCE_DIRS and '__pycache__' not in path.parts
            and path.suffix in ('.py', '.md', '.json'))


def verify_archive(path, expected):
    if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
        raise ValueError('Source archive checksum mismatch')
    with tarfile.open(path) as archive:
        names = []
        for member in archive.getmembers():
            if not member.isfile() or not source_allowed(member.name) and member.name != 'release-manifest.json':
                raise ValueError('Unsafe or unexpected source archive member: ' + member.name)
            names.append(member.name)
        if len(names) != len(set(names)) or not set(FILES).issubset(names):
            raise ValueError('Incomplete source archive')
        manifest = json.load(archive.extractfile('release-manifest.json'))
        for name, digest in manifest['files'].items():
            if hashlib.sha256(archive.extractfile(name).read()).hexdigest() != digest:
                raise ValueError('Source file checksum mismatch')
        if set(manifest['files']) != set(names) - {'release-manifest.json'}:
            raise ValueError('Source manifest mismatch')
    return manifest


def load_manifest():
    path = FOLDER / 'manifest.json'
    return json.loads(path.read_text()) if path.exists() else []


def pack(tag, ref):
    version_key(tag)
    commit = git('rev-parse', '--verify', ref + '^{commit}')
    timestamp = int(git('show', '-s', '--format=%ct', commit))
    manifest = load_manifest()
    if any(item['version'] == tag for item in manifest):
        raise ValueError('Version already exists; choose a new version')
    FOLDER.mkdir(parents=True, exist_ok=True)
    target = FOLDER / ('treeroot-' + tag + '.tar.gz')
    names = git('ls-tree', '-r', '--name-only', commit).splitlines()
    checksums = {}
    with tarfile.open(target, 'w:gz') as archive:
        for name in sorted(name for name in names if source_allowed(name)):
            mode = git('ls-tree', commit, '--', name).split()[0]
            if mode not in ('100644', '100755'):
                raise ValueError('Source links are not allowed')
            content = subprocess.check_output(['git', 'show', commit + ':' + name], cwd=ROOT)
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime = len(content), int(mode[-3:], 8), timestamp
            archive.addfile(info, io.BytesIO(content))
            checksums[name] = hashlib.sha256(content).hexdigest()
        content = json.dumps({'version': tag, 'commit': commit, 'files': checksums}, indent=2).encode()
        info = tarfile.TarInfo('release-manifest.json')
        info.size, info.mode, info.mtime = len(content), 0o644, timestamp
        archive.addfile(info, io.BytesIO(content))
    digest = hashlib.sha256(target.read_bytes()).hexdigest()
    verify_archive(target, digest)
    target.with_name(target.name + '.sha256').write_text(digest + '  ' + target.name + '\n')
    manifest.append({'version': tag, 'commit': commit, 'archive': target.name, 'sha256': digest,
                     'created_at': datetime.now(timezone.utc).isoformat()})
    ordered = sorted(manifest, key=lambda item: version_key(item['version']), reverse=True)
    # Verify both retained backups before deleting any older source package.
    for item in ordered[:2]:
        verify_archive(FOLDER / item['archive'], item['sha256'])
    for item in ordered[2:]:
        old = FOLDER / item['archive']
        if old.name != 'treeroot-' + item['version'] + '.tar.gz' or old.parent != FOLDER:
            raise ValueError('Unexpected source archive name')
        old.unlink(missing_ok=True)
        old.with_name(old.name + '.sha256').unlink(missing_ok=True)
    (FOLDER / 'manifest.json').write_text(json.dumps(ordered[:2], ensure_ascii=False, indent=2) + '\n')
    return ordered[:2]


def sync():
    import manage
    items = load_manifest()
    if len(items) != 2:
        raise RuntimeError('Exactly two source versions are required before server sync')
    for item in items:
        verify_archive(FOLDER / item['archive'], item['sha256'])
    config = manage.load_configuration()
    subprocess.run(manage.ssh(config, 'install -d -m 700 /root/treeroot-ops/releases'), check=True)
    for name in [item['archive'] + suffix for item in items for suffix in ('', '.sha256')] + ['manifest.json']:
        subprocess.run(manage.scp(config, str(FOLDER / name), config['ssh_user'] + '@' + config['host'] +
                                 ':/root/treeroot-ops/releases/' + name), check=True)
    subprocess.run(manage.ssh(config, 'chmod 600 /root/treeroot-ops/releases/*'), check=True)
    return {'synced': [item['version'] for item in items]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    command = sub.add_parser('pack')
    command.add_argument('tag')
    command.add_argument('--ref', default='HEAD')
    sub.add_parser('list')
    sub.add_parser('sync')
    args = parser.parse_args()
    result = pack(args.tag, args.ref) if args.command == 'pack' else sync() if args.command == 'sync' else load_manifest()
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
