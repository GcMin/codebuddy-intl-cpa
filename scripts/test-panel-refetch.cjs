// Exercise real panel functions without a browser or paid upstream calls.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const extract = (start, end) => html.slice(html.indexOf(start), html.indexOf(end));
const code = extract('function mmRevision(', 'function mmRenderAll(')
  + extract('function mmMarkDirty(', 'function mmYamlFor(')
  + extract('async function mmSave(', '// mmReloadToast reports')
  + extract('function mmClose(', 'function mmMaskClick(');
const snapshot = (value = '') => ({ realms: {
  global: { prefix: { enabled: true, value, def: 'cb/' }, disabled: ['saved'], models: [{ id: value || 'new-model' }] },
  cn: { prefix: { enabled: true, value: 'cn/', def: 'cn/' }, disabled: [] }
} });
const deferred = () => { let resolve; const promise = new Promise(r => resolve = r); return { promise, resolve }; };
function panel() {
  const elements = new Map();
  const calls = [];
  const ctx = {
    MM_DEFAULT_PFX: { global: 'cb/', cn: 'cn/' },
    MM: { cfg: {}, state: {}, dirty: { global: false, cn: false }, fetchAcct: {}, loaded: false },
    document: { getElementById(id) {
      if (!elements.has(id)) elements.set(id, { textContent: '', value: '', classList: { remove() {} }, querySelector: () => null });
      return elements.get(id);
    } },
    api: async (url, options) => { calls.push([url, options]); return snapshot(); },
    tr: s => s, mmRenderAll() {}, mmReloadToast() {}, mmToast() {}, setTimeout() {},
  };
  vm.createContext(ctx); vm.runInContext(code, ctx);
  return { ctx, calls };
}

test('refetch updates catalog/saved snapshot but preserves both dirty drafts', async () => {
  const { ctx } = panel(); await ctx.mmGetConfig();
  const g = ctx.MM.state.global; const c = ctx.MM.state.cn;
  g.pfxOn = false; g.disabled.add('draft'); c.pfxVal = 'other/';
  ctx.mmMarkDirty('global'); ctx.mmMarkDirty('cn');
  ctx.api = async () => snapshot('new/'); await ctx.mmGetConfig();
  assert.equal(ctx.MM.cfg.global.prefix.value, 'new/');
  assert.equal(ctx.MM.state.global, g); assert.equal(ctx.MM.state.cn, c);
  assert.equal(g.pfxOn, false); assert.ok(g.disabled.has('draft'));
  assert.equal(ctx.MM.dirty.global, true); assert.equal(ctx.MM.dirty.cn, true);
});

for (const action of ['mmSave', 'mmReset']) {
  test(`${action}: only target realm resets after success`, async () => {
    const { ctx } = panel(); await ctx.mmGetConfig();
    ctx.MM.state.global.pfxOn = false; ctx.mmMarkDirty('global');
    ctx.MM.state.cn.pfxVal = 'other/'; ctx.mmMarkDirty('cn');
    const c = ctx.MM.state.cn;
    await ctx[action]('global');
    assert.equal(ctx.MM.state.global.pfxOn, true); assert.equal(ctx.MM.dirty.global, false);
    assert.equal(ctx.MM.state.cn, c); assert.equal(c.pfxVal, 'other/'); assert.equal(ctx.MM.dirty.cn, true);
  });
  test(`${action}: edits made while request is pending survive`, async () => {
    const { ctx } = panel(); await ctx.mmGetConfig();
    ctx.mmMarkDirty('global'); const pending = deferred(); let n = 0;
    ctx.api = async () => ++n === 1 ? pending.promise : snapshot();
    const save = ctx[action]('global');
    ctx.MM.state.global.pfxVal = 'new-draft/'; ctx.mmMarkDirty('global');
    pending.resolve(snapshot()); await save;
    assert.equal(ctx.MM.state.global.pfxVal, 'new-draft/'); assert.equal(ctx.MM.dirty.global, true);
  });
  test(`${action}: failure preserves draft and dirty state`, async () => {
    const { ctx } = panel(); await ctx.mmGetConfig();
    ctx.MM.state.global.pfxOn = false; ctx.mmMarkDirty('global');
    ctx.api = async () => { throw new Error('failed'); }; await ctx[action]('global');
    assert.equal(ctx.MM.state.global.pfxOn, false); assert.equal(ctx.MM.dirty.global, true);
  });
}

test('out-of-order GET response cannot replace a newer snapshot', async () => {
  const { ctx } = panel(); const old = deferred(); const latest = deferred(); let n = 0;
  ctx.api = () => ++n === 1 ? old.promise : latest.promise;
  const p1 = ctx.mmGetConfig(); const p2 = ctx.mmGetConfig();
  latest.resolve(snapshot('latest/')); await p2;
  old.resolve(snapshot('old/')); await p1;
  assert.equal(ctx.MM.cfg.global.prefix.value, 'latest/'); assert.equal(ctx.MM.state.global.pfxVal, 'latest/');
});

test('a newer refetch carries pending successful Save reset for its realm', async () => {
  const { ctx } = panel(); await ctx.mmGetConfig(); ctx.mmMarkDirty('global');
  const revision = ctx.mmRevision('global');
  const old = deferred(); const latest = deferred(); let n = 0;
  ctx.api = () => ++n === 1 ? old.promise : latest.promise;
  const p1 = ctx.mmGetConfig('global', revision); const p2 = ctx.mmGetConfig();
  latest.resolve(snapshot('saved/')); await p2;
  old.resolve(snapshot('stale/')); await p1;
  assert.equal(ctx.MM.state.global.pfxVal, 'saved/'); assert.equal(ctx.MM.dirty.global, false);
});

test('close during fetch discards draft; response does not resurrect it', async () => {
  const { ctx } = panel(); await ctx.mmGetConfig();
  ctx.MM.state.global.pfxVal = 'discard/'; ctx.mmMarkDirty('global');
  const pending = deferred(); ctx.api = () => pending.promise;
  const fetch = ctx.mmGetConfig(); ctx.mmClose('global');
  pending.resolve(snapshot('server/')); await fetch;
  assert.equal(ctx.MM.state.global.pfxVal, 'server/'); assert.equal(ctx.MM.dirty.global, false);
});

test('close/reopen during Save cannot discard new draft on completion', async () => {
  const { ctx } = panel(); await ctx.mmGetConfig(); ctx.mmMarkDirty('global');
  const pending = deferred(); let n = 0;
  ctx.api = async () => ++n === 1 ? pending.promise : snapshot('server/');
  const save = ctx.mmSave('global'); ctx.mmClose('global');
  ctx.MM.state.global.pfxVal = 'reopened/'; ctx.mmMarkDirty('global');
  pending.resolve(snapshot()); await save;
  assert.equal(ctx.MM.state.global.pfxVal, 'reopened/'); assert.equal(ctx.MM.dirty.global, true);
});
