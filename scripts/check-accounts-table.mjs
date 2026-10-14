// End-to-end check of the accounts table, run against the REAL panel.html with
// a fetch stub (no network) and the sample snapshots. Chrome is optional: with
// no browser the check reports "skipped" and exits 0.
//
// Verifies what unit tests cannot: that the table actually renders in a browser,
// that a row expands/collapses, that the countdown ticker rewrites only text,
// and that search / sort / paging / the card toggle behave.
import {execFileSync} from 'node:child_process';
import {existsSync, mkdtempSync, readFileSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname, join} from 'node:path';
import {fileURLToPath} from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '..');

function chromeBinary() {
  if (process.env.CHROME) return process.env.CHROME;
  for (const name of ['google-chrome', 'chromium', 'chromium-browser', 'google-chrome-stable']) {
    for (const dir of (process.env.PATH || '').split(':')) {
      const candidate = join(dir, name);
      if (dir && existsSync(candidate)) return candidate;
    }
  }
  return '';
}

const snap = name => {
  const p = join(root, 'mockups', name);
  return existsSync(p) ? JSON.parse(readFileSync(p, 'utf8')) : null;
};
const accounts = snap('accounts-sample.json');
const credits = snap('credits-sample.json');
const today = snap('today-sample.json');

const chrome = chromeBinary();
if (!chrome || !accounts || !credits) {
  console.log('accounts table check: skipped (needs Chrome and mockups/*-sample.json)');
  process.exit(0);
}

const byIndex = {};
for (const a of credits.accounts || []) byIndex[a.auth_index] = a;
const perAccount = {};
for (const [idx, a] of Object.entries(byIndex)) {
  perAccount[idx] = {accounts: [{auth_index: idx, nickname: a.nickname, plan: a.plan, region: a.region,
    credits: a.credits, checkin: a.checkin, exhausted: a.exhausted, trial_claimed: a.trial_claimed}]};
}
const light = {accounts: (accounts.accounts || []).map(a => {
  const c = {...a}; delete c.credits;
  // Reproduce the reported cold-cache shape for CN: credits already known from a
  // warm cache, but no check-in summary yet. The row must fill itself in.
  if (c.region === 'cn') {
    const full = byIndex[c.auth_index];
    c.credits = full && full.credits;
    delete c.checkin;
  }
  // Put the widest realistic content on every Global row: a long streak and a
  // long nickname. Without them the table fits and a wrap regression hides.
  if (c.region === 'global') {
    c.nickname = (c.nickname || '') + '-longnicknamesuffix';
    c.daily = {...(c.daily || {}), lit: true, score: 2, lit_days: Array.from({length: 30}, (_, i) => `2026-09-${String(i+1).padStart(2,'0')}`)};
  }
  return c;
})};

const panel = readFileSync(join(root, 'workbuddy', 'panel.html'), 'utf8');
const stub = `<script>
window.__calls=[];
window.fetch=function(url){
  window.__calls.push(String(url));
  var u=String(url), body={}, per=${JSON.stringify(perAccount)};
  if(u.indexOf('/usage/summary')>=0) body=${JSON.stringify(today || {accounts: []})};
  else if(u.indexOf('/credits')>=0){ var m=u.match(/auth_index=([^&]+)/); body=(m&&per[decodeURIComponent(m[1])])||{}; }
  else if(u.indexOf('/accounts')>=0||u.indexOf('/refresh')>=0) body=${JSON.stringify(light)};
  else if(u.indexOf('/concurrency')>=0) body={limit:2,slots:{}};
  else if(u.indexOf('/dailybonus/status')>=0) body={enabled:true,schedule:[8,12,16,20]};
  else if(u.indexOf('/usage')>=0) body={records:[],totals:{}};
  return Promise.resolve({status:200,ok:true,json:function(){return Promise.resolve(body);},text:function(){return Promise.resolve(JSON.stringify(body));}});
};
try{ sessionStorage.setItem('workbuddy-mgmt-key','e2e'); }catch(e){}
// English, like the operator's panel: a leaked Chinese source string must fail.
try{ localStorage.setItem('workbuddy-panel-language','en'); }catch(e){}
</script>`;

const probe = `<script>
window.addEventListener('load',()=>{setTimeout(()=>{
  const out={};
  const rows=()=>document.querySelectorAll('#rows tr.acct-row');
  const details=()=>document.querySelectorAll('#rows tr.detail-row');
  out.rendered={rows:rows().length, details:details().length,
    tableShown:document.getElementById('tableWrap').style.display!=='none',
    cardsHidden:document.getElementById('cardWrap').style.display==='none',
    pager:(document.getElementById('pgInfo')||{}).textContent||''};
  const r=rows()[0];
  out.row={hasChevron:!!r.querySelector('.chev'), hasGateKeyBadge:!!r.querySelector('[data-conc]'),
    cells:r.querySelectorAll('td').length};

  // expand / collapse
  r.click();
  out.opened={details:details().length, packs:document.querySelectorAll('#rows tr.detail-row .pack-tbl tbody tr').length,
    chevronOpen:!!document.querySelector('#rows tr.acct-row.open .chev.open')};
  const cell=document.querySelector('#rows [data-exp]');
  const rowNode=document.querySelector('#rows tr.acct-row');
  out.expiry={text:cell?cell.textContent:''};
  const realNow=Date.now; Date.now=()=>realNow()+60000;
  tickExpiry();
  out.expiry.afterTick=cell?cell.textContent:'';
  out.expiry.changed=out.expiry.text!==out.expiry.afterTick;
  Date.now=()=>realNow()+400*86400000;
  tickExpiry();
  out.expiry.afterFar=cell?cell.textContent:'';
  out.expiry.cls=cell?cell.className:'';
  Date.now=realNow;
  out.expiry.noRerender = rowNode===document.querySelector('#rows tr.acct-row');
  out.expiry.timerRunning = expiryTimer!==null;
  document.querySelector('#rows tr.acct-row').click();
  out.closed={details:details().length, timerRunning:expiryTimer!==null};

  // checkbox must not toggle the row
  const cb=document.querySelector('#rows tr.acct-row input[type=checkbox]');
  cb.click();
  out.checkbox={details:details().length, picked:document.querySelectorAll('#rows tr.acct-row.picked').length,
    selbarShown:document.getElementById('selbar').classList.contains('on')};
  clearSel();

  // search
  const q=document.getElementById('acctSearch');
  q.value='zzz-no-match'; q.dispatchEvent(new Event('input'));
  out.searchNoMatch={rows:rows().length, empty:!!document.querySelector('#rows .empty')};
  q.value=''; q.dispatchEvent(new Event('input'));
  out.searchReset={rows:rows().length};

  // sort by name (click header) must reorder deterministically
  const thName=[...document.querySelectorAll('.acct-table th.sortable')].find(t=>t.dataset.k==='name');
  thName.click();
  out.sortName=[...rows()].map(t=>(t.querySelector('.nm-main')||{}).textContent);
  out.sortArrow=(thName.querySelector('.arrow')||{}).textContent;

  // paging
  const per=document.getElementById('perPage'); per.value='10'; per.dispatchEvent(new Event('change'));
  out.paged={rows:rows().length, pager:(document.getElementById('pgInfo')||{}).textContent};
  per.value='20'; per.dispatchEvent(new Event('change'));

  // view toggle
  setView('cards');
  out.cards={shown:document.getElementById('cardWrap').style.display!=='none',
    cards:document.querySelectorAll('#grid .card').length,
    tableHidden:document.getElementById('tableWrap').style.display==='none'};
  setView('table');
  out.backToTable={rows:rows().length};

  // Language leak + row uniformity.
  const CJK=/[\u3400-\u9fff]/;
  out.language=document.documentElement.getAttribute('data-lang')||panelLanguage;
  out.cjkInRows=[...document.querySelectorAll('#rows tr.acct-row')].map(r=>{
    const txt=r.textContent.replace(/\s+/g,' ').trim();
    return CJK.test(txt)?txt.slice(0,60):null;
  }).filter(Boolean);
  out.rowHeights=[...new Set([...document.querySelectorAll('#rows tr.acct-row')].map(r=>Math.round(r.getBoundingClientRect().height)))];
  const tw=document.querySelector('.tbl-wrap'), tb=document.querySelector('.acct-table');
  out.tableWidth={wrap:tw.clientWidth, table:tb.scrollWidth, inner:window.innerWidth};
  out.colWidths=[...document.querySelectorAll('.acct-table thead th')].map(th=>Math.round(th.getBoundingClientRect().width));
  // Actions must line up: the cell is right-aligned, so without fixed-width
  // slots the group width decides the position ("Lit + Claimed" is narrower than
  // "Lit + Claim pack") and the first button jumps 15px between rows.
  // Slots are right-aligned, so a row with one button (CN "Checked in") sits in
  // the LAST slot. Compare the last button across rows, and the first button
  // only among rows that actually have two.
  out.actionLastX=[];
  out.actionFirstX=[];
  document.querySelectorAll('#rows tr.acct-row').forEach(r=>{
    const bs=[...r.querySelectorAll('.act button')].map(b=>Math.round(b.getBoundingClientRect().left));
    if(!bs.length) return;
    out.actionLastX.push(bs[bs.length-1]);
    if(bs.length>1) out.actionFirstX.push(bs[0]);
  });
  out.actionWs=[...new Set([...document.querySelectorAll('#rows .actgrp > *')].map(el=>Math.round(el.getBoundingClientRect().width)))];
  // The CN row must have filled its daily piece from the lazy fetch.
  const cnRow=[...document.querySelectorAll('#rows tr.acct-row')].find(r=>(r.dataset.region||'')==='cn');
  out.cnDaily=cnRow?(cnRow.querySelector('.daily')||{}).textContent.replace(/\s+/g,' ').trim():'(no cn row)';
  out.badgeWrap=[...document.querySelectorAll('#rows tr.acct-row')].filter(r=>{
    const tops=[...r.querySelectorAll('td:nth-child(3) .badge')].map(b=>Math.round(b.getBoundingClientRect().top));
    return new Set(tops).size>1;
  }).length;

  const pre=document.createElement('pre'); pre.id='probe'; pre.textContent=JSON.stringify(out,null,1);
  document.body.appendChild(pre);
},900)});
</script>`;

const dir = mkdtempSync(join(tmpdir(), 'acct-table-'));
const file = join(dir, 'e2e.html');
writeFileSync(file, panel.replace('<script>', stub + '<script>', 1).replace('</body>', probe + '</body>'));

const dom = execFileSync(chrome, ['--headless', '--disable-gpu', '--no-sandbox', '--window-size=1000,1200',  // narrow, like the panel inside the CPA iframe
  '--virtual-time-budget=9000', '--dump-dom', `file://${file}`], {encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore']});

const m = dom.match(/<pre id="probe">([\s\S]*?)<\/pre>/);
if (!m) {
  console.error('accounts table check: no measurement output (panel failed to render)');
  process.exit(1);
}
const out = JSON.parse(m[1].replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&#39;/g, "'"));

if (process.env.DEBUG_WIDTH) console.log('tableWidth', JSON.stringify(out.tableWidth), 'cols', JSON.stringify(out.colWidths), 'badgeWrap', out.badgeWrap, 'heights', JSON.stringify(out.rowHeights));

let failures = 0;
function check(label, ok, detail) {
  if (ok) { console.log(`ok    ${label}`); return; }
  failures++;
  console.error(`FAIL  ${label}${detail === undefined ? '' : ' → ' + JSON.stringify(detail)}`);
}

const total = (light.accounts || []).length;
check('table is the default view with every account', out.rendered.rows === total && out.rendered.tableShown && out.rendered.cardsHidden, out.rendered);
check('pager reports the full result set', /of\s+\d+/.test(out.rendered.pager), out.rendered.pager);
check('row has chevron, gate-keyed badge and 9 cells', out.row.hasChevron && out.row.hasGateKeyBadge && out.row.cells === 9, out.row);
check('clicking a row opens its package list', out.opened.details === 1 && out.opened.packs > 0 && out.opened.chevronOpen, out.opened);
check('countdown tick rewrites text only (no re-render)', out.expiry.changed && out.expiry.noRerender, out.expiry);
// The label is translated (zh source, en output), so accept either spelling.
check('far-future expiry renders as expired', /expired|已过期/.test(out.expiry.afterFar) && /out/.test(out.expiry.cls), out.expiry);
check('timer runs while open and stops when closed', out.expiry.timerRunning && !out.closed.timerRunning, out.closed);
check('checkbox selects without expanding the row', out.checkbox.details === 0 && out.checkbox.picked === 1 && out.checkbox.selbarShown, out.checkbox);
check('search with no match shows the empty state', out.searchNoMatch.rows === 0 && out.searchNoMatch.empty, out.searchNoMatch);
check('clearing search restores every row', out.searchReset.rows === total, out.searchReset);
check('sorting by name is ordered', out.sortName.join('|') === [...out.sortName].sort((a, b) => a.localeCompare(b)).join('|'), out.sortName.slice(0, 3));
check('per-page 10 limits the page', out.paged.rows === Math.min(10, total), out.paged);
check('card view renders cards and hides the table', out.cards.shown && out.cards.cards === total && out.cards.tableHidden, out.cards);
check('toggling back to table restores rows', out.backToTable.rows === Math.min(20, total), out.backToTable);

// English mode must not leak the Chinese source of any row label. tr() is a
// whole-string lookup, so a button built with markup inside the key silently
// falls back to Chinese (that is exactly what shipped once).
check('no Chinese text leaks into English rows', out.cjkInRows.length === 0, out.cjkInRows);
// A CN account with credits but no cached check-in must fetch and show it,
// instead of sitting on "No check-in data yet".
check('CN row fills its check-in state instead of showing no data',
  /Checked in|Not checked in|Inactive/.test(out.cnDaily) && !/No check-in data/.test(out.cnDaily), out.cnDaily);

// Rows must stay one height and region badges must not wrap: wrapping made the
// table ragged at narrower widths (54px rows became 70px).
// Sub-pixel rounding makes this 53 vs 54; what matters is that no row grew a
// line (the wrap regression produced 70px rows).
check('every row has the same height', Math.max(...out.rowHeights) - Math.min(...out.rowHeights) <= 2, out.rowHeights);
check('region badges never wrap', out.badgeWrap === 0, out.badgeWrap);
check('the rightmost action button lines up on every row', new Set(out.actionLastX).size === 1, out.actionLastX);
check('the first action button lines up across rows with two', new Set(out.actionFirstX).size === 1, out.actionFirstX);
check('action slots share one width', out.actionWs.length === 1, out.actionWs);

if (failures) {
  console.error(`\naccounts table check: ${failures} failure(s)`);
  process.exit(1);
}
console.log('\naccounts table check: all good');
