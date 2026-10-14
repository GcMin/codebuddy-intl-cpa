// Layout regression check for the account cards.
//
// The card header is a flex row: nickname on the left, badge strip on the
// right. Without `min-width:0` on the name, the badge strip cannot shrink and
// is pushed past the card edge — a long unbreakable nickname hid the LAST
// badge (the live concurrency one) underneath the neighbouring card. Measured
// before the fix: h2.scrollWidth 373 vs clientWidth 348, badge 25px outside the
// card. This script renders the real card() + real CSS in headless Chrome and
// asserts every badge stays inside its card.
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

// Slice the pieces the card renderer needs, by marker rather than line number
// so the check survives unrelated edits above.
function slice(startMarker, endMarker){
  const i = html.indexOf(startMarker);
  if (i < 0) throw new Error(`panel.html: marker not found: ${startMarker}`);
  const j = html.indexOf(endMarker, i);
  if (j < 0) throw new Error(`panel.html: end marker not found: ${endMarker}`);
  return html.slice(i, j);
}

const renderer = [
  slice('const PANEL_EN = {', 'let panelLanguage'),
  slice('let panelLanguage', 'const PANEL_ZH_NOTICES'),
  slice('const PANEL_ZH_NOTICES', '\nfunction tr('),
  slice('function tr(', 'function translateStatic'),
  slice('function esc(', 'function progressHTML('),
  slice('function progressHTML(', 'function card('),
  slice('function card(', '\nlet currentFilter'),
].join('\n');

// Realistic worst cases: the account that actually lost its badge (17-char
// nickname, no hyphen, four badges), a deliberately absurd one, and a short
// name that must not be needlessly ellipsized.
const fixtures = [
  {auth_index:'1', nickname:'Intl-thomashallcodeevj', plan:'free', region:'global', exhausted:true, concurrency_known:true, concurrency_current:0, concurrency_limit:2},
  {auth_index:'2', nickname:'Intl-'+'x'.repeat(90), plan:'pro', region:'global', exhausted:true, disabled:true, concurrency_known:true, concurrency_current:2, concurrency_limit:2},
  {auth_index:'3', nickname:'Intl-a', plan:'trial', region:'cn', concurrency_known:true, concurrency_current:1, concurrency_limit:2},
];

const css = slice('<style>', '</style>');
const measure = `
window.onerror=function(m,s,l){document.getElementById('out').textContent='JSERROR: '+m+' @'+l;};
try{
  document.getElementById('grid').innerHTML = ACCOUNTS.map(card).join("");
  var rows=[];
  document.querySelectorAll('#grid .card').forEach(function(c){
    var h2=c.querySelector('h2'), badge=c.querySelector('[data-conc]'), name=c.querySelector('.cname');
    var cr=c.getBoundingClientRect(), cs=getComputedStyle(c);
    var innerRight=cr.right-parseFloat(cs.paddingRight);
    var row={nickname:(name?name.getAttribute('title'):''), hasName:!!name,
             h2Scroll:h2.scrollWidth, h2Client:h2.clientWidth,
             badgeInsideStrip: !!(badge && badge.closest('.cbadges'))};
    if(badge){var b=badge.getBoundingClientRect(); row.badgeText=badge.textContent; row.overflowPx=Math.round(b.right-innerRight); row.visible=b.right<=innerRight+0.5;}
    else {row.badgeText='MISSING'; row.visible=false;}
    rows.push(row);
  });
  document.getElementById('out').textContent=JSON.stringify(rows);
}catch(e){document.getElementById('out').textContent='CATCH: '+e.message;}
`;

const harness = `<!doctype html><html><head><meta charset="utf-8"><style>
${css}
body{background:#111;margin:0;padding:16px}
</style></head><body>
<div id="grid" class="grid"></div>
<pre id="out"></pre>
<script>
${renderer}
var ACCOUNTS = ${JSON.stringify(fixtures)};
${measure}
</script></body></html>`;

const chrome = chromeBinary();
if (!chrome) {
  console.log('panel card overflow check: skipped (no Chrome/Chromium found; set CHROME=/path/to/chrome)');
  process.exit(0);
}

const dir = mkdtempSync(join(tmpdir(), 'panel-overflow-'));
const file = join(dir, 'harness.html');
writeFileSync(file, harness);

const dom = execFileSync(chrome, [
  '--headless', '--disable-gpu', '--no-sandbox', '--window-size=1200,900',
  '--virtual-time-budget=4000', '--dump-dom', `file://${file}`,
], {encoding:'utf8', stdio:['ignore', 'pipe', 'ignore']});

const match = dom.match(/<pre id="out">([\s\S]*?)<\/pre>/);
if (!match) {
  console.error('panel card overflow check: harness produced no measurement output');
  process.exit(1);
}
const raw = match[1]
  .replace(/&quot;/g, '"').replace(/&#39;/g, "'")
  .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&')
  .trim();
if (raw.startsWith('JSERROR') || raw.startsWith('CATCH')) {
  console.error('panel card overflow check: harness error:', raw.slice(0, 400));
  process.exit(1);
}

const rows = JSON.parse(raw);
let failures = 0;
for (const row of rows) {
  const problems = [];
  if (!row.hasName) problems.push('nickname is not wrapped in .cname');
  if (!row.badgeInsideStrip) problems.push('concurrency badge is not inside .cbadges');
  if (row.badgeText === 'MISSING') problems.push('concurrency badge not rendered');
  if (!row.visible) problems.push(`badge overflows the card by ${row.overflowPx}px`);
  if (row.h2Scroll > row.h2Client) problems.push(`header overflows (${row.h2Scroll} > ${row.h2Client})`);
  if (problems.length) {
    failures++;
    console.error(`FAIL  ${row.nickname.slice(0, 40)}: ${problems.join('; ')}`);
  } else {
    console.log(`ok    ${row.nickname.slice(0, 40)} badge=${row.badgeText} overflow=${row.overflowPx}px`);
  }
}

if (failures) {
  console.error(`\npanel card overflow check: ${failures} card(s) hide a badge`);
  process.exit(1);
}
console.log('\npanel card overflow check: all badges visible');
