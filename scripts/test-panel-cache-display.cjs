const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');
const ctx = {tr:s=>s, esc:s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')};
vm.createContext(ctx);
vm.runInContext(html.slice(html.indexOf('function mmIsGlobalOpenAI('), html.indexOf('function mmBadges(')),ctx);
vm.runInContext(html.slice(html.indexOf('function usageTraceCID('), html.indexOf('function renderUsageData(')),ctx);

test('all Global OpenAI rows show 272K–1M and 2× notice without modifying metadata',()=>{
  for(const id of ['gpt-6.1-sol','gpt-5.6-luna','cb/gpt-6-astra','chatgpt-latest','o3-mini','o4-mini','codex']){
    const m={id,ctx:'200K',mult:'x0.25'};
    const cell=ctx.mmCtxCell(m,'global');
    assert.match(cell,/272K–1M/); assert.match(cell,/&gt;272K input: credits 2×/);
    assert.match(cell,/Display only/); assert.equal(m.ctx,'200K');assert.equal(m.mult,'x0.25');
    assert.doesNotMatch(ctx.mmCtxCell(m,'cn'),/2×/);
  }
  assert.match(ctx.mmCtxCell({id:'provider-model',vendor:'OpenAI'},'global'),/2×/);
  assert.doesNotMatch(ctx.mmCtxCell({id:'grok-4.6',ctx:'500K'},'global'),/2×/);
});
function row(id,start,auth,hashes,cid='session'){
  const stage={body_bytes:500,message_count:hashes.length,message_hashes:hashes,
    system_hash:'sys',first_user_hash:'user',tools_hash:'tools',options_hash:'opts',body_hash:id};
  return {auth_id:auth,model:'gpt-6.1-sol',input_tokens:10000,cache_read_tokens:0,
    cache_trace:{request_id:id,started_at:start,inbound:stage,upstream:stage,
      attempts:[{auth_id:auth,cid,status:200,upstream_request_id:'out'}]}};
}
test('hash UI compares same session, shows account change and append-only, excludes prompt fields',()=>{
  const prev=row('prev','01','Gary',['a','b']);
  const other=row('other','02','Gary',['z'],'different-session');
  const target=row('miss','03','Thomas',['a','b','c']);
  target.payload='DO-NOT-PRINT-PROMPT';target.cache_trace.secret='DO-NOT-PRINT-TOKEN';
  target.cache_trace.attempts.unshift({auth_id:'Gary',cid:'session',status:429,upstream_request_id:'first'});
  const out=ctx.usageCacheTraceCell(target,[target,other,prev]);
  assert.match(out,/Cache hash · 0.0% · low hit · failover/);
  assert.match(out,/Previous same-session request: prev/);
  assert.match(out,/Account changed: yes/);assert.match(out,/Identical leading messages: 2/);
  assert.match(out,/Append-only messages/);assert.match(out,/HTTP 429/);
  assert.doesNotMatch(out,/other|DO-NOT-PRINT/);
});
test('changed prefixes, legacy records, filtered history and escaping are explicit',()=>{
  const a=row('a','01','Gary',['a','b']), b=row('<img onerror="bad">','02','Gary',['a','x']);
  b.cache_trace.upstream.tools_hash='changed';
  const out=ctx.usageCacheTraceCell(b,[a,b]);
  assert.match(out,/First changed message: 2/);assert.match(out,/Changed fields: tools_hash/);
  assert.match(out,/&lt;img/);assert.doesNotMatch(out,/<img/);
  assert.match(ctx.usageCacheTraceCell({},[]),/· hash tracing off/);
  assert.match(ctx.usageCacheTraceCell(b,[b]),/not in loaded records/);
});
test('truncated lists do not assert full append-only history',()=>{
  const a=row('a','01','Gary',['a']),b=row('b','02','Gary',['a','b']);
  a.cache_trace.upstream.hashes_truncated=true;
  const out=ctx.usageCacheTraceCell(b,[a,b]);
  assert.match(out,/Hash lists capped/);
  assert.doesNotMatch(ctx.usageTraceComparison(a.cache_trace.upstream,b.cache_trace.upstream),/Append-only/);
});
