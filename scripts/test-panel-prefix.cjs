// Prefix UI regression tests run the actual panel functions, without network.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const code = html.slice(html.indexOf('function mmRenderPrefixUI('), html.indexOf('/* ---------- Advanced tab'));
function panel(realm, state = {}) {
  const els = new Map();
  const ctx = {
    MM_DEFAULT_PFX: { global: 'cb/', cn: 'cn/' },
    MM: { state: { [realm]: { pfxOn: true, pfxVal: '', pfxDef: realm === 'global' ? 'cb/' : 'cn/', disabled: new Set(), ...state } }, cfg: { [realm]: { models: [{ id: 'fast-model' }] } } },
    document: { getElementById(id) {
      if (!els.has(id)) els.set(id, { value: '', classList: { toggle() {} } });
      return els.get(id);
    } },
    tr: s => s, esc: s => s, mmMarkDirty: () => {}, mmRenderAdvancedPreviews: () => {}
  };
  vm.createContext(ctx); vm.runInContext(code, ctx);
  return { ctx, el: id => ctx.document.getElementById(id) };
}
for (const [realm, prefix, suffix] of [['global', 'cb/', 'g'], ['cn', 'cn/', 'c']]) {
  test(`${realm}: ON empty shows default in field and preview, keeps saved value empty`, () => {
    const { ctx, el } = panel(realm);
    ctx.mmRenderPrefixUI(realm); ctx.mmRenderPreview(realm);
    assert.equal(el(`mm-pfx-${suffix}`).value, prefix);
    assert.equal(el(`mm-pfx-${suffix}`).disabled, false);
    assert.equal(ctx.MM.state[realm].pfxVal, '');
    assert.equal(ctx.mmEffPfx(realm), prefix);
    assert.match(el(`mm-prev-${suffix}-pfx`).innerHTML, new RegExp(prefix + 'fast-model'));
  });
  test(`${realm}: custom prefix, clear while editing, blur restores default`, () => {
    const { ctx, el } = panel(realm);
    ctx.mmPrefixInput(realm, 'yes/'); ctx.mmRenderPrefixUI(realm);
    assert.equal(el(`mm-pfx-${suffix}`).value, 'yes/');
    assert.equal(ctx.mmEffPfx(realm), 'yes/');
    ctx.mmPrefixInput(realm, ' yes ');
    assert.equal(ctx.mmEffPfx(realm), 'yes/');
    el(`mm-pfx-${suffix}`).value = '';
    ctx.mmPrefixInput(realm, '');
    assert.equal(el(`mm-pfx-${suffix}`).value, ''); // don't prevent clearing/typing
    assert.equal(ctx.mmEffPfx(realm), prefix);
    ctx.mmRenderPrefixUI(realm); // blur handler
    assert.equal(el(`mm-pfx-${suffix}`).value, prefix);
    assert.equal(ctx.MM.state[realm].pfxVal, '');
  });
  test(`${realm}: OFF stays bare; re-enable uses default or preserves custom`, () => {
    const { ctx, el } = panel(realm, { pfxOn: false });
    ctx.mmRenderPrefixUI(realm);
    assert.equal(el(`mm-pfx-${suffix}`).disabled, true);
    assert.equal(ctx.mmEffPfx(realm), '');
    ctx.mmTogglePrefix(realm);
    assert.equal(el(`mm-pfx-${suffix}`).value, prefix);
    ctx.mmPrefixInput(realm, 'yes/');
    ctx.mmTogglePrefix(realm); ctx.mmTogglePrefix(realm);
    assert.equal(el(`mm-pfx-${suffix}`).value, 'yes/');
    assert.equal(ctx.mmEffPfx(realm), 'yes/');
  });
}
test('whitespace-only saved input resolves to default rather than slash', () => {
  const { ctx } = panel('global', { pfxVal: '   ' });
  assert.equal(ctx.mmEffPfx('global'), 'cb/');
});
