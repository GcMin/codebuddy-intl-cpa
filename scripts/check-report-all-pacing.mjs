// Verifies that "Report all" walks accounts ONE AT A TIME with a pause between
// them, instead of firing every account at once. That pacing is the point: a
// burst is what produced the 429 storms, and the host's own report script paces
// its accounts with a 1s sleep.
//
// Runs the real panel.html in headless Chrome with a stubbed fetch that records
// the timing of every /dailybonus call. Chrome is optional.
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

const chrome = chromeBinary();
if (!chrome) {
  console.log('report-all pacing check: skipped (no Chrome/Chromium found)');
  process.exit(0);
}

const panel = readFileSync(join(root, 'workbuddy', 'panel.html'), 'utf8');

// Four Global accounts, none lit, so all four are report targets.
const accounts = [
  {auth_index: 'g1', nickname: 'Intl-a', uid: 'u1', region: 'global', plan: 'free', daily: {lit: false, status: 'not-lit'}},
  {auth_index: 'g2', nickname: 'Intl-b', uid: 'u2', region: 'global', plan: 'free', daily: {lit: false, status: 'not-lit'}},
  {auth_index: 'g3', nickname: 'Intl-c', uid: 'u3', region: 'global', plan: 'free', daily: {lit: false, status: 'not-lit'}},
  {auth_index: 'g4', nickname: 'Intl-d', uid: 'u4', region: 'global', plan: 'free', daily: {lit: false, status: 'not-lit'}},
  // Already lit: must be skipped entirely (no request at all).
  {auth_index: 'g5', nickname: 'Intl-e', uid: 'u5', region: 'global', plan: 'free', daily: {lit: true, score: 2}},
  // CN: never a report target.
  {auth_index: 'c1', nickname: 'CN-1', uid: 'u6', region: 'cn', plan: 'free', checkin: {active: true, today_checked_in: false, streak_days: 3}},
];

const stub = `<script>
window.__reportCalls=[];
window.fetch = function(url,opts){
  var u=String(url), body={};
  if(opts&&opts.method==='POST'&&u.indexOf('/dailybonus')>=0){
    var idx='';
    try{ idx=(JSON.parse(opts.body||'{}').auth_index)||''; }catch(e){}
    var rec={start:Date.now(), auth_index:idx};
    window.__reportCalls.push(rec);
    window.__inflight=(window.__inflight||0)+1;
    window.__maxInflight=Math.max(window.__maxInflight||0, window.__inflight);
    body={results:[{auth_index:idx, day:'2026-10-14', lit:true, score:2, status:'reported'}]};
    return Promise.resolve({status:200,ok:true,json:function(){window.__inflight--;rec.end=Date.now();return Promise.resolve(body);},text:function(){window.__inflight--;rec.end=Date.now();return Promise.resolve(JSON.stringify(body));}});
  }
  else if(u.indexOf('/accounts')>=0||u.indexOf('/refresh')>=0) body=${JSON.stringify({accounts: accounts.map(a => { const c = {...a}; delete c.credits; return c; })})};
  else if(u.indexOf('/credits')>=0) body={accounts:[]};
  else if(u.indexOf('/usage/summary')>=0) body={accounts:[]};
  else if(u.indexOf('/concurrency')>=0) body={limit:2,slots:{}};
  else if(u.indexOf('/usage')>=0) body={records:[],totals:{}};
  return Promise.resolve({status:200,ok:true,json:function(){return Promise.resolve(body);},text:function(){return Promise.resolve(JSON.stringify(body));}});
};
try{sessionStorage.setItem('workbuddy-mgmt-key','pacing');}catch(e){}
</script>`;

// Kick off Report all and sample the call count over time to prove the walk is
// serial: the count must climb one at a time, never jump to all four at once.
const probe = `<script>window.addEventListener('load',()=>{setTimeout(()=>{
  const btn=document.querySelector('button[onclick*="reportAll"]');
  if(!btn){ const pre=document.createElement('pre');pre.id='probe';pre.textContent=JSON.stringify({error:'reportAll button not found'});document.body.appendChild(pre); return; }
  const samples=[];
  const started=Date.now();
  btn.click();
  const timer=setInterval(()=>{
    samples.push({ms:Date.now()-started, calls:(window.__reportCalls||[]).length});
  },250);
  setTimeout(()=>{
    clearInterval(timer);
    const calls=window.__reportCalls||[];
    const gaps=[];
    for(let i=1;i<calls.length;i++) gaps.push(calls[i].start-calls[i-1].start);
    const pre=document.createElement('pre');pre.id='probe';
    pre.textContent=JSON.stringify({
      total:calls.length,
      order:calls.map(c=>c.auth_index),
      gaps,
      maxInflight:window.__maxInflight||0,
      elapsed:Date.now()-started,
    },null,1);
    document.body.appendChild(pre);
  },9000);
},900)});</script>`;

const dir = mkdtempSync(join(tmpdir(), 'report-pacing-'));
const file = join(dir, 'pacing.html');
writeFileSync(file, panel.replace('<script>', stub + '<script>', 1).replace('</body>', probe + '</body>'));

const dom = execFileSync(chrome, ['--headless', '--disable-gpu', '--no-sandbox', '--window-size=1600,1000',
  '--virtual-time-budget=20000', '--dump-dom', `file://${file}`], {encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore']});

const m = dom.match(/<pre id="probe">([\s\S]*?)<\/pre>/);
if (!m) {
  console.error('report-all pacing check: no measurement output');
  process.exit(1);
}
const out = JSON.parse(m[1].replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&#39;/g, "'"));

let failures = 0;
const check = (label, ok, detail) => {
  if (ok) { console.log(`ok    ${label}`); return; }
  failures++;
  console.error(`FAIL  ${label}${detail === undefined ? '' : ' → ' + JSON.stringify(detail)}`);
};

if (out.error) {
  console.error('report-all pacing check:', out.error);
  process.exit(1);
}

const targets = ['g1', 'g2', 'g3', 'g4'];
check('every unlit Global account is reported, in order', out.order.join(',') === targets.join(','), out.order);
check('lit Global and CN accounts are skipped', !out.order.includes('g5') && !out.order.includes('c1'), out.order);
check('one request per account (no fan-out)', out.total === targets.length, out.total);
// The heart of it: never more than one request in flight at a time.
check('calls arrive one at a time, never as a batch', out.maxInflight === 1, {maxInflight: out.maxInflight});
const minGap = Math.min(...out.gaps);
check('there is a real pause between accounts (>=1s)', minGap >= 1000, out.gaps);
check('the walk takes roughly targets x gap', out.elapsed >= 4500, out.elapsed);

if (failures) {
  console.error(`\nreport-all pacing check: ${failures} failure(s)`);
  process.exit(1);
}
console.log(`\nreport-all pacing check: all good (${out.total} accounts, gaps ${out.gaps.join('/')}ms)`);
