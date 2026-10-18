// Guards the Usage-card hash-tracing toggle wiring.
//
// The toggle is the only way to flip cache tracing at runtime, and it talks to
// two routes that must stay in sync with management.go: GET /cachetrace/config
// (mirror the persisted state) and POST /cachetrace/config (flip + persist).
// A silent rename on either side would leave the checkbox rendering but doing
// nothing, so assert the wiring instead of just the markup.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');

test('the usage card exposes a wired hash-tracing toggle', () => {
  assert.match(
    html,
    /<input type="checkbox" id="traceToggle" onchange="toggleTrace\(this\.checked\)">/,
    'the trace toggle input must call toggleTrace with the checkbox state',
  );
  assert.match(html, /data-i18n="哈希追踪"/, 'the toggle label must be translatable');
  assert.match(html, /loadTraceStatus\(\);/, 'initUsage must mirror the persisted switch into the checkbox');
  assert.match(html, /api\("\/cachetrace\/config"\)/, 'status fetch must target GET /cachetrace/config');
  assert.match(
    html,
    /api\("\/cachetrace\/config",\{method:"POST"/,
    'the toggle must POST /cachetrace/config',
  );
  assert.match(html, /async function toggleTrace\(on\)/, 'toggleTrace must stay reachable from the checkbox');
});

test('the no-trace cell separates the write count from the label', () => {
  // "201.1K / 0No hash" read as "cache read 0" once; the muted label must carry
  // its own separator so the cache-write zero can never bleed into it.
  assert.match(
    html,
    /\} \/ \$\{fmtTok\(r\.cache_write_tokens\)\}\$\{usageCacheTraceCell\(r,recs\)\}/,
    'the cache read/write cell must still append the trace cell after the write count',
  );
  assert.match(
    html,
    /'<span class="muted" title="'\+esc\(tr\("No fingerprint in this record/,
    'the fallback cell must render the muted label with an explanatory tooltip',
  );
  assert.match(html, /'"> · '\+esc\(tr\("hash tracing off"\)\)/, 'the fallback label needs a separator before it');
});
