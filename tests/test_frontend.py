"""Verify proxy trend filters, chart labels and user timestamps in the browser script."""
from pathlib import Path
import os
import shutil
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = r'''
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const elements = new Map();
const element = selector => {
  if (!elements.has(selector)) elements.set(selector, {
    value: '', innerHTML: '', textContent: '', clientWidth: 390, listeners: new Map(),
    classList: {add() {}, remove() {}, toggle() {}},
    addEventListener(name, handler) {this.listeners.set(name, handler);}, setAttribute() {},
    showModal() {this.open=true;}, close() {this.open=false;}
  });
  return elements.get(selector);
};
let option;
const chart = {setOption(value) {option = value;}, resize() {}};
const context = vm.createContext({
  URLSearchParams, AbortSignal, Intl, Date, setInterval() {},
  document: {querySelector: element, querySelectorAll: () => [], addEventListener() {}},
  ResizeObserver: class {observe() {}}, matchMedia: () => ({matches: true}),
  echarts: {getInstanceByDom: () => chart}
});
vm.runInContext(fs.readFileSync(process.argv[1], 'utf8'), context);
const run = code => vm.runInContext(code, context);
const change = (key, value) => element('#traffic-' + key).listeners.get('change')({target: {value}});
run("state.page='proxy-dashboard'; state.users=[{id:1,name:'Fixture',created:1793505600,last_used:1793509200,upload:0,download:0,domains:0,connections:0,enabled:1},{id:2,name:'Unused',created:1793505600,last_used:null,upload:0,download:0,domains:0,connections:0,enabled:1}]");
run('renderUsers()');
assert.ok(element('#usage-cards').innerHTML.includes('上次使用：2026/'));
assert.ok(element('#usage-cards').innerHTML.includes('上次使用：从未使用'));
run("state.page='users';renderUsers()");
assert.ok(element('#user-cards').innerHTML.includes('创建：2026/'));
assert.ok(element('#user-cards').innerHTML.includes('上次使用：2026/'));
change('year', '2026');
change('month', '11');
change('day', '1');
run("state.filter='2'");
let params = new URLSearchParams(run('proxyOverviewQuery()'));
assert.equal(params.get('year'), '2026');
assert.equal(params.get('month'), '11');
assert.equal(params.get('day'), '1');
assert.equal(params.get('user_id'), '2');
assert.equal(params.get('time_zone'), 'Asia/Shanghai');
change('month', '2');
assert.equal(run('state.trend.day'), '');
assert.ok(!element('#traffic-day').innerHTML.includes('value="29"'));
change('year', '');
assert.equal(run('state.trend.month'), '');
assert.equal(run('state.trend.day'), '');
assert.equal(element('#traffic-month').disabled, true);
assert.equal(element('#traffic-day').disabled, true);
run("state.page='proxy-dashboard';renderTraffic([{ts:1793462400,upload:1,download:2}])");
assert.ok(option.xAxis.axisLabel.formatter('1793462400').includes('11/01'));
assert.ok(option.xAxis.axisLabel.formatter('1793462400').includes('\n00:00'));
assert.ok(option.xAxis.axisPointer.label.formatter({value:'1793462400'}).includes('2026/11/01'));
run("state.trend={year:'2026',month:'11',day:'1'};renderTraffic([{ts:1793462400,upload:1,download:2}],'traffic-chart',24,'hour')");
assert.equal(option.xAxis.axisLabel.formatter('1793462400'), '00:00');
run("renderTraffic([{ts:1793462400,upload:1,download:2}],'traffic-chart',24,'month')");
assert.equal(option.xAxis.axisLabel.formatter('1793462400'), '11月');
run("renderTraffic([{ts:1793462400,upload:1,download:2}],'traffic-chart',24,'day')");
assert.equal(option.xAxis.axisLabel.formatter('1793462400'), '11/01');
(async () => {
  run(`let saved=[]; api=async(path,options)=>{saved.push({path,options});return {
    total_bytes:1610612736,daily_bytes:1073741824,speed_bps:500000,
    total_used:200,daily_used:100,blocked:null,ready:true};};`);
  await run("openUserLimits({id:7,name:'Fixture'}, {})");
  assert.equal(element('#limit-total_bytes').value, 1.5);
  assert.equal(element('#limit-speed_bps').value, .5);
  assert.equal(element('#limits-dialog').open, true);
  element('#limits-clear').listeners.get('click')();
  assert.equal(element('#limit-daily_bytes').value, '');
  element('#limit-total_bytes').value = '2.5';
  element('#limit-daily_bytes').value = '1';
  element('#limit-speed_bps').value = '.8';
  run('refresh=async()=>{};toast=()=>{};');
  element('#limits-form').listeners.get('submit')({preventDefault(){},submitter:{}});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(run('saved.at(-1).path'), '/users/7/limits');
  assert.equal(run('saved.at(-1).options.body'), JSON.stringify({total_bytes:2684354560,daily_bytes:1073741824,speed_bps:800000}));
  vm.runInContext(fs.readFileSync(process.argv[1], 'utf8').match(/async function refresh\(\)\{[\s\S]*?\n\}/)[0], context);
  run(`state.csrf='fixture';state.refreshing=false;state.pending=false;
    let resolveFirst, requestCount=0, rendered=[];
    loadUsers=async()=>{}; renderUsers=()=>{}; renderOverview=data=>rendered.push(data.marker);
    api=async()=>++requestCount===1?await new Promise(resolve=>resolveFirst=resolve):{marker:'latest'};
    let first=refresh();`);
  await Promise.resolve();
  run("state.trend.day='2';refresh();resolveFirst({marker:'stale'})");
  await run('first');
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(run('rendered.join(",")'), 'latest', 'stale filter response must not overwrite the current chart');
})().catch(error => {console.error(error); process.exitCode=1;});
'''


class FrontendTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('node'), 'Node.js is required for browser script regression tests')
    def test_user_last_used_and_calendar_trend(self):
        result = subprocess.run(['node', '-e', SCRIPT, str(ROOT / 'app.js')],
                                capture_output=True, text=True, timeout=15,
                                env=os.environ | {'TZ': 'Asia/Shanghai'})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
