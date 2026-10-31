import importlib.util
from contextlib import closing
import io
from pathlib import Path
import sqlite3
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch, Mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops'))
import bootstrap
import manage
import remote
import releases


class OperationsTests(unittest.TestCase):
    def test_cleanup_validates_two_backups_before_deleting_only_project_packages(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            ops = folder / 'ops'
            versions = ops / 'releases'
            versions.mkdir(parents=True)
            cache = folder / 'pip'
            cache.mkdir()
            (cache / 'cached-wheel').write_bytes(b'cached')
            obsolete = [ops / 'release-20261030T123456Z.tar.gz', ops / 'layout-abcdef0.py',
                        versions / 'treeroot-v2026.10.30.1.tar.gz']
            preserved = [ops / 'socialcrawler-release.tar.gz', ops / 'remote.py',
                         ops / 'treeroot-20261031T123456.123Z.tar.gz', versions / 'manifest.json']
            for path in obsolete + preserved:
                path.write_bytes(b'preserve unless explicitly obsolete')
            def location(value):
                return {'/root/treeroot-ops': ops, '/root/.cache/pip': cache}.get(str(value), Path(value))
            with patch.object(remote, 'Path', side_effect=location), patch.object(remote, 'health'), \
                 patch.object(remote, 'run') as run, patch.object(remote, 'source_backups') as verify:
                verify.side_effect = ValueError('missing verified backups')
                with self.assertRaises(ValueError):
                    remote.cleanup(True)
                self.assertTrue(all(path.exists() for path in obsolete))
                run.assert_not_called()
                verify.side_effect = None
                verify.return_value = {'manifest.json', 'new.tar.gz', 'new.tar.gz.sha256'}
                plan = remote.cleanup()
                self.assertEqual(set(plan['obsolete_code_packages']), set(map(str, obsolete)))
                self.assertTrue(cache.exists())
                remote.cleanup(True)
                self.assertTrue(all(not path.exists() for path in obsolete))
                self.assertTrue(all(path.exists() for path in preserved))
                self.assertFalse(cache.exists())
                run.assert_called_once_with('apt-get', 'clean')

    def test_source_archives_keep_two_versions_without_private_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            with patch.object(releases, 'FOLDER', folder):
                for version in (1, 2, 3):
                    items = releases.pack('v2026.10.31.' + str(version), 'HEAD')
            self.assertEqual([item['version'] for item in items], ['v2026.10.31.3', 'v2026.10.31.2'])
            self.assertEqual(len(list(folder.glob('*.tar.gz'))), 2)
            self.assertFalse((folder / 'treeroot-v2026.10.31.1.tar.gz').exists())
            kept = remote.source_backups(folder)
            self.assertEqual(len(kept), 5)
            for item in items:
                manifest = releases.verify_archive(folder / item['archive'], item['sha256'])
                self.assertIn('AGENTS.md', manifest['files'])
                self.assertIn('ops/manage.py', manifest['files'])
                self.assertFalse(any(name.startswith(('private/', '.git/', '.venv/', 'releases/')) for name in manifest['files']))
            (folder / items[0]['archive']).write_bytes(b'corrupt')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                remote.source_backups(folder)

    def test_source_backup_allowlist_rejects_secrets_and_recursive_archives(self):
        for name in ['private/config.json', '.git/config', 'releases/old.tar.gz', 'panel.sqlite3',
                     '../app.py', '/etc/treeroot/config.json', 'ops/__pycache__/x.pyc']:
            self.assertFalse(releases.source_allowed(name))
        self.assertTrue(releases.source_allowed('app.py'))
        self.assertTrue(releases.source_allowed('docs/DEPLOYMENT.md'))

    def test_health_checks_chart_bundle_only_when_page_uses_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for html, expected in [('<script src="/assets/app.js"></script>', 3),
                                   ('<script src="/assets/echarts.min.js"></script>', 4)]:
                (root / 'index.html').write_text(html)
                def response(url, **kwargs):
                    return Mock(status=200, headers={'Content-Type': 'text/css' if url.endswith('.css') else 'application/javascript'})
                with patch.object(remote, 'APP', root), patch.object(remote.urllib.request, 'urlopen', side_effect=response) as request:
                    remote.health()
                self.assertEqual(request.call_count, expected)

    def test_failed_maintenance_backup_restarts_previously_active_services(self):
        with patch.object(remote.subprocess, 'run', return_value=Mock(returncode=0)), \
             patch.object(remote, 'run') as run, \
             patch.object(remote, 'create_backup', side_effect=RuntimeError('disk full')), \
             patch.object(remote.os, 'geteuid', return_value=0):
            with self.assertRaisesRegex(RuntimeError, 'disk full'):
                remote.backup('192.0.2.1', stop_services=True)
        run.assert_any_call('systemctl', 'start', 'xray', 'treeroot-panel', 'caddy')

    def test_successful_maintenance_backup_leaves_services_stopped(self):
        with patch.object(remote.subprocess, 'run', return_value=Mock(returncode=0)), \
             patch.object(remote, 'run') as run, \
             patch.object(remote, 'create_backup', return_value={'stopped': True}), \
             patch.object(remote.os, 'geteuid', return_value=0):
            self.assertTrue(remote.backup('192.0.2.1', stop_services=True)['stopped'])
        self.assertFalse(any(call.args[:2] == ('systemctl', 'start') for call in run.call_args_list))

    def test_release_excludes_private_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / 'release.tar.gz'
            manage.package(archive)
            with tarfile.open(archive) as stream:
                names = stream.getnames()
            self.assertEqual(set(names), set(remote.FILES))
            self.assertFalse(any('private' in n or 'sqlite' in n or 'config.json' in n for n in names))

    def test_rejects_path_traversal(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / 'bad.tar.gz'
            with tarfile.open(archive, 'w:gz') as stream:
                member = tarfile.TarInfo('../stolen')
                member.size = 1
                stream.addfile(member, io.BytesIO(b'x'))
            with self.assertRaises(ValueError):
                remote.safe_extract(archive, Path(temporary) / 'out')

    def test_rejects_symbolic_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / 'bad.tar.gz'
            with tarfile.open(archive, 'w:gz') as stream:
                member = tarfile.TarInfo('app.py')
                member.type = tarfile.SYMTYPE
                member.linkname = '/etc/passwd'
                stream.addfile(member)
            with self.assertRaises(ValueError):
                remote.safe_extract(archive, Path(temporary) / 'out')

    def test_release_rejects_database_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / 'bad.tar.gz'
            with tarfile.open(archive, 'w:gz') as stream:
                member = tarfile.TarInfo('panel.sqlite3')
                member.size = 1
                stream.addfile(member, io.BytesIO(b'x'))
            with self.assertRaises(ValueError):
                remote.safe_extract(archive, temporary, allow=remote.FILES)

    def test_migration_preserves_identity_and_statistics(self):
        with tempfile.TemporaryDirectory() as temporary:
            database = Path(temporary) / 'panel.sqlite3'
            with closing(sqlite3.connect(database)) as db, db:
                db.executescript('''CREATE TABLE users(id INTEGER, client_uuid TEXT, token TEXT, upload INTEGER, download INTEGER);
                CREATE TABLE counters(value INTEGER); CREATE TABLE sessions(token TEXT);
                CREATE TABLE login_attempts(ip TEXT); CREATE TABLE state(key TEXT,value TEXT);
                CREATE TABLE domains(domain TEXT,connections INTEGER);
                INSERT INTO users VALUES(1,'keep-uuid','keep-token',123,456);
                INSERT INTO counters VALUES(999); INSERT INTO sessions VALUES('old-session');
                INSERT INTO state VALUES('log_cursor','old-inode'); INSERT INTO state VALUES('other','keep');
                INSERT INTO domains VALUES('google.com',7);''')
            self.assertEqual(bootstrap.prepare_database(database), 1)
            with closing(sqlite3.connect(database)) as db:
                self.assertEqual(db.execute('SELECT * FROM users').fetchone(), (1, 'keep-uuid', 'keep-token', 123, 456))
                self.assertEqual(db.execute('SELECT * FROM domains').fetchone(), ('google.com', 7))
                self.assertEqual(db.execute('SELECT COUNT(*) FROM counters').fetchone()[0], 0)
                self.assertEqual(db.execute('SELECT COUNT(*) FROM sessions').fetchone()[0], 0)
                self.assertEqual(db.execute('SELECT * FROM state').fetchall(), [('other', 'keep')])


if __name__ == '__main__':
    unittest.main()
