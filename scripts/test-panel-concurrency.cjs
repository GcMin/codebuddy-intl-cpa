const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');

// ---- card(): concurrency badge -------------------------------------------------
const tr = s=>s;
// Use the panel's REAL esc() so numeric inputs fail exactly like in the
// browser (the original bug: esc(2) -> "(2).replace is not a function").
const escSrc = html.slice(html.indexOf('function esc('), html.indexOf('function planLabel('));
vm.runInContext(escSrc, vm.createContext({})); // syntax check only
const escCtx = {tr};
vm.createContext(escCtx);
vm.runInContext(escSrc, escCtx);
const esc = escCtx.esc;
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

test('card does NOT call esc() on the numeric concurrency_limit (regression: .replace is not a function)', () => {
  // The exact field shape the backend sends: concurrency_limit is a NUMBER.
  // The old code passed it to esc() directly and killed the whole grid.
  const a = {auth_index:'1', nickname:'Gary', plan:'pro', region:'global', concurrency_current:0, concurrency_limit:2, concurrency_known:true};
  let out = '';
  assert.doesNotThrow(() => { out = cardCtx.card(a); });
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

// ---- card header layout contract ------------------------------------------------
// The badge strip must never be pushed outside the card. Without min-width:0 on
// the nickname, a long unbreakable name (e.g. "thomashallcodeevj") squeezed the
// strip past the card edge and hid the last badge — the live concurrency one.
// scripts/check-panel-card-overflow.mjs proves it by measuring in Chrome; these
// assertions keep the markup/CSS contract that makes it hold.
test('card wraps the nickname and keeps badges in a non-shrinking strip', () => {
  const out = cardCtx.card({auth_index:'1', nickname:'Intl-thomashallcodeevj', plan:'free', region:'global', concurrency_current:0, concurrency_limit:2, concurrency_known:true});
  assert.match(out, /<span class="cname" title="Intl-thomashallcodeevj">Intl-thomashallcodeevj<\/span>/);
  // The whole badge strip (region, plan, concurrency) lives inside .cbadges and
  // closes before </h2>, so no badge can be laid out next to the nickname.
  const strip = out.slice(out.indexOf('<span class="cbadges">'), out.indexOf('</h2>'));
  assert.match(strip, /<span class="badge conc" data-conc="1"/);
  assert.ok(strip.endsWith('</span>'), 'badge strip must close inside the header');
});

test('nickname in the title attribute is escaped', () => {
  const out = cardCtx.card({auth_index:'2', nickname:'"><img src=x>', plan:'pro', region:'global'});
  assert.doesNotMatch(out, /<img/);
  assert.match(out, /class="cname" title="&quot;&gt;&lt;img src=x&gt;"/);
});

test('card header CSS lets the name truncate and pins the badge strip', () => {
  const css = html.slice(html.indexOf('<style>'), html.indexOf('</style>'));
  assert.match(css, /\.card h2 \.cname\{[^}]*min-width:0/);
  assert.match(css, /\.card h2 \.cname\{[^}]*text-overflow:ellipsis/);
  assert.match(css, /\.card h2 \.cbadges\{[^}]*flex:0 0 auto/);
});

// The badge is looked up in GET /concurrency, whose keys are gate keys
// (realm:UID) — not auth_index. Shipping auth_index here made every badge sit
// at 0/2 and the 5s poll overwrite the correct value from /accounts.
test('badge uses the gate key from /accounts, never auth_index alone', () => {
  const gate = 'global:95e6659d-2f49-45a1-8abb-be3f7cfd8c28';
  const withKey = cardCtx.card({auth_index:'0ffe10a82d6cfd07', concurrency_key:gate, nickname:'P', plan:'trial',
    region:'global', concurrency_current:1, concurrency_limit:2, concurrency_known:true});
  assert.match(withKey, new RegExp('data-conc="' + gate.replace(/[:]/g, '\\:') + '"'));
  assert.doesNotMatch(withKey, /data-conc="0ffe10a82d6cfd07"/);

  // Legacy payload without the key still renders (falls back), so an older
  // /accounts response cannot break the grid.
  const withoutKey = cardCtx.card({auth_index:'0ffe10a82d6cfd07', nickname:'P', plan:'trial',
    region:'global', concurrency_current:0, concurrency_limit:2, concurrency_known:true});
  assert.match(withoutKey, /data-conc="0ffe10a82d6cfd07"/);
});

test('poll resolves the badge through the gate-keyed slots map', async () => {
  const gate = 'global:95e6659d-2f49-45a1-8abb-be3f7cfd8c28';
  const stub = domStub();
  stub.badges = [(() => { const o = {idx: gate, textContent:'0/2', _cls:new Set(),
      get classList(){return o;}, toggle(n,on){on?o._cls.add(n):o._cls.delete(n);},
      getAttribute(){return o.idx;}}; return o; })()];
  stub.api = (path) => { stub.apiCalls.push(path); return Promise.resolve({limit:2, slots:{[gate]:2}}); };
  const ctx = {document: stub, api: stub.api.bind(stub), getKey: stub.getKey};
  vm.createContext(ctx);
  const src = html.slice(html.indexOf('async function refreshConcurrencyBadges'), html.indexOf('setInterval(refreshConcurrencyBadges'));
  vm.runInContext(src + '; refreshConcurrencyBadges()', ctx);
  await new Promise(r=>setTimeout(r,10));
  assert.equal(stub.badges[0].textContent, '2/2');
  assert.ok(stub.badges[0]._cls.has('full'));
});
