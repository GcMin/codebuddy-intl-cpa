// Closing a Model Manager must discard only its unsaved draft (no network).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const revisionCode = html.slice(html.indexOf('function mmRevision('), html.indexOf('async function mmGetConfig('));
const stateCode = html.slice(html.indexOf('function mmStateFromCfg('), html.indexOf('function mmRenderAll('));
const modalCode = html.slice(html.indexOf('function mmOpen('), html.indexOf('function mmTab('));
function panel() {
  const els = new Map();
  const renders = [];
  const ctx = {
    MM_DEFAULT_PFX: { global: 'cb/', cn: 'cn/' },
    MM: { loaded: true, cfg: {}, state: {}, dirty: { global: false, cn: false } },
    document: { getElementById(id) {
      if (!els.has(id)) {
        const classes = new Set();
        const pre = { textContent: 'stale draft YAML', style: { display: 'block' } };
        els.set(id, { value: 'unsaved', textContent: '', classes, pre,
          classList: { add: s => classes.add(s), remove: s => classes.delete(s) },
          querySelector: () => pre });
      }
      return els.get(id);
    } },
    mmGetConfig: () => { throw new Error('unexpected network fetch'); }
  };
  for (const name of ['mmRenderPrefixUI', 'mmRenderFallbackUI', 'mmRenderModels', 'mmRenderPreview', 'mmRenderAdvancedPreviews']) ctx[name] = r => renders.push([name, r]);
  vm.createContext(ctx); vm.runInContext(revisionCode + stateCode + modalCode, ctx);
  for (const realm of ['global', 'cn']) {
    ctx.MM.cfg[realm] = { prefix: { enabled: true, value: '', def: ctx.MM_DEFAULT_PFX[realm] }, disabled: ['old-model'], custom: [{ id: 'saved-custom', name: 'Saved', ctx: '200000' }], fallback: { enabled: true, model: 'fast-model' }, retry_fallback: { enabled: false, model: '' } };
    ctx.MM.state[realm] = ctx.mmStateFromCfg(realm, ctx.MM.cfg[realm]);
  }
  return { ctx, renders, el: id => ctx.document.getElementById(id) };
}
for (const realm of ['global', 'cn']) {
  test(`${realm}: close/reopen discards prefix/models/custom/fallback draft and dirty/YAML`, () => {
    const { ctx, el, renders } = panel();
    const suffix = realm === 'global' ? 'g' : 'c';
    const st = ctx.MM.state[realm];
    st.pfxOn = false; st.pfxVal = 'yes/'; st.disabled.add('fast-model');
    st.custom[0].name = 'Edited'; st.custom.push({ id: 'unsaved-custom' });
    st.fbOn = false; st.fbModel = 'deep-model'; st.fbrOn = true; st.fbrModel = 'deep-model';
    ctx.MM.dirty[realm] = true;
    ctx.mmClose(realm); ctx.mmOpen(realm);
    const restored = ctx.MM.state[realm];
    assert.equal(restored.pfxOn, true); assert.equal(restored.pfxVal, '');
    assert.equal(restored.disabled.has('fast-model'), false);
    assert.equal(restored.disabled.has('old-model'), true);
    assert.equal(restored.custom.length, 1); assert.equal(restored.custom[0].name, 'Saved');
    assert.equal(restored.fbOn, true); assert.equal(restored.fbModel, 'fast-model');
    assert.equal(restored.fbrOn, false); assert.equal(restored.fbrModel, '');
    assert.equal(ctx.MM.dirty[realm], false); assert.equal(el(`mm-dirty-${suffix}`).textContent, '');
    assert.equal(el(`mm-add-${suffix}-id`).value, '');
    assert.equal(el(`mm-ov-${realm}`).pre.style.display, 'none');
    assert.ok(renders.some(([name, r]) => name === 'mmRenderPrefixUI' && r === realm));
    assert.ok(renders.some(([name, r]) => name === 'mmRenderFallbackUI' && r === realm));
  });
}
test('discarding Global does not discard the CN draft', () => {
  const { ctx } = panel(); ctx.MM.state.cn.pfxVal = 'other/'; ctx.MM.dirty.cn = true;
  ctx.mmClose('global');
  assert.equal(ctx.MM.state.cn.pfxVal, 'other/'); assert.equal(ctx.MM.dirty.cn, true);
});
test('backdrop uses the same discard path; clicks inside modal do not close', () => {
  const { ctx, el } = panel(); ctx.MM.state.global.pfxOn = false;
  ctx.mmMaskClick({ target: {} }, 'global'); assert.equal(ctx.MM.state.global.pfxOn, false);
  ctx.mmMaskClick({ target: el('mm-ov-global') }, 'global'); assert.equal(ctx.MM.state.global.pfxOn, true);
});
test('new saved snapshot survives close/reopen instead of reverting to the older value', () => {
  const { ctx } = panel();
  ctx.MM.cfg.global.prefix = { enabled: false, value: '', def: 'cb/' }; // successful Save + GET
  ctx.MM.state.global = ctx.mmStateFromCfg('global', ctx.MM.cfg.global);
  ctx.mmClose('global'); ctx.mmOpen('global');
  assert.equal(ctx.MM.state.global.pfxOn, false);
});
