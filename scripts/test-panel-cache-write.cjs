const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const ctx = {tr:s=>s, esc:s=>String(s).replace(/</g,'&lt;')};
vm.createContext(ctx);
vm.runInContext(html.slice(html.indexOf('function fmtCred('),html.indexOf('function fmtDT(')),ctx);

test('write presence distinguishes missing, zero, invalid and old rows',()=>{
  assert.equal(ctx.usageWriteTokens({cache_write_status:'reported',cache_write_tokens:0}),'0');
  assert.equal(ctx.usageWriteTokens({cache_write_status:'reported',cache_write_tokens:2000}),'2K');
  assert.equal(ctx.usageWriteTokens({cache_write_status:'missing',cache_write_tokens:0}),'write not reported');
  assert.equal(ctx.usageWriteTokens({cache_write_status:'invalid',cache_write_tokens:0}),'invalid write value');
  assert.equal(ctx.usageWriteTokens({cache_write_tokens:0}),'unknown legacy write');
  assert.equal(ctx.usageWriteTokens({cache_write_tokens:2000}),'2K · unmarked legacy write');
  assert.match(html,/\$\{usageWriteTokens\(r\)\}/);
  assert.match(html,/\.utbl \.cache-tokens\{[^}]*white-space:normal/);
});
