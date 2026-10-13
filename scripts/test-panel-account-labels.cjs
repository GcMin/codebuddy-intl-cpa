const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const code = html.slice(html.indexOf('function mmRenderFetchStats('), html.indexOf('async function mmRefetch('));
function panel(config) {
  const output = { innerHTML: '' }; const select = { value: 'uid-two.json' }; const urls = [];
  const ctx = { MM: {cfg: {global: config}, fetchAcct: {}}, document: {getElementById: id => id === 'mm-facct-g' ? select : output},
    tr: s => s, esc: s => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    fmtAgo: () => '', mmGetConfig: async () => urls.push(ctx.MM.fetchAcct.global), mmFail() {} };
  vm.createContext(ctx); vm.runInContext(code, ctx);
  return {ctx, output, urls};
}
test('fetch dropdown displays nicknames but selection and requests retain record IDs', async () => {
  const {ctx, output, urls} = panel({accounts: ['uid-one.json', 'uid-two.json'], fetch_account: 'uid-two.json',
    account_labels: {'uid-one.json':'Intl-imported', 'uid-two.json':'Intl-imported'}});
  ctx.mmRenderFetchStats('global');
  assert.ok(output.innerHTML.includes('<option value="uid-one.json" title="uid-one.json">Intl-imported</option>'));
  assert.ok(output.innerHTML.includes('<option value="uid-two.json" title="uid-two.json" selected>Intl-imported</option>'));
  await ctx.mmChangeFetchAcct('global'); assert.deepEqual(urls, ['uid-two.json']);
});
test('old/missing label payload falls back to ID and display text is escaped', () => {
  const {ctx, output} = panel({accounts: ['uid-one.json', 'uid-two.json'],
    account_labels: {'uid-one.json':'<img onerror="bad">'}});
  ctx.mmRenderFetchStats('global');
  assert.ok(output.innerHTML.includes('&lt;img onerror=&quot;bad&quot;&gt;'));
  assert.ok(output.innerHTML.includes('>uid-two.json</option>'));
  assert.ok(!output.innerHTML.includes('<img'));
});
