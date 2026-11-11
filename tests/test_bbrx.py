import base64
from contextlib import closing
import io
import json
from pathlib import Path
import sqlite3
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch, Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'ops'))
import bbrx


class BBRxTests(unittest.TestCase):
    def test_full_backup_checks_member_hashes_and_database_integrity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            dbfile = root / 'data.sqlite3'
            with closing(sqlite3.connect(dbfile)) as db:
                db.execute('CREATE TABLE users(id INTEGER PRIMARY KEY)')
            files = {'data/panel.sqlite3': dbfile.read_bytes(), 'config/panel.json': b'{}',
                     'config/xray.json': b'{}'}
            import hashlib
            manifest = {'format': 1, 'sha256': {k: hashlib.sha256(v).hexdigest() for k, v in files.items()}}
            archive = root / 'backup.tar.gz'
            for corrupt in (False, True):
                with tarfile.open(archive, 'w:gz') as stream:
                    for name, data in dict(files, **{'manifest.json': json.dumps(manifest).encode()}).items():
                        if corrupt and name == 'config/xray.json':
                            data = b'changed'
                        info = tarfile.TarInfo(name)
                        info.size = len(data)
                        stream.addfile(info, io.BytesIO(data))
                if corrupt:
                    with self.assertRaisesRegex(RuntimeError, 'checksum'):
                        bbrx.verified_backup(archive)
                else:
                    self.assertEqual(bbrx.verified_backup(archive), bbrx.digest(archive))

    def test_rejects_mismatched_kernel_before_loading_or_changing_sysctl(self):
        with patch.object(bbrx, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'kernel/module'):
                bbrx.load({'kernel': 'different-kernel', 'module': '/unused'})
            run.assert_not_called()

    def test_rejects_a_different_loaded_module_before_switching_algorithm(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            module, version = root / 'module.ko', root / 'srcversion'
            module.write_bytes(b'fixture module')
            version.write_text('OLD-SOURCE')
            record = {'kernel': bbrx.os.uname().release, 'module': str(module),
                      'module_sha256': bbrx.digest(module)}
            def path(value):
                return version if str(value) == '/sys/module/tcp_bbrx/srcversion' else Path(value)
            with patch.object(bbrx, 'Path', side_effect=path), \
                 patch.object(bbrx, 'run', side_effect=['cubic bbrx', 'NEW-SOURCE']) as run:
                with self.assertRaisesRegex(RuntimeError, 'different BBRx module'):
                    bbrx.load(record)
                self.assertEqual(run.call_count, 2)

    def test_failed_confirmation_restores_previous_default_and_persistent_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            unit, installed, snapshot = root / 'bbrx.service', root / 'installed.json', root / 'state.json'
            old = b'original unit\n'
            unit.write_bytes(old)
            state = {'phase': 'pending', 'previous_cc': 'cubic', 'was_enabled': False,
                     'build': {'kernel': 'fixture'}, 'timer': 'fixture-rollback', 'files': {
                         'unit': {'content': base64.b64encode(old).decode(), 'mode': 0o640},
                         'installed': None}}
            bbrx.save(snapshot, state)
            def run(*args):
                if args[:2] == ('systemd-analyze', 'verify'):
                    raise RuntimeError('fixture unit validation failed')
                return ''
            with patch.object(bbrx, 'UNIT', unit), patch.object(bbrx, 'INSTALLED', installed), \
                 patch.object(bbrx, 'load'), patch.object(bbrx, 'run', side_effect=run) as calls, \
                 patch.object(bbrx.subprocess, 'run', return_value=Mock(returncode=0)):
                with self.assertRaisesRegex(RuntimeError, 'validation failed'):
                    bbrx.confirm(snapshot)
            self.assertEqual(unit.read_bytes(), old)
            self.assertEqual(unit.stat().st_mode & 0o777, 0o640)
            self.assertFalse(installed.exists())
            self.assertEqual(json.loads(snapshot.read_text())['phase'], 'rolled_back')
            calls.assert_any_call('sysctl', '-w', bbrx.CC + '=cubic')

    def test_expired_guard_does_not_undo_confirmed_settings(self):
        with tempfile.TemporaryDirectory() as temporary:
            snapshot = Path(temporary) / 'state.json'
            bbrx.save(snapshot, {'phase': 'confirmed'})
            with patch.object(bbrx, 'run') as run:
                self.assertEqual(bbrx.rollback(snapshot, pending_only=True),
                                 {'changed': False, 'phase': 'confirmed'})
                run.assert_not_called()

    def test_confirmation_queues_boot_without_waiting_on_its_own_lock(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            unit, installed, snapshot = root / 'unit', root / 'installed', root / 'state.json'
            bbrx.save(snapshot, {'phase': 'pending', 'build': {}, 'timer': 'fixture'})
            with patch.object(bbrx, 'UNIT', unit), patch.object(bbrx, 'INSTALLED', installed), \
                 patch.object(bbrx, 'load'), patch.object(bbrx, 'run') as run, \
                 patch.object(bbrx, 'status', return_value={}):
                bbrx.confirm(snapshot)
                run.assert_any_call('systemctl', 'start', '--no-block', 'treeroot-bbrx')
                self.assertNotIn(unittest.mock.call('systemctl', 'enable', '--now', 'treeroot-bbrx'),
                                 run.call_args_list)
            self.assertEqual(json.loads(snapshot.read_text())['phase'], 'confirmed')

    def test_missing_module_on_new_kernel_keeps_boot_default(self):
        with tempfile.TemporaryDirectory() as temporary:
            installed = Path(temporary) / 'installed.json'
            bbrx.save(installed, {'kernel': 'older-kernel', 'module': '/missing'})
            with patch.object(bbrx, 'INSTALLED', installed), patch.object(bbrx, 'run') as run:
                self.assertFalse(bbrx.boot()['applied'])
                run.assert_not_called()

    def test_invalid_backup_prevents_trial_and_guard_creation(self):
        with patch.object(bbrx, 'verified_backup', side_effect=RuntimeError('invalid backup')), \
             patch.object(bbrx, 'load') as load, patch.object(bbrx, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'invalid backup'):
                bbrx.trial({}, Path('/missing'))
            load.assert_not_called()
            run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
