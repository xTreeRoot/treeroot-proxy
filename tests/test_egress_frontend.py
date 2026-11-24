"""Preserve the period, anchor, timezone and limit API contracts."""
from pathlib import Path
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReactFrontendTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('npm'), 'npm is required for React tests; run npm ci in frontend/')
    def test_react_period_anchor_timezone_and_limit_contracts(self):
        result = subprocess.run(['npm', 'test', '--', 'src/test/contracts.test.ts', '--reporter=dot'],
                                cwd=ROOT / 'frontend', capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
