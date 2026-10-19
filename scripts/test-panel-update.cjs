// Regression checks for the panel's real /update/check contract (no browser/network).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
// The update-widget code is executed verbatim; mdLite (render) and
// htmlUnescape (host-sanitizer entity decode) live elsewhere in panel.html,
// so extract them by name into the sandbox alongside the sliced section.
function extractFn(name) {
  const i = html.indexOf('function ' + name + '(');
  if (i < 0) throw new Error('function ' + name + ' not found in panel.html');
  let depth = 0;
  const j = html.indexOf('{', i);
  for (let k = j; k < html.length; k++) {
    if (html[k] === '{') depth++;
    else if (html[k] === '}') { depth--; if (!depth) return html.slice(i, k + 1); }
  }
  throw new Error('unbalanced braces for function ' + name);
}
const code = [extractFn('htmlUnescape'), extractFn('mdLite'), html.slice(html.indexOf('function mmCmpVer('), html.indexOf('// Skip version:'))].join('\n');
function panel() {
  const els = new Map();
  const calls = [];
  const ctx = {
    MM: { update: null, skipVersion: '' },
    document: { getElementById(id) {
      if (!els.has(id)) els.set(id, { style: {}, classes: {}, classList: { toggle(k, v) { els.get(id).classes[k] = v; } } });
      return els.get(id);
    } },
    tr: s => s, fmtAgo: s => s,
    esc: s => String(s).replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
    getKey: () => 'test-only-key',
    api: async p => { calls.push(p); return { current: '0.9.6', latest: '0.9.7', available: true, checkedAt: '2026-10-13T00:00:00Z', seen: false }; },
    mmToast: () => {}, window: { open: (...args) => calls.push(args) }
  };
  vm.createContext(ctx); vm.runInContext(code, ctx);
  return { ctx, calls, el: id => ctx.document.getElementById(id) };
}
test('actual flat API payload renders both versions, notification, time and release link', () => {
  const { ctx, el, calls } = panel();
  ctx.mmHandleUpdate({ current: '0.9.6', latest: '0.9.7', available: true, checkedAt: 'now', changelog: '<script>bad</script>', seen: false });
  assert.equal(el('ucur').textContent, 'v0.9.6');
  assert.equal(el('ulatest').textContent, 'v0.9.7');
  assert.match(el('ulabel').textContent, /v0.9.6.*v0.9.7/);
  assert.equal(el('udot').style.display, 'flex');
  assert.match(el('ulastcheck').textContent, /now/);
  assert.doesNotMatch(el('uchangelog').innerHTML, /<script>/);
  el('ugithub').onclick();
  assert.equal(calls[0][0], 'https://github.com/zidanefaqih/codebuddy-intl-cpa/releases/tag/v0.9.7');
});
test('equal, older, skipped and failed checks never hide version widget or claim an update', () => {
  for (const payload of [
    { current: '0.9.7', latest: '0.9.7', available: false },
    { current: '0.9.7-local', latest: '0.9.6', available: false },
    { current: '0.9.6', latest: '0.9.7', available: true, seen: true },
    { current: '0.9.6', latest: '0.9.7', available: true, failed: true },
    { current: '0.9.6', failed: true }
  ]) {
    const { ctx, el } = panel(); ctx.mmHandleUpdate(payload);
    assert.equal(el('uwidget').style.display, 'block');
    assert.equal(el('udot').style.display, 'none');
    if (payload.failed) assert.match(el('ustatus').textContent, /检查失败/);
  }
});
test('local skip removes only the notification and current-only model config preserves upstream', () => {
  const { ctx, el } = panel();
  ctx.mmHandleUpdate({ current: '0.9.6', latest: '0.9.7', available: true });
  ctx.MM.skipVersion = '0.9.7'; ctx.mmHandleUpdate({ current: '0.9.6' });
  assert.equal(el('ulatest').textContent, 'v0.9.7');
  assert.equal(el('uwidget').style.display, 'block');
  assert.equal(el('udot').style.display, 'none');
});
test('manual check bypasses backend cache; no credential means no request', async () => {
  const { ctx, calls } = panel();
  await ctx.mmCheckUpdate(true); assert.equal(calls[0], '/update/check?refresh=1');
  await ctx.mmCheckUpdate(false); assert.equal(calls[1], '/update/check');
  ctx.getKey = () => null;
  await ctx.mmCheckUpdate(false); await ctx.mmCheckUpdate(true);
  assert.equal(calls.length, 2);
});
test('network failure keeps last-known versions and does not report up-to-date', async () => {
  const { ctx, el } = panel(); await ctx.mmCheckUpdate(false);
  ctx.api = async () => { throw new Error('offline'); }; await ctx.mmCheckUpdate(true);
  assert.equal(el('ulatest').textContent, 'v0.9.7');
  assert.equal(el('udot').style.display, 'none');
  assert.match(el('ustatus').textContent, /检查失败/);
});
