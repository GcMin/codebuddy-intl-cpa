const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');

function slice(start, end) {
  const i = html.indexOf(start);
  const j = html.indexOf(end, i);
  if (i < 0 || j < 0) throw new Error(`marker not found: ${start} / ${end}`);
  return html.slice(i, j);
}

// Real helpers from the panel, plus the table module under test.
const escSrc = slice('function esc(', 'function planLabel(');
const planLabelSrc = slice('function planLabel(', 'function progressHTML(');
const creditOfSrc = slice('function creditOf(', 'function renderSummary(');
const acctFilterSrc = slice('function accountsForFilter(', 'function creditOf(');
const tableSrc = slice('const acctView = {', 'function renderAccounts()');
const lazySrc = slice('function rowNeedsFetch(', 'function lazyLoadCredits(');

const ctx = {
  tr: s => s,
  fmtCred: n => String(n),
  fmtTok: n => String(n),
  lastAccounts: [],
  currentFilter: 'all',
  CSS: {escape: s => s},
};
vm.createContext(ctx);
vm.runInContext(escSrc + planLabelSrc + acctFilterSrc + creditOfSrc + tableSrc + lazySrc, ctx);

// `const` declarations inside a VM script are script-scoped, not properties of
// the context object, so pull the state object out explicitly.
const acctView = vm.runInContext('acctView', ctx);

const gateKey = 'global:95e6659d-2f49-45a1-8abb-be3f7cfd8c28';

// ---- package classification ----------------------------------------------------
test('packTag classifies by shape, not by name (upstream calls both "Bonus Pack")', () => {
  const {packTag} = ctx;
  assert.equal(packTag({name: 'Bonus Pack', size: 250}).t, '注册 250');
  assert.equal(packTag({name: 'Free Plan Subscription', size: 100}).cls, 'rec');
  assert.equal(packTag({name: 'CodeBuddy个人体验版', size: 500}).cls, 'rec');
  assert.equal(packTag({name: 'Bonus Pack', size: 30}).t, '每日 30');
  assert.equal(packTag({name: 'CodeBuddy个人版国内运营裂变包', size: 100}).t, '裂变');
  assert.equal(packTag({name: 'Mystery', size: 7}).t, '一次性');
});

// ---- expiry countdown ----------------------------------------------------------
test('fmtLeft renders days/hours/minutes, warns under 2 days, expires in the past', () => {
  const {fmtLeft} = ctx;
  const at = ms => new Date(Date.now() + ms).toISOString();
  // Tolerate the millisecond that passes between building the timestamp and
  // formatting it: assert the shape and the day bucket, not an exact minute.
  assert.match(fmtLeft(at(3 * 86400000 + 3600000)).t, /^3d \d+h \d+m$/);
  assert.equal(fmtLeft(at(3 * 86400000)).cls, '');
  assert.equal(fmtLeft(at(86400000)).cls, 'soon');       // 1 day out
  assert.equal(fmtLeft(at(-1000)).t, '已过期');
  assert.equal(fmtLeft(at(-1000)).cls, 'out');
  assert.equal(fmtLeft('').t, '—');
  assert.equal(fmtLeft('not-a-date').t, '—');
});

// ---- daily report vs check-in ---------------------------------------------------
test('dailyOf: CN uses check-in, Global uses the growth report, unknown stays unknown', () => {
  const {dailyOf} = ctx;
  // CN
  assert.equal(dailyOf({region: 'cn', checkin: {active: true, today_checked_in: true, streak_days: 13}}).label, '已签到');
  assert.equal(dailyOf({region: 'cn', checkin: {active: true, today_checked_in: false, streak_days: 3}}).label, '未签到');
  assert.equal(dailyOf({region: 'cn', checkin: {active: false}}).label, '未启用');
  // Global — lit/score come from the heatmap, status from our own attempt.
  assert.equal(dailyOf({region: 'global', daily: {lit: true, score: 2}}).label, '已点亮');
  assert.match(dailyOf({region: 'global', daily: {lit: true, score: 2, lit_days: ['a', 'b', 'c']}}).sub, /2/);
  assert.equal(dailyOf({region: 'global', daily: {lit: false, status: 'not-lit', last_lit: '2026-10-13'}}).label, '未点亮');
  assert.match(dailyOf({region: 'global', daily: {lit: false, last_lit: '2026-10-13'}}).sub, /2026-10-13/);
  assert.equal(dailyOf({region: 'global', daily: {lit: false, status: 'failed', detail: 'heatmap: timeout'}}).label, '报告失败');
  // A failed attempt that still ended up lit must not read as a failure.
  assert.equal(dailyOf({region: 'global', daily: {lit: true, score: 2, status: 'failed'}}).label, '已点亮');
  // No run recorded yet must NOT be reported as "not reported".
  const unknown = dailyOf({region: 'global'});
  assert.equal(unknown.label, '—');
  assert.equal(unknown.sub, '暂无报告记录');
});

test('dailyOf ranks put reported first and unknown last (sort order)', () => {
  const {dailyOf} = ctx;
  const ranks = [
    dailyOf({region: 'global', daily: {lit: true, score: 2}}).rank,
    dailyOf({region: 'global', daily: {lit: false, status: 'reported'}}).rank,
    dailyOf({region: 'global', daily: {status: 'failed'}}).rank,
    dailyOf({region: 'global'}).rank,
  ];
  assert.deepEqual(ranks, [0, 1, 3, 4]);
});

// ---- row rendering -------------------------------------------------------------
test('rowHTML uses the gate key for the concurrency badge and shows all columns', () => {
  const {rowHTML} = ctx;
  const out = rowHTML({
    auth_index: '0ffe10a82d6cfd07', concurrency_key: gateKey, nickname: 'Intl-x', uid: 'u-1',
    region: 'global', plan: 'trial', concurrency_current: 1, concurrency_limit: 2, concurrency_known: true,
    credits: {total_remain: 345, total_used: 425, total_size: 770, packages: [{size: 250, remain: 0}]},
    daily: {lit: true, score: 2},
  });
  assert.match(out, new RegExp(`data-conc="${gateKey.replace(/:/g, ':')}"`));
  assert.doesNotMatch(out, /data-conc="0ffe10a82d6cfd07"/);
  assert.match(out, /data-region="global"/);
  assert.match(out, /345/);            // credits remain
  assert.match(out, /770/);            // credits size
  assert.match(out, /已点亮/);          // daily pill (lit)
  assert.match(out, /acct-row/);
  assert.match(out, /class="chev/);
});

test('rowHTML escapes account data and survives missing fields', () => {
  const {rowHTML} = ctx;
  const out = rowHTML({auth_index: 'a"b', nickname: '<img src=x>', uid: '"><script>', region: 'cn'});
  assert.doesNotMatch(out, /<img/);
  assert.doesNotMatch(out, /<script>/);
  assert.match(out, /class="acct-row/);
});

// ---- filtering and sorting -----------------------------------------------------
test('acctFiltered searches name/uid/region/plan and sorts by the selected key', () => {
  ctx.lastAccounts = [
    {auth_index: 'a', nickname: 'Intl-gary', uid: 'uid-aaa', region: 'global', plan: 'trial',
     credits: {total_remain: 0, total_used: 740, total_size: 740}, daily: {lit: false, status: 'reported'}},
    {auth_index: 'b', nickname: 'CN-64426370', uid: 'uid-bbb', region: 'cn', plan: 'free',
     credits: {total_remain: 1970, total_used: 3630, total_size: 5600}, checkin: {active: true, today_checked_in: true}},
    {auth_index: 'c', nickname: 'Intl-laura', uid: 'uid-ccc', region: 'global', plan: 'trial',
     credits: {total_remain: 599, total_used: 141, total_size: 740}, daily: {lit: true, score: 2}},
  ];
  const {acctFiltered} = ctx;

  acctView.q = '';
  acctView.sort = {k: 'remain', dir: -1};
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b', 'c', 'a']);

  acctView.sort = {k: 'name', dir: 1};
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b', 'a', 'c']); // CN-, Intl-gary, Intl-laura

  acctView.sort = {k: 'report', dir: 1};   // reported(0) < not reported(1)
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b', 'c', 'a']);

  acctView.q = 'laura';
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['c']);
  acctView.q = 'uid-bbb';
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b']);
  acctView.q = 'cn';
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b']);
  acctView.q = '';
});

test('acctFiltered honours the region filter chips', () => {
  ctx.lastAccounts = [
    {auth_index: 'a', nickname: 'g', region: 'global'},
    {auth_index: 'b', nickname: 'c', region: 'cn'},
  ];
  const {acctFiltered} = ctx;
  acctView.q = ''; acctView.sort = {k: 'name', dir: 1};
  ctx.currentFilter = 'cn';
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['b']);
  ctx.currentFilter = 'global';
  assert.deepEqual(acctFiltered().map(a => a.auth_index), ['a']);
  ctx.currentFilter = 'all';
  assert.equal(acctFiltered().length, 2);
});

// ---- detail rows ---------------------------------------------------------------
test('detailHTML lists packages expiring first and includes the CN check-in block', () => {
  const {detailHTML} = ctx;
  const out = detailHTML({
    auth_index: 'b', uid: 'uid-bbb', region: 'cn',
    credits: {total_remain: 100, total_used: 100, total_size: 200, packages: [
      {name: 'later', size: 30, remain: 30, cycle_start: '2026-10-10 00:00:00', cycle_end: '2026-12-01 00:00:00'},
      {name: 'sooner', size: 30, remain: 0, cycle_start: '2026-10-01 00:00:00', cycle_end: '2026-10-20 00:00:00'},
    ]},
    checkin: {active: true, today_checked_in: true, streak_days: 13, daily_credit: 100, today_credit: 100, total_credits: 1300, activity_name: 'Buddy应用'},
  });
  assert.ok(out.indexOf('sooner') < out.indexOf('later'), 'packages must be expiring-first');
  assert.match(out, /data-exp="2026-10-20 00:00:00"/);
  assert.match(out, /签到/);
  assert.match(out, /13/);
  assert.match(out, /Buddy应用/);
  assert.match(out, /detail-row/);
});

test('detailHTML says so explicitly when there is no package data', () => {
  const {detailHTML} = ctx;
  const out = detailHTML({auth_index: 'x', region: 'global'});
  assert.match(out, /暂无积分包数据/);
  assert.doesNotMatch(out, /pack-tbl/);
});

// ---- manual report button ------------------------------------------------------
// The row keeps only what changes today's state: Report for Global, check-in for
// CN. Per-row Refresh is gone (refreshing is a bulk action on selected rows), and
// a lit day shows a disabled "Lit" button instead of a pointless Report.
test('Global rows show Report while unlit and a disabled Lit button once lit', () => {
  const {rowHTML} = ctx;
  const base = {auth_index: 'a1', nickname: 'G', uid: 'u', region: 'global', plan: 'trial'};

  const unlit = rowHTML({...base, daily: {lit: false, status: 'not-lit', last_lit: '2026-10-13'}});
  assert.match(unlit, /data-action="report"/);
  assert.match(unlit, /未点亮/);

  const lit = rowHTML({...base, daily: {lit: true, score: 2}});
  assert.doesNotMatch(lit, /data-action="report"/);   // nothing to do
  assert.match(lit, /已点亮/);                          // disabled state instead
  assert.match(lit, /disabled/);

  // A failed attempt that is still unlit keeps the button (retry is meaningful).
  const failed = rowHTML({...base, daily: {lit: false, status: 'failed', detail: 'heatmap: timeout'}});
  assert.match(failed, /data-action="report"/);
  assert.match(failed, /报告失败/);

  // Unknown state: report is still offered (it is idempotent and reads the
  // heatmap first), so an operator can act without refreshing first.
  const unknown = rowHTML({...base});
  assert.match(unknown, /data-action="report"/);
  assert.match(unknown, /暂无报告记录/);

  // CN never gets the report button; its reward is check-in.
  const cn = rowHTML({auth_index: 'c1', nickname: 'C', uid: 'u', region: 'cn',
    checkin: {active: true, today_checked_in: false, streak_days: 3}});
  assert.doesNotMatch(cn, /data-action="report"/);
  assert.match(cn, /data-action="checkin"/);
});

test('rows no longer carry a per-row refresh action', () => {
  const {rowHTML} = ctx;
  const global = rowHTML({auth_index: 'a1', nickname: 'G', uid: 'u', region: 'global',
    daily: {lit: false, status: 'not-lit'}, credits: {total_remain: 1, total_size: 2}});
  const cn = rowHTML({auth_index: 'c1', nickname: 'C', uid: 'u', region: 'cn',
    checkin: {active: true, today_checked_in: false}});
  assert.doesNotMatch(global, /data-action="refresh"/);
  assert.doesNotMatch(cn, /data-action="refresh"/);
});

test('a failed report shows the reason, not a bare "not reported"', () => {
  const {dailyOf} = ctx;
  const st = dailyOf({region: 'global', daily: {lit: false, status: 'failed', detail: 'heatmap: timeout'}});
  assert.equal(st.label, '报告失败');
  assert.equal(st.sub, 'heatmap: timeout');
  assert.equal(st.pill, 'pill-err');
  // rank 3 keeps failures sorted after pending and before unknown
  assert.equal(st.rank, 3);
});

// ---- table layout contract ------------------------------------------------------
// Cells must not wrap. With wrapping on, the Region badges broke onto a second
// line at narrower widths (the panel lives inside the CPA iframe), which made
// rows 54px tall in one place and 70px in another — the "not tidy" complaint.
// Long content is truncated by .nm-wrap / .pack-name instead.
test('table cells are nowrap and long content truncates instead of wrapping', () => {
  const css = html.slice(html.indexOf('<style>'), html.indexOf('</style>'));
  const tdRule = css.match(/\.acct-table td\{[^}]*\}/);
  assert.ok(tdRule, '.acct-table td rule missing');
  assert.match(tdRule[0], /white-space:nowrap/, 'table cells must not wrap');
  assert.match(css, /\.nm-wrap\{[^}]*max-width/);
  assert.match(css, /\.nm-main\{[^}]*text-overflow:ellipsis/);
});

// ---- lazy load coverage ---------------------------------------------------------
// A row is only complete with its daily piece. Fetching just the rows without
// credits left a warm-cached CN account showing "no check-in data" forever, even
// though one request would have filled it (that is exactly what the operator
// saw: a CN row with credits but "-" for Daily).
test('rowNeedsFetch asks for the daily piece, not only credits', () => {
  const {rowNeedsFetch} = ctx;

  // CN with credits but no check-in: the reported bug.
  assert.equal(rowNeedsFetch({region: 'cn', credits: {total_remain: 1}, checkin: undefined}), true);
  // CN complete.
  assert.equal(rowNeedsFetch({region: 'cn', credits: {total_remain: 1}, checkin: {active: true}}), false);

  // Global with credits but no growth-day state.
  assert.equal(rowNeedsFetch({region: 'global', credits: {total_remain: 1}, daily: undefined}), true);
  assert.equal(rowNeedsFetch({region: 'global', credits: {total_remain: 1}, daily: {lit: true}}), false);

  // No credits at all: still needs a fetch regardless of region.
  assert.equal(rowNeedsFetch({region: 'global', daily: {lit: true}}), true);
  assert.equal(rowNeedsFetch({region: 'cn', checkin: {active: true}}), true);

  // Never re-fetch rows that errored or are disabled.
  assert.equal(rowNeedsFetch({region: 'global', error: 'boom'}), false);
  assert.equal(rowNeedsFetch({region: 'cn', disabled: true}), false);
});
