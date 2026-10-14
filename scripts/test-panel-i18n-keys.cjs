// Guards the panel's English translation table.
//
// The table is a JS object literal, so a duplicate key is not an error — the
// last one silently wins and the earlier value is discarded. That is how the
// daily-bonus toast shipped as "daily bonesenabled ✅": the table concatenates
// fragments ("每日奖励" + "已开启 ✅") and relies on the trailing space in
// "Daily bonus ", which a later duplicate "daily bonuses" removed.
//
// Both checks below fail on that exact regression.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../workbuddy/panel.html'), 'utf8');

// Reads the PANEL_EN object literal out of panel.html and returns both its
// entries in source order and the effective map (last duplicate wins, as the
// JS engine sees it).
function panelEnglish() {
  const marker = html.indexOf('const PANEL_EN');
  assert.ok(marker > 0, 'panel.html no longer declares PANEL_EN — update this test');
  const open = html.indexOf('{', marker);
  let depth = 0;
  let end = open;
  for (; end < html.length; end += 1) {
    if (html[end] === '{') depth += 1;
    else if (html[end] === '}') {
      depth -= 1;
      if (depth === 0) break;
    }
  }
  const body = html.slice(open, end + 1);

  const pair = /^\s*"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"\s*,?\s*$/gm;
  const entries = [];
  let match;
  while ((match = pair.exec(body)) !== null) {
    entries.push({key: match[1], value: match[2]});
  }
  assert.ok(entries.length > 300, `expected a populated translation table, parsed ${entries.length} entries`);

  const ctx = {module: {exports: {}}};
  vm.createContext(ctx);
  vm.runInContext(`module.exports = ${body};`, ctx);
  return {entries, table: ctx.module.exports};
}

test('PANEL_EN has no duplicate keys', () => {
  const {entries} = panelEnglish();
  const seen = new Map();
  const duplicates = [];
  for (const {key, value} of entries) {
    if (seen.has(key)) duplicates.push(`${JSON.stringify(key)} (${JSON.stringify(seen.get(key))} then ${JSON.stringify(value)})`);
    else seen.set(key, value);
  }
  assert.deepEqual(
    duplicates,
    [],
    'a duplicate key is silently ignored by the JS engine — remove one, or the first value never reaches the UI',
  );
});

test('the toggle toasts compose into readable English', () => {
  const {table} = panelEnglish();
  // panel.html builds these toasts by concatenating fragments, so the keys must
  // still join with a separator. "daily bonesenabled ✅" is the regression.
  const cases = [
    ['每日奖励', '已开启 ✅', 'Daily bonus enabled ✅'],
    ['每日奖励', '已关闭 ⏸', 'Daily bonus disabled ⏸'],
    ['自动签到', '已开启 ✅', 'Auto check-in enabled ✅'],
    ['自动签到', '已关闭 ⏸', 'Auto check-in disabled ⏸'],
    ['哈希追踪', '已开启 ✅', 'Hash tracing enabled ✅'],
    ['哈希追踪', '已关闭 ⏸', 'Hash tracing disabled ⏸'],
  ];
  for (const [left, right, expected] of cases) {
    const composed = `${table[left]}${table[right]}`;
    assert.equal(composed, expected, `${left} + ${right} reads as ${JSON.stringify(composed)}`);
  }
});

test('the credit-mix label reads as a single daily bonus', () => {
  const {table} = panelEnglish();
  // "<b>3</b>&times;30 Daily bonus" in the credit breakdown.
  assert.equal(`3×30 ${table['每日奖励']}`.trim(), '3×30 Daily bonus');
});
