"""Exercise browser request construction without requiring a local browser install."""
from pathlib import Path
import shutil
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = r'''
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const elements = new Map(), requests = [];
const element = selector => {
  if (!elements.has(selector)) elements.set(selector, {
    value: '', innerHTML: '', textContent: '', clientWidth: 390,
    classList: {add() {}, remove() {}, toggle() {}},
    addEventListener() {}, setAttribute() {}
  });
  return elements.get(selector);
};
const state = {page: 'egress-dashboard', pagination: {'egress-records': {page: 1, size: 25}}};
const context = vm.createContext({
  $: element, state, URLSearchParams, AbortController,
  AbortSignal: {any: signals => signals[0], timeout: () => new AbortController().signal},
  ResizeObserver: class {observe() {}},
  document: {addEventListener() {}, querySelectorAll: () => []},
  echarts: {getInstanceByDom: () => undefined},
  renderTraffic() {}, pagination() {}, bytes: String, date: String, esc: String,
  page: name => {state.page = name;},
  api: async path => {
    requests.push(path);
    const hours = Number(new URLSearchParams(path.split('?')[1]).get('hours'));
    assert.ok([24, 168].includes(hours), 'overview must send a supported integer');
    return {
      hours, since: 123, until: 456, device_options: [], devices: [], domains: [], series: [],
      total_upload: 0, total_download: 0, day_upload: 0, day_download: 0,
      enabled: 0, available: 0, day_connections: 0, day_succeeded: 0, online: 0
    };
  }
});
vm.runInContext(fs.readFileSync(process.argv[1], 'utf8'), context);
(async () => {
  for (const [restored, expected] of [['', '24'], ['undefined', '24'], ['NaN', '24'],
      ['最近 7 天', '24'], ['24', '24'], ['168', '168']]) {
    element('#egress-overview-hours').value = restored;
    await vm.runInContext('refreshEgressOverview()', context);
    assert.equal(element('#egress-overview-hours').value, expected);
    assert.equal(new URLSearchParams(requests.at(-1).split('?')[1]).get('hours'), expected);
    assert.ok(!element('#egress-overview-status').textContent.includes('失败'));
  }
  vm.runInContext('egressOverviewData = undefined; openEgressOverviewRecords("")', context);
  assert.equal(vm.runInContext('"start" in egressRecordFilters', context), false,
    'opening records before overview loads must omit an unknown optional integer');
  assert.equal(state.page, 'egress-records');
  vm.runInContext('egressOverviewData = {since: 123}; openEgressOverviewRecords("")', context);
  assert.equal(vm.runInContext('egressRecordFilters.start', context), 123);
})().catch(error => {console.error(error); process.exitCode = 1;});
'''


class EgressFrontendTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('node'), 'Node.js is required for browser request regression tests')
    def test_restored_period_and_optional_record_start(self):
        result = subprocess.run(['node', '-e', SCRIPT, str(ROOT / 'egress.js')],
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
