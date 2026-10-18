// Layout regression check for the usage table.
//
// The table renders nowrap cells (model ids, the cache-hash sub-line, account
// nicknames) and used to lay itself out automatically, so a long row grew the
// table past `.tbl-wrap` and every row got a horizontal scrollbar. The fix is
// `table-layout:fixed` plus proportional column widths; this check renders the
// real CSS with deliberately wide fixtures and asserts the wrapper never scrolls
// sideways.
//
// Chrome is optional: without it the check reports "skipped" and exits 0 so it
// can run in CI images that have no browser. `CHROME` overrides the binary.
import {execFileSync} from 'node:child_process';
import {existsSync, mkdtempSync, readFileSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname, join} from 'node:path';
import {fileURLToPath} from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '../workbuddy/panel.html'), 'utf8');

function chromeBinary(){
  if (process.env.CHROME) return process.env.CHROME;
  for (const name of ['google-chrome', 'chromium', 'chromium-browser', 'google-chrome-stable']) {
    for (const dir of (process.env.PATH || '').split(':')) {
      const candidate = join(dir, name);
      if (dir && existsSync(candidate)) return candidate;
    }
  }
  return '';
}

function slice(startMarker, endMarker){
  const i = html.indexOf(startMarker);
  if (i < 0) throw new Error(`panel.html: marker not found: ${startMarker}`);
  const j = html.indexOf(endMarker, i);
  if (j < 0) throw new Error(`panel.html: end marker not found: ${endMarker}`);
  return html.slice(i, j);
}

const css = slice('<style>', '</style>');
const header = slice('<table class="utbl">', '<tbody id="usageBody">');

// Worst cases: the fail row the tier-gating test produced (long nickname + fail
// badge), a long model id with an alias, and a cache cell whose sub-line is the
// widest thing in the table.
const rows = [
  {
    time: '2026-10-18 22:00', account: 'Intl-thomashallcodeevj', failed: true,
    model: 'gpt-6.1-sol', alias: 'cb/gpt-6.1-sol', in: '0', out: '0', cr: '0', cw: '0', total: '0', credit: '0',
    trace: true,
  },
  {
    time: '2026-10-18 21:57', account: 'Intl-gary-martinfzr', failed: false,
    model: 'glm-5.3', alias: 'cb/glm-5.3', in: '20', out: '4', cr: '18', cw: '0', total: '24', credit: '0.01',
    trace: true,
  },
  {
    time: '2026-10-18 21:40', account: 'CN-64426370', failed: false,
    model: 'deepseek-v4.1-flash', alias: 'cn/deepseek-v4.1-flash', in: '184614', out: '1097',
    cr: '184611', cw: '531', total: '186242', credit: '46.2', trace: false,
  },
];

const rowHTML = (r) => {
  const fail = r.failed ? '<span class="badge exhausted" style="margin-left:6px">失败</span>' : '';
  const model = `${r.model}${r.alias && r.alias !== r.model ? ` <span class="muted">(${r.alias})</span>` : ''}`;
  const cacheSub = r.trace
    ? '<details class="cache-trace"><summary>Cache hash · 0.0% · low hit · failover</summary><pre>x</pre></details>'
    : '<span class="muted"> · hash tracing off</span>';
  return `<tr><td>${r.time}</td><td>${r.account}${fail}</td><td>${model}</td>` +
    `<td class="num">${r.in}</td><td class="num">${r.out}</td>` +
    `<td class="num">${r.cr} / ${r.cw}${cacheSub}</td>` +
    `<td class="num">${r.total}</td><td class="num cr">${r.credit}</td></tr>`;
};

const measure = `
window.onerror=function(m,s,l){document.getElementById('out').textContent='JSERROR: '+m+' @'+l;};
try{
  var body=document.getElementById('usageBody');
  body.innerHTML=ROWS.map(ROW_HTML).join("");
  var out=[];
  document.querySelectorAll('.tbl-wrap').forEach(function(w){
    var t=w.querySelector('table');
    var wide=0, widest='';
    t.querySelectorAll('tbody tr:first-child td').forEach(function(td,i){
      var sw=td.scrollWidth, cw=td.clientWidth;
      if(sw-cw>wide){wide=sw-cw; widest='col'+(i+1)+' scroll='+sw+' client='+cw;}
    });
    out.push({wrapClient:w.clientWidth, wrapScroll:w.scrollWidth, tableScroll:t.scrollWidth,
              overflowPx:w.scrollWidth-w.clientWidth, widestCell:widest});
  });
  document.getElementById('out').textContent=JSON.stringify(out);
}catch(e){document.getElementById('out').textContent='CATCH: '+e.message;}
`;

const harness = `<!doctype html><html><head><meta charset="utf-8"><style>
${css}
/* Two widths: the panel's usual content width, and a narrow window where a
   fixed layout is most likely to push the table out of its wrapper. */
.host{background:#111;padding:16px}
.host.narrow{width:640px}
.tbl-wrap{max-height:none}
</style></head><body>
<div class="host" id="h1"><div class="tbl-wrap"><table class="utbl">${header}<tbody id="usageBody"></tbody></table></div></div>
<div class="host narrow"><div class="tbl-wrap"><table class="utbl">${header}<tbody id="usageBody2"></tbody></table></div></div>
<pre id="out"></pre>
<script>
var ROWS=${JSON.stringify(rows)};
var ROW_HTML=${rowHTML.toString()};
try{
  document.getElementById('usageBody2').innerHTML=ROWS.map(ROW_HTML).join("");
  var out=[];
  document.querySelectorAll('.tbl-wrap').forEach(function(w){
    var t=w.querySelector('table');
    var wide=0, widest='';
    t.querySelectorAll('tbody tr:first-child td').forEach(function(td,i){
      var sw=td.scrollWidth, cw=td.clientWidth;
      if(sw-cw>wide){wide=sw-cw; widest='col'+(i+1)+' scroll='+sw+' client='+cw;}
    });
    out.push({wrapClient:w.clientWidth, wrapScroll:w.scrollWidth, tableScroll:t.scrollWidth,
              overflowPx:w.scrollWidth-w.clientWidth, widestCell:widest});
  });
  document.getElementById('out').textContent=JSON.stringify(out);
}catch(e){document.getElementById('out').textContent='CATCH: '+e.message;}
</script></body></html>`;

const chrome = chromeBinary();
if (!chrome) {
  console.log('panel usage overflow check: skipped (no Chrome/Chromium found; set CHROME=/path/to/chrome)');
  process.exit(0);
}

const dir = mkdtempSync(join(tmpdir(), 'panel-usage-overflow-'));
const file = join(dir, 'harness.html');
writeFileSync(file, harness);

const dom = execFileSync(chrome, [
  '--headless', '--disable-gpu', '--no-sandbox', '--window-size=1200,900',
  '--virtual-time-budget=4000', '--dump-dom', `file://${file}`,
], {encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore']});

const match = dom.match(/<pre id="out">([\s\S]*?)<\/pre>/);
if (!match) {
  console.error('panel usage overflow check: harness produced no measurement output');
  process.exit(1);
}
const raw = match[1]
  .replace(/&quot;/g, '"').replace(/&#39;/g, "'")
  .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&')
  .trim();
if (raw.startsWith('JSERROR') || raw.startsWith('CATCH')) {
  console.error('panel usage overflow check: harness error:', raw.slice(0, 400));
  process.exit(1);
}

const results = JSON.parse(raw);
let failures = 0;
results.forEach((r, i) => {
  const label = i === 0 ? 'panel width' : 'narrow width';
  if (r.overflowPx > 1) {
    failures++;
    console.error(`FAIL  ${label}: table scrolls horizontally by ${r.overflowPx}px (wrap ${r.wrapScroll} > ${r.wrapClient}); widest ${r.widestCell}`);
  } else {
    console.log(`ok    ${label}: no horizontal scroll (wrap ${r.wrapClient}px, overflow ${r.overflowPx}px)`);
  }
});

if (failures) {
  console.error(`\npanel usage overflow check: ${failures} width(s) force a horizontal scrollbar`);
  process.exit(1);
}
console.log('\npanel usage overflow check: table fits its wrapper');
