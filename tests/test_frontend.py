"""Run React interaction regressions in the standard project suite."""
from pathlib import Path
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReactFrontendTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('npm'), 'npm is required for React tests; run npm ci in frontend/')
    def test_react_authentication_navigation_filters_and_mutations(self):
        result = subprocess.run(['npm', 'test', '--', 'src/test/interactions.test.tsx', '--reporter=dot'],
                                cwd=ROOT / 'frontend', capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_bundled_assets_are_local_and_cache_versioned(self):
        html = (ROOT / 'index.html').read_text()
        self.assertIn('id="root"', html)
        self.assertIn('type="module"', html)
        self.assertRegex(html, r'/assets/app\.js\?v=[a-f0-9]{12}')
        self.assertRegex(html, r'/assets/style\.css\?v=[a-f0-9]{12}')
        self.assertNotIn('/assets/egress.js', html)
        self.assertNotIn('/src/main.tsx', html)
