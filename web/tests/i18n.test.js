// Checks translations and localized helpers: node web/tests/i18n.test.js (also run by go test ./web).
// - every entry has a French and an English value with the same {placeholders};
// - every key used by app.js (literal tr('…') calls and the dynamic prefixes) and index.html exists;
// - the pure helpers render in both languages.
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const WEB = path.join(__dirname, '..');
const I = require(path.join(WEB, 'i18n.js'));
const RT = require(path.join(WEB, 'app.js'));

const M = I.messages;
const problems = [];
const ph = (v) => (typeof v === 'string' ? (v.match(/\{\w+\}/g) || []).sort().join()
  : v && typeof v === 'object' && !Array.isArray(v) ? ph(v.one) + '|' + ph(v.other) : '');
for (const k of Object.keys(M)) {
  const e = M[k];
  if (!Array.isArray(e) || e.length !== I.LANGS.length || e.some((v) => v == null || v === '' && k !== '')) { problems.push('incomplete entry ' + k); continue; }
  if (ph(e[0]).replace(/\|/g, ',').split(',').filter(Boolean).sort().join() !== ph(e[1]).replace(/\|/g, ',').split(',').filter(Boolean).sort().join()) {
    problems.push('placeholders differ: ' + k);
  }
}

const app = fs.readFileSync(path.join(WEB, 'app.js'), 'utf8');
const html = fs.readFileSync(path.join(WEB, 'index.html'), 'utf8') + fs.readFileSync(path.join(WEB, 'setup.html'), 'utf8');
const wizard = fs.readFileSync(path.join(WEB, 'setup.js'), 'utf8');
const used = new Set();
for (const m of app.matchAll(/tr\('([A-Za-z0-9_.]+)'/g)) used.add(m[1]);
for (const m of app.matchAll(/'((?:st|mp|tbl|kpi|goal|goals|hist|match|res|pl|lb|dev|onb|act|sit|mental|time|mech|perf|hits|feed|nav|toast|common|footer|badge|ui|set|day|prog|unit|fmt|pb|confirm|filters|settings|wf|field|fatigue|focus|trn|live)\.[A-Za-z0-9_.]+)'/g)) used.add(m[1]);
for (const m of wizard.matchAll(/'(wiz\.[A-Za-z0-9_.]+)'/g)) used.add(m[1]);
for (const m of html.matchAll(/data-i18n(?:-html)?="([^"]+)"/g)) used.add(m[1]);
for (const m of html.matchAll(/data-i18n-attr="([^"]+)"/g)) m[1].split(';').forEach((p) => used.add(p.split(':')[1].trim()));
// Keys built at runtime.
['score', 'goals', 'assists', 'saves', 'shots', 'touches', 'demos'].forEach((k) => used.add('perf.' + k));
['win', 'loss', 'abandoned'].forEach((r) => { used.add('res.' + r); used.add('res.' + r + '.short'); });
RT.MECH_METRICS.forEach((m) => used.add('mech.' + m.key));
RT.PERF_STATS.concat(RT.MOVE_STATS, RT.HIT_STATS).forEach((s) => used.add(s.lk));
RT.TAGS.forEach((t) => used.add('tag.' + t));
RT.FOCUSES.forEach((f) => { used.add('focus.' + f); used.add('drill.' + f); });
RT.FACTORS.concat(RT.FATIGUE_STATS).forEach((f) => used.add(f.lk));
['match', 'game', 'day', 'player', 'goal', 'abandon', 'session'].forEach((u) => used.add('unit.' + u));
for (const k of used) if (!k.endsWith('.') && !I.has(k)) problems.push('missing key ' + k);

if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}

// Rendering in both languages.
I.setLang('fr');
assert.strictEqual(RT.plural(1, 'match'), '1 match');
assert.strictEqual(RT.plural(3, 'match'), '3 matchs');
assert.strictEqual(RT.fmtPct(0.5), '50 %');
assert.strictEqual(RT.tagLabel('ranked'), 'Classé');
assert.strictEqual(RT.prettyArena('EuroStadium_Night_P'), 'Mannfield (Nuit)');
I.setLang('en');
assert.strictEqual(RT.plural(1, 'match'), '1 match');
assert.strictEqual(RT.plural(3, 'match'), '3 matches');
assert.strictEqual(RT.fmtNum(1234.5, 1), '1,234.5');
assert.strictEqual(RT.fmtPct(0.5), '50%');
assert.strictEqual(RT.tagLabel('ranked'), 'Ranked');
assert.strictEqual(RT.prettyArena('EuroStadium_Night_P'), 'Mannfield (Night)');
assert.strictEqual(I.t('goals.decided', { n: 2, v: '2' }), '2 decided matches');
assert.strictEqual(I.t('nope.missing'), 'nope.missing');
// Routes: English, and the French ones of older versions still work.
assert.deepStrictEqual(RT.parseRoute('#/historique'), { view: 'history', id: null, player: null });
assert.deepStrictEqual(RT.parseRoute('#/joueur/Bob/match/3'), { view: 'match', id: 3, player: 'bob' });
assert.strictEqual(RT.routeHash('bob', 'history'), '#/player/bob/history');
console.log('i18n ok: ' + Object.keys(M).length + ' keys, ' + used.size + ' used');

// R2 dynamic duration labels, fixed destination and actionable timer recovery exist in both languages.
for (const lang of ['fr', 'en']) {
  I.setLang(lang);
  assert.ok(I.t('live.pace', { minutes: 3 }).includes('3'));
  assert.ok(!I.t('live.pace', { minutes: 3 }).includes('5'));
  assert.ok(I.t('rank.fixedTarget', { v: 'Synthetic account' }).includes('Synthetic account'));
  assert.ok(I.t('trn.timerBounds').includes('12') && I.t('trn.timerBounds').includes('4096'));
  assert.ok(I.t('trn.timerPrecision').includes('60') && I.t('trn.timerPrecision').includes('300'));
  assert.ok(I.t('trn.keptActivity', { v: 299 }).includes('299'));
  for (const key of ['trn.pendingSave', 'trn.retrySave', 'trn.tooShort', 'trn.legacyActivity', 'trn.planFoot', 'live.perfRawSub', 'shared.retry', 'trn.exportTimer', 'trn.discardTimer', 'trn.confirmDiscardTimer', 'trn.rejectedSave', 'trn.invalidTimer', 'trn.limitTime', 'trn.limitIntervals', 'trn.timerPrecision', 'trn.timerBounds']) {
    assert.notStrictEqual(I.t(key), key);
  }
}
I.setLang('en');
