import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const script = [1,2,3].map(i=>readFileSync(new URL(`./panel_script_${i}.js`, import.meta.url),'utf8')).join('');

function panel(pathname = '/v0/resource/plugins/codex-health-monitor/panel') {
  const elements = new Map();
  const context = vm.createContext({
    location: {pathname},
    document: {getElementById(id) {
      if (!elements.has(id)) {
        const classes = new Set();
        elements.set(id, {
          textContent: '',
          innerHTML: '',
          disabled: false,
          style: {},
          classList: {
            add: (...names) => names.forEach(name => classes.add(name)),
            remove: (...names) => names.forEach(name => classes.delete(name)),
            contains: name => classes.has(name),
            toggle: (name, force) => {
              if (force === undefined ? !classes.has(name) : force) classes.add(name);
              else classes.delete(name);
            },
          },
        });
      }
      return elements.get(id);
    }},
  });
  vm.runInContext(script.slice(0, script.indexOf("document.querySelectorAll('input[name=\"scheduleMode\"]')")), context);
  return {elements, run: code => vm.runInContext(code, context)};
}

test('panel API follows the loaded plugin ID', () => {
  for (const id of ['codex-health-monitor', 'codex-health-monitor-linux-arm64', 'codex-health-monitor-v0.1.8']) {
    for (const suffix of ['', '/']) {
      const p = panel(`/v0/resource/plugins/${id}/panel${suffix}`);
      assert.equal(p.run('API'), `/v0/management/plugins/${id}`);
    }
  }
});

test('history pagination handles page changes, refresh and shrinking history', () => {
  const p = panel();
  p.run(`renderHistory([{started_at:'2026-09-06T00:00:00Z',trigger:'manual',accounts:Array.from({length:23},(_,i)=>({email:'user-'+i,status:'healthy',healthy:true}))}])`);
  assert.equal((p.elements.get('history').innerHTML.match(/<tr>/g) || []).length, 10);
  p.run('historyPage=3;renderHistoryPage()');
  assert.equal((p.elements.get('history').innerHTML.match(/<tr>/g) || []).length, 3);
  assert.equal(p.elements.get('historyNext').disabled, true);
  p.run('renderHistory([{accounts:historyRows.map(row=>row.account)}])');
  assert.equal(p.run('historyPage'), 3);
  p.run('renderHistory([{accounts:[{email:"remaining",status:"not_checked"}]}])');
  assert.equal(p.run('historyPage'), 1);
  p.run('renderHistory([])');
  assert.equal(p.elements.get('historyPrev').disabled, true);
  assert.equal(p.elements.get('historyNext').disabled, true);
});

test('failed discovery remains visible in history and escapes its message', () => {
  const p = panel();
  p.run(`renderHistory([{trigger:'scheduled',error_code:'account_discovery_failed',error_message:'Failed <script>bad</script>',accounts:null}])`);
  assert.match(p.elements.get('history').innerHTML, /Failed &lt;script&gt;bad&lt;\/script&gt;/);
  assert.match(p.elements.get('history').innerHTML, /响应异常/);
});

test('dirty simulator state enables save and clean state disables it', () => {
  const p = panel();
  p.run('clearDirty()');
  assert.equal(p.elements.get('saveWindow').disabled, true);
  p.run('markDirty()');
  assert.equal(p.elements.get('saveWindow').disabled, false);
  assert.equal(p.elements.get('simDirty').classList.contains('show'), true);
});

test('window labels show actual anchored time ranges', () => {
  const p = panel();
  assert.equal(p.run('windowRangeText(330,630)'), '05:30–10:30');
  assert.equal(p.run('windowRangeText(840,1140)'), '14:00–19:00');
  assert.equal(p.run('windowRangeText(1260,1560)'), '21:00–次日 02:00');
});

test('quota parser prefers shared 5H and 7D auth-file signals', () => {
  const p = panel();
  const windows = p.run(`quotaWindowsForEntry({quota:{observed_at:'2026-09-09T10:00:00Z',signals:{
    'X-Codex-Secondary-Used-Percent':'18',
    'X-Codex-Secondary-Window-Minutes':'300',
    'X-Codex-Secondary-Reset-After-Seconds':'3600',
    'X-Codex-Primary-Used-Percent':'47',
    'X-Codex-Primary-Window-Minutes':'10080',
    'X-Codex-Primary-Reset-After-Seconds':'7200'
  }}})`);
  assert.equal(windows.length, 2);
  assert.equal(windows[0].minutes, 300);
  assert.equal(Math.round(windows[0].remaining), 82);
  assert.equal(windows[1].minutes, 10080);
  assert.equal(Math.round(windows[1].remaining), 53);
});

test('account table keeps quota and reset information in separate columns', () => {
  const p = panel();
  p.run(`renderAccounts(mergeQuotaAccounts([
    {email:'user@example.com',auth_index:'idx-1',account_id:'acct-1',status:'healthy',healthy:true}
  ],[
    {provider:'codex',auth_index:'idx-1',quota:{observed_at:'2026-09-09T10:00:00Z',signals:{
      'X-Codex-Primary-Used-Percent':'18',
      'X-Codex-Primary-Window-Minutes':'300',
      'X-Codex-Primary-Reset-At':'1788951000',
      'X-Codex-Secondary-Used-Percent':'47',
      'X-Codex-Secondary-Window-Minutes':'10080',
      'X-Codex-Secondary-Reset-At':'1789555800'
    },reset_credits:[{expires_at_ms:1789641600000},{expires_at_ms:1789814400000}]}}
  ]))`);
  const html = p.elements.get('accounts').innerHTML;
  assert.match(html, /class="quota-cell"/);
  assert.match(html, />5H</);
  assert.match(html, />82%/);
  assert.match(html, />7D</);
  assert.match(html, />53%/);
  assert.match(html, /class="reset-cell"/);
  assert.equal((html.match(/<td/g) || []).length, 11);
});
