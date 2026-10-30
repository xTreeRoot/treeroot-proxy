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


class OperationsTests(unittest.TestCase):
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
