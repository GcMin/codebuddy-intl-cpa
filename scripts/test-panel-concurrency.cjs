const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');

// ---- card(): concurrency badge -------------------------------------------------
const tr = s=>s;
const esc = s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const cardCtx = {tr, esc};
vm.createContext(cardCtx);
vm.runInContext(html.slice(html.indexOf('function planLabel('), html.indexOf('function card(')) === '' ? '' : html.slice(html.indexOf('function planLabel('), html.indexOf('let currentFilter')), cardCtx);

test('card renders live concurrency badge with data-conc hook and 0/2 default', () => {
  const a = {auth_index:'1', nickname:'Gary', plan:'pro', region:'global', concurrency_current:0, concurrency_limit:2, concurrency_known:true};
  const out = cardCtx.card(a);
  assert.match(out, /class="badge conc"/); // idle: no busy/full modifier
  assert.match(out, /data-conc="1"/);
  assert.match(out, />0\/2</);
});

test('badge marks busy (1/2) and full (2/2) with modifier classes', () => {
  const mid = cardCtx.card({auth_index:'2', nickname:'X', plan:'pro', region:'global', concurrency_current:1, concurrency_limit:2, concurrency_known:true});
  assert.match(mid, /badge conc busy/);
  assert.match(mid, />1\/2</);
  const full = cardCtx.card({auth_index:'3', nickname:'Y', plan:'pro', region:'cn', concurrency_current:2, concurrency_limit:2, concurrency_known:true});
  assert.match(full, /badge conc full/);
  assert.match(full, />2\/2</);
});

test('legacy dashboard without concurrency fields shows no badge', () => {
  const out = cardCtx.card({auth_index:'4', nickname:'Old', plan:'pro', region:'global'});
  assert.doesNotMatch(out, /data-conc/);
});

test('badge content escapes account index', () => {
  const out = cardCtx.card({auth_index:'"><script>', nickname:'Z', plan:'pro', region:'global', concurrency_current:0, concurrency_limit:2, concurrency_known:true});
  assert.doesNotMatch(out, /<script>/);
  assert.match(out, /data-conc="&quot;&gt;&lt;script&gt;"/);
});

// ---- refreshConcurrencyBadges(): in-place DOM update ----------------------------
function domStub(){
  const els = [];
  const mk = idx => ({idx, textContent:'', classes:new Set(), classList:{toggle(n,on){on?els&&0:0; mk0(n,on);}}});
  const badge = idx => {
    const o = {idx, textContent:'', _cls:new Set(),
      get classList(){ return o; },
      toggle(n,on){ on ? o._cls.add(n) : o._cls.delete(n); },
      getAttribute(){ return o.idx; }
    };
    return o;
  };
  return {
    badges: [badge('a'), badge('b')],
    hidden: false,
    apiCalls: [],
    api(path){ this.apiCalls.push(path); return Promise.resolve({limit:2, slots:{a:2, b:0}}); },
    getKey(){ return 'key'; },
    querySelectorAll(){ return this.badges; }
  };
}
test('refreshConcurrencyBadges updates numbers and classes without re-render', async () => {
  const stub = domStub();
  const ctx = {document: stub, api: stub.api.bind(stub), getKey: stub.getKey};
  vm.createContext(ctx);
  const src = html.slice(html.indexOf('async function refreshConcurrencyBadges'), html.indexOf('setInterval(refreshConcurrencyBadges'));
  vm.runInContext(src + '; refreshConcurrencyBadges()', ctx);
  await new Promise(r=>setTimeout(r,10));
  assert.equal(stub.badges[0].textContent, '2/2');
  assert.ok(stub.badges[0]._cls.has('full'));
  assert.equal(stub.badges[1].textContent, '0/2');
  assert.ok(!stub.badges[1]._cls.has('busy'));
  assert.deepEqual(stub.apiCalls, ['/concurrency']);
});

test('refreshConcurrencyBadges is silent on transient API failure', async () => {
  const stub = domStub();
  stub.api = () => Promise.reject(new Error('net'));
  const ctx = {document: stub, api: stub.api, getKey: stub.getKey};
  vm.createContext(ctx);
  const src = html.slice(html.indexOf('async function refreshConcurrencyBadges'), html.indexOf('setInterval(refreshConcurrencyBadges'));
  vm.runInContext(src + '; refreshConcurrencyBadges()', ctx);
  await new Promise(r=>setTimeout(r,10));
  assert.equal(stub.badges[0].textContent, ''); // untouched
});
