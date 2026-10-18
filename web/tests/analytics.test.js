// Checks pure analytics and state helpers: node web/tests/analytics.test.js (also run by go test ./web).
'use strict';
const assert = require('assert');
const I = require('../i18n.js');
const RT = require('../app.js');

I.setLang('en');
// Kickoff goals: within 3 s of the match start, the previous goal or the overtime start.
const G = (t, team, ot) => ({ t: t, team: team, overtime: !!ot });
const koReg = { result: 'loss', duration_s: 300, team_score: 1, opp_score: 3, goals: [G(2, 'them'), G(50, 'us'), G(53, 'them'), G(57, 'them')] };
const koOt = { result: 'win', overtime: true, duration_s: 320, overtime_s: 20, team_score: 2, opp_score: 1, goals: [G(100, 'them'), G(298, 'us'), G(302, 'us', true)] };
assert.deepStrictEqual(RT.kickoffGoals(koReg).map((k) => k.kickoff), [true, false, true, false]);
assert.deepStrictEqual(RT.kickoffGoals(koOt).map((k) => k.kickoff), [false, false, true]);
const ko = RT.computeSituations([koReg, koOt]).kickoff;
assert.deepStrictEqual([ko.kickoffs, ko.won, ko.lost, ko.conceded, ko.afterLost.n], [9, 1, 2, 4, 1]);
// Score at half-time: ahead / level / behind, leads lost, comebacks.
const sc = RT.computeScoreStates([koReg, koOt]);
assert.deepStrictEqual([sc.ahead.n, sc.level.n, sc.behind.n, sc.led, sc.trailed], [0, 0, 2, 1, 2]);
assert.strictEqual(sc.comebackShare, 0.5);
// Touches: thirds from my side, aerial touches, the map cells.
const P = (x, y, z, pre, post) => ({ x: x, y: y, z: z, pre: pre, post: post });
const tm = RT.computeTouches([{ result: 'win', hits: { points: [P(0, 4000, 93, 0, 3000), P(0, -4000, 600, 500, 900), P(0, 0, 93, 0, 1000)] } }]);
assert.deepStrictEqual([tm.n, tm.off, tm.def, tm.mid, tm.aerial, tm.powerful], [3, 1, 1, 1, 1, 1]);
assert.strictEqual(tm.cells.reduce((s, c) => s + c, 0), 3);
// Win factors: a stat that splits wins from losses is reported, with its direction.
const fm = [];
for (let i = 0; i < 40; i++) fm.push({ result: i % 2 ? 'win' : 'loss', duration_s: 300, me: {}, movement: { zero_boost_pct: i % 2 ? 5 + i / 100 : 15 + i / 100 } });
const wf = RT.computeWinFactors(fm);
const zb = wf.clear.find((r) => r.key === 'zero_boost');
assert.ok(zb && zb.better === 'less' && zb.lo.wr > 0.9 && zb.hi.wr === 0, JSON.stringify(zb));
assert.strictEqual(RT.computeFocusSuggestions(fm)[0].focus, 'boost');
// Warm-up plan: car control first, then the suggested mechanics, the minutes add up.
const plan = RT.buildWarmupPlan([{ focus: 'boost' }, { focus: 'freeplay' }, { focus: 'kickoff' }], 15);
assert.deepStrictEqual(plan.map((b) => b.focus), ['freeplay', 'boost', 'kickoff']);
assert.strictEqual(plan.reduce((s, b) => s + b.minutes, 0), 15);
assert.deepStrictEqual(RT.buildWarmupPlan([], 10).map((b) => b.focus), ['freeplay', 'shooting', 'aerials']);
// Warm-up effect: a training of 5+ min ended within an hour before a session's first match warms it up.
const H = 3600000, base = Date.UTC(2026, 9, 1, 18);
const wm = [];
for (let d = 0; d < 12; d++) {
  for (let i = 0; i < 3; i++) {
    const t = base + d * 24 * H + i * 8 * 60000;
    wm.push({ id: d * 10 + i, started_at: new Date(t).toISOString(), ended_at: new Date(t + 6 * 60000).toISOString(), duration_s: 300,
      result: d % 2 === 0 ? 'win' : 'loss', me: { touches: 30 } });
  }
}
const tr0 = (d, min, gap) => ({ started_at: new Date(base + d * 24 * H - gap - min * 60000).toISOString(), ended_at: new Date(base + d * 24 * H - gap).toISOString() });
const trainings = [0, 2, 4, 6, 8, 10].map((d) => tr0(d, 15, 5 * 60000)).concat([tr0(1, 3, 0), tr0(3, 20, 2 * H)]); // too short, too early
const eff = RT.computeWarmupEffect(RT.annotate(wm), trainings);
assert.deepStrictEqual([eff.warm.sessions, eff.cold.sessions, eff.warm.first.wr, eff.cold.first.wr, eff.enough], [6, 6, 1, 0, true]);
const ts = RT.computeTrainingSummary(trainings, RT.annotate(wm), base + 12 * 24 * H);
assert.strictEqual(ts.count, 8);
// Live: the same score at the same time in my matches of the mode; overtimes apart; the session going on.
const lm = [];
for (let i = 0; i < 10; i++) {
  // Even: 1–0 up at 2:00 then won; odd: 0–1 down at 2:00 then lost (the last one in overtime).
  const up = i % 2 === 0;
  lm.push({ id: i, mode: '2v2', online: true, result: up ? 'win' : 'loss', duration_s: i === 9 ? 360 : 300, overtime: i === 9, overtime_s: i === 9 ? 60 : 0,
    team_score: up ? 1 : 0, opp_score: up ? 0 : 1, goals: [{ t: 100, team: up ? 'us' : 'them' }],
    started_at: new Date(base + i * 7 * 60000).toISOString(), ended_at: new Date(base + i * 7 * 60000 + 300000).toISOString(), me: { score: 300 } });
}
const la = RT.annotate(lm);
assert.deepStrictEqual([RT.liveOdds(la, '2v2', 120, 1, false).n, RT.liveOdds(la, '2v2', 120, 1, false).wr], [5, 1]);
assert.deepStrictEqual([RT.liveOdds(la, '2v2', 120, -1, false).n, RT.liveOdds(la, '2v2', 120, -1, false).wr], [5, 0]);
assert.strictEqual(RT.liveOdds(la, '2v2', 60, 0, false).n, 10); // before the goal: all level
assert.strictEqual(RT.liveOdds(la, '1v1', 60, 0, false).n, 0);
assert.strictEqual(RT.liveOdds(la, '2v2', 330, 0, true).n, 1);
// A forfeit freezes the score: 0–2 at 3:00 then given up at 3:20 still counts as a loss at 0–2 later on.
// Down 2 at 1:30 left (210 s): 4 played to the end and lost, 2 comebacks (down 2 from 205 s), 6 forfeits.
const ffm = [];
const ffg = (goals, result, dur, extra) => {
  const us = goals.filter((g) => g[1] === 'us').length;
  ffm.push(Object.assign({ id: 100 + ffm.length, mode: '2v2', online: true, result, duration_s: dur, team_score: us, opp_score: goals.length - us,
    goals: goals.map((g) => ({ t: g[0], team: g[1] })), started_at: new Date(base + ffm.length * H).toISOString() }, extra || {}));
};
for (let i = 0; i < 4; i++) ffg([[100, 'them'], [180, 'them']], 'loss', 300);
for (let i = 0; i < 2; i++) ffg([[190, 'them'], [205, 'them'], [240, 'us'], [270, 'us'], [295, 'us']], 'win', 300);
for (let i = 0; i < 6; i++) ffg([[100, 'them'], [180, 'them']], 'loss', 200, { forfeit: true });
ffg([[100, 'them'], [180, 'them']], 'loss', 200); // ended early without a forfeit (left before the end): unknown later
const ffo = RT.liveOdds(RT.annotate(ffm), '2v2', 210, -2, false);
assert.deepStrictEqual([ffo.wins, ffo.n], [2, 12]);
assert.ok(ffo.ci.lo < ffo.wr && ffo.wr < ffo.ci.hi && ffo.ci.lo >= 0 && ffo.ci.hi <= 1);
// Wilson 90 %: 0 of 5 is still compatible with about 35 %, and an empty sample has no interval.
assert.ok(RT.wilson(0, 5).lo === 0 && RT.wilson(0, 5).hi > 0.3);
assert.ok(Math.abs(RT.wilson(10, 20).lo + RT.wilson(10, 20).hi - 1) < 1e-9);
assert.strictEqual(RT.wilson(0, 0), null);
const cs = RT.currentSession(la, base + 9 * 7 * 60000 + 320000, '2v2');
assert.deepStrictEqual([cs.n, cs.next, cs.record.wins, cs.streak.type], [10, 11, 5, 'loss']);
assert.strictEqual(RT.currentSession(la, base + 9 * 7 * 60000 + 300000 + 31 * 60000, '2v2'), null);
const lv = RT.liveAsMatch({ mode: '2v2', my_team: 1, team_score: 1, opp_score: 0, elapsed_s: 90, me: { name: 'Me', goals: 1 },
  players: [{ name: 'Opp', team: 0 }], goals: [{ t: 50, team: 1, scorer: 'Me', assister: '' }, { t: 60, team: -1, scorer: '' }] });
assert.deepStrictEqual([lv.players.length, lv.players[0].is_me, lv.goals[0].team, lv.goals[0].me_scored, lv.goals[1].team], [2, true, 'us', true, '']);
assert.strictEqual(RT.livePace(2, 150, false), 4);
assert.strictEqual(RT.livePace(2, 30, false), null);
// Status polling: data_rev first, the count without it (older server/agent); null (never/failed) reloads.
assert.strictEqual(RT.dataMarker({ data_rev: 7, match_count: 3 }, 'match_count'), 'rev:7');
assert.strictEqual(RT.dataMarker({ match_count: 3 }, 'match_count'), 3);
assert.strictEqual(RT.dataMarker({ match_count: 3 }, 'training_count'), null);
assert.strictEqual(RT.dataMarker(null, 'match_count'), null);
assert.strictEqual(RT.needsReload('rev:7', 'rev:7'), false);
assert.strictEqual(RT.needsReload('rev:8', 'rev:7'), true);
assert.strictEqual(RT.needsReload(3, null), true); // the last load failed: retried with no new match
assert.strictEqual(RT.needsReload(null, 3), false);
assert.deepStrictEqual(['v0.19.0', '0.19.0', '', undefined].map(RT.stripV), ['0.19.0', '0.19.0', '', '']);
// M01: ties must belong to the same median group in recent form and in the comparison.
const medianGames = Array.from({ length: 40 }, (_, i) => ({
  result: i < 20 ? 'win' : 'loss', duration_s: 300, mode: '2v2',
  movement: { air_pct: i < 20 ? 20 : 80 }
}));
const airFactor = RT.computeWinFactors(medianGames).rows.find((r) => r.key === 'air');
assert.deepStrictEqual([airFactor.threshold, airFactor.inclusive, airFactor.hi.n, airFactor.lo.n], [80, true, 20, 20]);
assert.strictEqual(airFactor.recentBadShare, 1);
assert.ok(RT.computeFocusSuggestions(medianGames).some((s) => s.focus === 'aerials'));
const reversedAir = medianGames.map((m) => ({ ...m, result: m.result === 'win' ? 'loss' : 'win' }));
assert.strictEqual(RT.computeWinFactors(reversedAir).rows.find((r) => r.key === 'air').recentBadShare, 0);
const strictMedian = Array.from({ length: 40 }, (_, i) => ({
  result: i < 20 ? 'win' : 'loss', duration_s: 300, movement: { air_pct: i }
}));
const strictAir = RT.computeWinFactors(strictMedian).rows.find((r) => r.key === 'air');
assert.deepStrictEqual([strictAir.inclusive, strictAir.hi.n, strictAir.recentBadShare], [false, 19, 19 / 20]);
// M02: air time remains descriptive, with no universal good/bad direction.
const airMetric = RT.computeMechanics(medianGames).metrics.find((m) => m.key === 'air_pct');
assert.strictEqual(airMetric.trend, 60);
assert.strictEqual(airMetric.higherBetter, null);
assert.strictEqual(RT.computeMechanics(medianGames).metrics.find((m) => m.key === 'zero_boost_pct').higherBetter, false);
// M05: uncertain timelines cannot describe a historical live score, but results remain usable.
const timeline = {
  result: 'win', mode: '2v2', online: true, duration_s: 300, team_score: 1, opp_score: 0,
  goals: [{ t: 100, team: 'us' }]
};
const unknownGoal = { ...timeline, goals: [{ t: 100, team: '' }] };
assert.strictEqual(RT.liveOdds([unknownGoal], '2v2', 120, 0, false).n, 0);
const invalidTimelines = [
  unknownGoal,
  ...[undefined, null, '100', NaN, Infinity, -1, 301].map((t) => ({ ...timeline, goals: [{ t, team: 'us' }] })),
  { ...timeline, goals: [null] },
  { ...timeline, goals: [] },
  { ...timeline, goals: [{ t: 100, team: 'them' }] },
  { ...timeline, goals: [{ t: 100, team: 'us' }, { t: 110, team: 'us' }] },
  { ...timeline, partial: true },
  { ...timeline, duration_s: undefined },
  { ...timeline, team_score: -1 },
  { ...timeline, team_score: 0.5 },
  { ...timeline, opp_score: undefined }
];
for (const m of invalidTimelines) {
  assert.strictEqual(RT.liveOdds([m], '2v2', 60, 0, false).n, 0);
  assert.deepStrictEqual([RT.computeKpis([m]).n, RT.computeKpis([m]).wins], [1, 1]);
}
assert.strictEqual(RT.liveOdds([timeline], '2v2', 60, 0, false).n, 1);
assert.strictEqual(RT.liveOdds([timeline], '2v2', 120, 1, false).n, 1);
assert.strictEqual(RT.liveOdds([timeline], '2v2', 120, 0, false).n, 0);
const scoreless = { ...timeline, result: 'loss', team_score: 0, goals: [] };
assert.strictEqual(RT.liveOdds([scoreless], '2v2', 120, 0, false).n, 1);
assert.strictEqual(RT.liveOdds([{ ...timeline, goals: [{ t: 0, team: 'us' }] }], '2v2', 0, 1, false).n, 1);
const qualifiedOt = { ...koOt, mode: '2v2', online: true };
assert.strictEqual(RT.liveOdds([qualifiedOt], '2v2', 310, 0, true).n, 1);
assert.strictEqual(RT.liveOdds([{ ...qualifiedOt, goals: [{ t: 100, team: '' }] }], '2v2', 310, 0, true).n, 0);
// M07: no estimated restart after a terminal regulation goal; OT still starts once.
const terminalGoal = { ...timeline, goals: [{ t: 300, team: 'us' }] };
assert.strictEqual(RT.computeSituations([terminalGoal]).kickoff.kickoffs, 1);
assert.strictEqual(RT.computeSituations([timeline]).kickoff.kickoffs, 2);
assert.strictEqual(RT.computeSituations([{ ...terminalGoal, duration_s: 200, goals: [{ t: 200, team: 'us' }] }]).kickoff.kickoffs, 1);
assert.strictEqual(RT.computeSituations([qualifiedOt]).kickoff.kickoffs, 4);
const tiedAtEnd = { ...qualifiedOt, goals: [{ t: 100, team: 'them' }, { t: 300, team: 'us' }, { t: 320, team: 'us', overtime: true }] };
assert.strictEqual(RT.computeSituations([tiedAtEnd]).kickoff.kickoffs, 3);
// M10: enough sessions does not imply enough eligible first-three matches.
const sparseGames = wm.filter((m) => m.id % 10 === 0).slice(0, 10);
const sparseEffect = RT.computeWarmupEffect(RT.annotate(sparseGames), trainings);
assert.deepStrictEqual([
  sparseEffect.warm.sessions, sparseEffect.cold.sessions, sparseEffect.warm.first.n, sparseEffect.cold.first.n,
  sparseEffect.enough, sparseEffect.delta
], [5, 5, 5, 5, false, null]);
I.setLang('fr');
assert.strictEqual(RT.warmupEffectMessage(sparseEffect), 'Pour comparer, chaque groupe doit atteindre 5 sessions et 10 matchs exploitables parmi les 3 premiers : avec échauffement 5/5 sessions, 5/10 matchs ; sans échauffement 5/5 sessions, 5/10 matchs.');
I.setLang('en');
assert.strictEqual(RT.warmupEffectMessage(sparseEffect), 'To compare, each group needs 5 sessions and 10 eligible matches among the first 3: with a warm-up 5/5 sessions, 5/10 matches; without a warm-up 5/5 sessions, 5/10 matches.');
assert.strictEqual(RT.warmupEffectMessage(eff), I.t('trn.effectGood', { v: '100' }));
assert.strictEqual(RT.warmupEffectMessage({ ...eff, delta: -0.1 }), I.t('trn.effectBad', { v: '10' }));
assert.strictEqual(RT.warmupEffectMessage({ ...eff, delta: 0 }), I.t('trn.effectFlat'));
const noWarmup = RT.computeWarmupEffect(RT.annotate(sparseGames), []);
assert.ok(RT.warmupEffectMessage(noWarmup).startsWith(I.t('trn.effectNone')));
assert.ok(RT.warmupEffectMessage(noWarmup).includes('with a warm-up 0/5 sessions, 0/10 matches'));
// Ten first-three matches per side are necessary, and five distinct sessions remain necessary too.
const tenEach = wm.filter((m) => m.id % 10 < 2).slice(0, 20);
const atThreshold = RT.computeWarmupEffect(RT.annotate(tenEach), trainings);
assert.deepStrictEqual([atThreshold.warm.sessions, atThreshold.cold.sessions, atThreshold.warm.first.n, atThreshold.cold.first.n, atThreshold.enough], [5, 5, 10, 10, true]);
const nineWarm = RT.computeWarmupEffect(RT.annotate(tenEach.filter((m) => m.id !== 1)), trainings);
assert.deepStrictEqual([nineWarm.warm.sessions, nineWarm.warm.first.n, nineWarm.enough], [5, 9, false]);
assert.ok(RT.warmupEffectMessage(nineWarm).includes('with a warm-up 5/5 sessions, 9/10 matches'));
const fourSessions = RT.computeWarmupEffect(RT.annotate(wm.filter((m) => m.id < 80)), trainings);
assert.deepStrictEqual([fourSessions.warm.sessions, fourSessions.cold.sessions, fourSessions.warm.first.n, fourSessions.cold.first.n, fourSessions.enough], [4, 4, 12, 12, false]);
I.setLang('fr');
assert.ok(RT.warmupEffectMessage(fourSessions).includes('avec échauffement 4/5 sessions, 12/10 matchs'));
assert.ok(RT.warmupEffectMessage(noWarmup).startsWith(I.t('trn.effectNone')));
I.setLang('en');
// M06: averages give each observed match one vote, not each second of telemetry.
const unequalCoverage = [
  { movement: { sample_s: 300, air_pct: 10 } },
  { movement: { sample_s: 1, air_pct: 90 } }
];
assert.strictEqual(RT.computeMechanics(unequalCoverage).metrics.find((m) => m.key === 'air_pct').avg, 50);
I.setLang('fr');
assert.ok(I.t('mech.sub', { v: '2 matchs' }).includes('Moyennes par match'));
assert.ok(I.t('mech.sub', { v: '2 matchs' }).includes('non pondérées'));
assert.strictEqual(I.t('mp.average'), 'Moyenne par match');
I.setLang('en');
assert.ok(I.t('mech.sub', { v: '2 matches' }).includes('Per-match averages'));
assert.ok(I.t('mech.sub', { v: '2 matches' }).includes('not weighted'));
assert.strictEqual(I.t('mp.average'), 'Per-match average');
// M04: no magnitude-based unit guess, including either side of the old 300 boundary.
const rawSpeeds = RT.computeMechanics([{ movement: { avg_speed: 1450 }, hits: { avg_speed: 3000, max_speed: 4300 } }]);
assert.strictEqual(rawSpeeds.metrics.find((m) => m.key === 'avg_speed').avg, 1450);
assert.strictEqual(rawSpeeds.metrics.find((m) => m.key === 'hits_avg').avg, 3000);
assert.strictEqual(rawSpeeds.maxHitRaw, 4300);
for (const defs of [RT.MECH_METRICS, RT.MOVE_STATS, RT.HIT_STATS, RT.FACTORS, RT.FATIGUE_STATS]) {
  assert.ok(defs.every((d) => d.unit !== 'km/h'), 'unknown telemetry carries no physical unit');
}
assert.strictEqual(RT.statValue({ movement: { avg_speed: 95 } }, 'movement', RT.MOVE_STATS[0]), 95);
assert.strictEqual(RT.FACTORS.find((f) => f.key === 'speed').value({ movement: { avg_speed: 95 } }), 95);
assert.strictEqual(RT.FATIGUE_STATS.find((f) => f.key === 'hit_power').value({ hits: { avg_speed: 95 } }), 95);
for (const lang of ['fr', 'en']) {
  I.setLang(lang);
  for (const value of [0, 95, 299, 300, 3120.4]) {
    assert.strictEqual(RT.formatRawSpeed(value), RT.fmtNum(value, 1) + ' ' + I.t('unit.rawSpeed'));
  }
  for (const missing of [null, undefined, NaN, Infinity, '95']) assert.strictEqual(RT.formatRawSpeed(missing), '—');
}
I.setLang('en');
// Declared source units are labels, never guessed or converted; incompatible units do not aggregate.
assert.strictEqual(RT.formatRawSpeed(95, 'km/h'), RT.fmtNum(95, 1) + ' km/h');
assert.strictEqual(RT.formatRawSpeed(1450, 'uu/s'), RT.fmtNum(1450, 1) + ' uu/s');
assert.strictEqual(RT.formatRawSpeed(95, 'unknown'), RT.formatRawSpeed(95));
const typed = (speed_unit) => ({ result: 'win', speed_unit, movement: { avg_speed: 95, air_pct: 20 }, hits: { avg_speed: 100, max_speed: 110 } });
const typedMech = RT.computeMechanics([typed('uu/s')]);
assert.strictEqual(typedMech.metrics[0].avg, 95);
assert.strictEqual(typedMech.metrics[0].unit, 'uu/s');
const mixedUnits = [typed('km/h'), typed('uu/s'), typed('unknown')];
const mixedMech = RT.computeMechanics(mixedUnits);
assert.strictEqual(mixedMech.metrics[0].avg, null);
assert.strictEqual(mixedMech.metrics.find((m) => m.key === 'air_pct').avg, 20);
assert.strictEqual(mixedMech.maxHitRaw, null);
assert.strictEqual(RT.computeWinFactors(mixedUnits).rows.find((r) => r.key === 'speed').enough, false);
assert.ok(RT.computeSessionFatigue(mixedUnits).rows.find((r) => r.key === 'speed').cells.every((c) => c.n === 0));
assert.strictEqual(RT.liveAsMatch({ speed_unit: 'uu/s', players: [] }).speed_unit, 'uu/s');
const mixedHistory = RT.annotate(mixedUnits.concat([typed('uu/s')]).map((m, i) => Object.assign({}, m, {
  id: i + 1, mode: '2v2', started_at: new Date(Date.UTC(2026, 9, 18, i)).toISOString(), duration_s: 300
})));
const mixedBase = RT.matchBaseline(mixedHistory, 4);
assert.strictEqual(mixedBase.movement.avg_speed.n, 0);
assert.strictEqual(mixedBase.movement.avg_speed.unit, null);
assert.strictEqual(mixedBase.movement.air_pct.avg, 20);
// Omitted historical metadata must not mix with newly unqualified observations.
const legacy = { result: 'win', movement: { avg_speed: 1500, air_pct: 20 }, hits: { avg_speed: 3000, max_speed: 4000 } };
const unknown = { ...typed('unknown'), movement: { avg_speed: 60, air_pct: 20 } };
const legacyUnknown = RT.computeMechanics([legacy, unknown]);
assert.strictEqual(legacyUnknown.metrics[0].avg, null, 'legacy/unknown average must not become 780');
assert.strictEqual(legacyUnknown.metrics[0].n, 0);
assert.strictEqual(legacyUnknown.maxHitRaw, null);
assert.strictEqual(legacyUnknown.metrics.find((m) => m.key === 'air_pct').avg, 20);
assert.strictEqual(RT.computeMechanics([legacy]).metrics[0].avg, 1500);
assert.strictEqual(RT.computeMechanics([unknown]).metrics[0].avg, 60);
assert.notStrictEqual(RT.speedUnit(legacy), RT.speedUnit(unknown));
assert.strictEqual(RT.speedUnit({ speed_unit: '' }), RT.speedUnit(legacy));
assert.strictEqual(RT.computeMechanics([legacy, typed('uu/s')]).metrics[0].avg, null,
  'historical convention is not verified source provenance');
assert.strictEqual(RT.computeWinFactors([legacy, unknown]).rows.find((r) => r.key === 'speed').enough, false);
assert.ok(RT.computeSessionFatigue([legacy, unknown]).rows.find((r) => r.key === 'speed').cells.every((c) => c.n === 0));
const legacyHistory = RT.annotate([legacy, legacy, unknown, unknown].map((m, i) => ({ ...m,
  id: i + 1, mode: '2v2', started_at: new Date(Date.UTC(2026, 9, 18, i)).toISOString(), duration_s: 300
})));
assert.strictEqual(RT.matchBaseline(legacyHistory, 4).movement.avg_speed.n, 0);
for (const lang of ['fr', 'en']) {
  I.setLang(lang);
  assert.strictEqual(RT.formatRawSpeed(1500, RT.speedUnit(legacy)), RT.fmtNum(1500, 1) + ' ' + I.t('unit.legacySpeed'));
  assert.strictEqual(RT.formatRawSpeed(60, RT.speedUnit(unknown)), RT.fmtNum(60, 1) + ' ' + I.t('unit.rawSpeed'));
}

// Live momentum: the last 90 s against the earlier part of the match, per team.
{
  const ev = (t, kind, team) => ({ t, kind, team });
  // Opponents: 6 shots and 2 goals over the first 150 s (2.4/min), then 1 shot, saved, in the last 90 s (3.6 expected).
  // My team: 1 shot before (missed), 4 in the last 90 s (0.6 expected), 2 of them scored.
  const events = [30, 50, 70, 90, 110, 140].map((t) => ev(t, 'shot', 'them')).concat([ev(100, 'shot', 'us'), ev(160, 'shot', 'them')],
    [170, 190, 210, 230].map((t) => ev(t, 'shot', 'us')), [ev(160, 'save', 'us'), ev(190, 'save', 'them')]);
  const goals = [{ t: 50, team: 'them' }, { t: 110, team: 'them' }, { t: 210, team: 'us' }, { t: 230, team: 'us' }];
  const mo = RT.computeMomentum({ events, goals }, 240);
  assert.deepStrictEqual([mo.them.recent.shots, mo.them.before.shots, mo.them.recent.goals, mo.us.recent.shots, mo.us.recent.goals], [1, 6, 0, 4, 2]);
  assert.ok(Math.abs(mo.them.expected - 3.6) < 1e-9 && Math.abs(mo.us.recent.shotsPerMin - 4 * 60 / 90) < 1e-9);
  assert.deepStrictEqual([mo.us.lastMin, mo.them.lastMin], [3, 0]); // shots after 180 s
  assert.deepStrictEqual([mo.them.recent.unconverted, mo.us.recent.unconverted], [0, 1]); // theirs saved; 4 of mine: 2 in, 1 saved, 1 neither
  assert.deepStrictEqual(mo.signals.map((s) => s.key).sort(), ['themFading', 'usRising', 'usSharper'].sort());
  // As many shots as before (4 against 3.6 expected) but no more goals: blunted, not fading.
  const blunt = events.concat([175, 200, 220].map((t) => ev(t, 'shot', 'them')));
  assert.deepStrictEqual(RT.computeMomentum({ events: blunt, goals }, 240).signals.filter((x) => x.key.startsWith('them')).map((x) => x.key), ['themBlunted']);
  // Before 3:00 of play the earlier part is shorter than the window: no change is stated yet.
  assert.deepStrictEqual(RT.computeMomentum({ events, goals }, 170).signals, []);
  // An agent without timed events, or the first minute: no momentum at all (missing, not zero).
  assert.strictEqual(RT.computeMomentum({ events: null, goals }, 240), null);
  assert.strictEqual(RT.computeMomentum({ events: [], goals: [] }, 40), null);
  const lvm = RT.liveAsMatch({ my_team: 1, elapsed_s: 100, events: [{ t: 10, kind: 'shot', team: 0 }, { t: 20, kind: 'save', team: 1, me: true }] });
  assert.deepStrictEqual(lvm.events, [{ t: 10, kind: 'shot', team: 'them', me: false }, { t: 20, kind: 'save', team: 'us', me: true }]);
  assert.strictEqual(RT.liveAsMatch({ my_team: 0 }).events, null);
}

// Honor Duels (3:00): own filter (left out of the stats by default, kept in the history), compared only with duels.
{
  const at = (i) => new Date(base + i * H).toISOString();
  const duel = (i, result, goalsT) => ({ id: 500 + i, mode: '1v1', online: true, result, regulation_s: 180, duration_s: 180, started_at: at(i),
    team_score: result === 'win' ? 1 : 0, opp_score: result === 'win' ? 0 : 1, goals: [{ t: goalsT, team: result === 'win' ? 'us' : 'them' }], me: { score: 100 } });
  const std = (i, result) => ({ id: 600 + i, mode: '1v1', online: true, result, regulation_s: 300, duration_s: 300, started_at: at(i),
    team_score: result === 'win' ? 1 : 0, opp_score: result === 'win' ? 0 : 1, goals: [{ t: 30, team: result === 'win' ? 'us' : 'them' }], me: { score: 500 } });
  const ms = RT.annotate([duel(0, 'win', 30), duel(1, 'win', 30), duel(2, 'win', 30), std(3, 'loss'), std(4, 'loss'), std(5, 'loss'), duel(6, 'loss', 30)]);
  const now = base + 10 * H;
  assert.ok(RT.isDuel(ms[0]) && !RT.isDuel(ms[3]) && !RT.isDuel({ duration_s: 180 })); // a 3:00 forfeit is not a duel
  assert.strictEqual(RT.regulationS(ms[0]), 180);
  const f = Object.assign({}, RT.DEFAULT_FILTERS, { period: 'all' });
  assert.strictEqual(RT.filterMatches(ms, f, now).length, 3);
  assert.strictEqual(RT.filterMatches(ms, Object.assign({}, f, { duel: 'only' }), now).length, 4);
  assert.strictEqual(RT.filterMatches(ms, Object.assign({}, f, { duel: 'include' }), now).length, 7);
  assert.strictEqual(RT.filterHistory(ms, {}, now).length, 7);
  assert.strictEqual(RT.filterHistory(ms, { duel: 'exclude' }, now).length, 3);
  const b = RT.matchBaseline(ms, 506, 50);
  assert.deepStrictEqual([b.n, b.duel, b.me.score.avg], [3, true, 100]);
  assert.deepStrictEqual([RT.matchBaseline(ms, 605, 50).n, RT.matchBaseline(ms, 605, 50).me.score.avg], [2, 500]);
  // 1–0 up at 1:00 in a duel: only the duels count.
  const od = RT.liveOdds(ms, '1v1', 60, 1, false, true), os = RT.liveOdds(ms, '1v1', 60, 1, false, false);
  assert.deepStrictEqual([od.n, od.wins, os.n], [3, 3, 0]);
}

// Wall time before v0.20.1 (wall_pct always 0, counted as ground) is unknown, not a measured 0.
{
  const at = (i) => new Date(base + i * H).toISOString();
  const mvOld = { sample_s: 320, ground_pct: 85, wall_pct: 0, air_pct: 15, avg_speed: 1400 };
  const mvNew = { sample_s: 320, ground_pct: 70, wall_pct: 15, air_pct: 15, avg_speed: 1400 };
  const mk = (i, mv, result) => ({ id: 700 + i, mode: '2v2', online: true, result: result || 'win', duration_s: 300, started_at: at(i), me: {}, movement: Object.assign({}, mv) });
  assert.ok(!RT.wallKnown(mvOld) && RT.wallKnown(mvNew) && RT.wallKnown({ sample_s: 40, wall_pct: 0 }));
  const ms = RT.annotate([0, 1, 2, 3, 4].map((i) => mk(i, mvOld)).concat([mk(5, mvNew), mk(6, mvNew), mk(7, mvNew), mk(8, mvNew)]));
  const b = RT.matchBaseline(ms, 708, 50);
  assert.deepStrictEqual([b.movement.wall_pct.n, b.movement.wall_pct.avg, b.movement.ground_pct.avg, b.movement.air_pct.n], [3, 15, 70, 3]);
  assert.strictEqual(b.movement.avg_speed.n, 8); // the other movement stats keep the old matches
  const sp = RT.computeMechanics(ms).split;
  assert.deepStrictEqual([sp.n, sp.ground, sp.wall, sp.air], [4, 70, 15, 15]);
  // The wall factor no longer splits old matches (0) from new ones.
  const wm = RT.annotate(Array.from({ length: 40 }, (_, i) => mk(i, i < 20 ? mvOld : mvNew, i < 20 ? 'loss' : 'win')));
  assert.strictEqual(RT.computeWinFactors(wm).rows.find((r) => r.key === 'wall').enough, false);
}

// Modes: a win rate that only depends on the mode is no factor; a real gap within the modes is one.
{
  const ms = [];
  for (let i = 0; i < 80; i++) {
    const one = i % 2 === 0, win = one ? i % 10 < 4 : i % 10 >= 3; // 1v1: 40 %, 3v3: 70 %, whatever the speed
    ms.push({ mode: one ? '1v1' : '3v3', result: win ? 'win' : 'loss', duration_s: 300, me: {},
      movement: { avg_speed: (one ? 1550 : 1300) + (i % 7) * 5, zero_boost_pct: 12 } });
  }
  assert.ok(!RT.computeWinFactors(ms).clear.some((r) => r.key === 'speed'));
  // Within each mode, low zero boost wins: found, with one median per mode (no single threshold).
  const zm = [];
  for (let i = 0; i < 80; i++) {
    const one = i % 2 === 0, win = i % 4 < 2;
    zm.push({ mode: one ? '1v1' : '3v3', result: win ? 'win' : 'loss', duration_s: 300, me: {},
      movement: { zero_boost_pct: (one ? 20 : 8) + (win ? 0 : 6) + (i % 5) * 0.1 } });
  }
  const z = RT.computeWinFactors(zm).clear.find((r) => r.key === 'zero_boost');
  assert.ok(z && z.better === 'less' && z.threshold === null && z.modes === 2 && z.recent === null, JSON.stringify(z));
  // What follows the score is not a factor any more.
  assert.deepStrictEqual(RT.FACTORS.filter((f) => ['saves', 'def_touches', 'demos_taken', 'demolished'].includes(f.key)), []);
  // Session gaps and mechanics trends on deviations from each mode: 1v1 first, then 3v3, same level.
  const sm = [];
  for (let s = 0; s < 8; s++) {
    for (let k = 1; k <= 6; k++) {
      const one = k <= 3;
      sm.push({ _sess: s, _sessIdx: k, mode: one ? '1v1' : '3v3', result: k % 2 ? 'win' : 'loss', duration_s: 300, me: { touches: one ? 40 : 20 }, movement: { avg_speed: one ? 1600 : 1300 } });
    }
  }
  const tch = RT.computeSessionFatigue(sm).rows.find((r) => r.key === 'touches');
  assert.ok(tch.cells[1].avg < tch.cells[0].avg && Math.abs(tch.cells[2].delta) < 1e-9, JSON.stringify(tch.cells)); // raw drop, no real gap
  const speedTrend = RT.computeMechanics(sm).metrics.find((m) => m.key === 'avg_speed').trend;
  assert.ok(Math.abs(speedTrend) < 1e-9, speedTrend);
}
// Merge contract: new live features retain unit provenance, availability and regulation metadata.
for (const regulation_s of [180, 300]) {
  const terminal = { ...timeline, regulation_s, duration_s: regulation_s, goals: [{ t: regulation_s, team: 'us' }] };
  assert.strictEqual(RT.computeSituations([terminal]).kickoff.kickoffs, 1);
  const ot = { ...terminal, overtime: true, overtime_s: 20, duration_s: regulation_s + 20,
    team_score: 2, opp_score: 1, goals: [{ t: 90, team: 'them' }, { t: regulation_s, team: 'us' },
      { t: regulation_s + 20, team: 'us', overtime: true }] };
  assert.strictEqual(RT.computeSituations([ot]).kickoff.kickoffs, 3, 'terminal equalizer adds only the separately counted OT kickoff');
  const live = RT.liveAsMatch({ mode: '1v1', regulation_s, elapsed_s: regulation_s + 20,
    overtime: true, my_team: 0, speed_unit: 'uu/s', events: [] });
  assert.strictEqual(live.regulation_s, regulation_s);
  assert.strictEqual(live.overtime_s, 20);
  assert.strictEqual(RT.isDuel(live), regulation_s === 180);
  assert.strictEqual(live.speed_unit, 'uu/s');
  assert.deepStrictEqual(live.events, [], 'known empty events must remain different from an old agent');
  assert.strictEqual(RT.computeMomentum(live, live.duration_s).us.recent.shots, 0);
}
for (const events of [undefined, null]) {
  const oldAgent = RT.liveAsMatch({ elapsed_s: 240, events });
  assert.strictEqual(oldAgent.events, null);
  assert.strictEqual(RT.computeMomentum(oldAgent, 240), null);
}
const mixedTrendMatches = Array.from({ length: 40 }, (_, i) => ({
  ...typed(i < 20 ? 'uu/s' : 'unknown'), mode: '2v2', _sessIdx: i % 6 + 1,
  movement: { avg_speed: i < 20 ? 1500 : 60, air_pct: 20 }
}));
const mixedTrend = RT.computeMechanics(mixedTrendMatches).metrics.find((m) => m.key === 'avg_speed');
assert.strictEqual(mixedTrend.avg, null);
assert.strictEqual(mixedTrend.trend, null, 'mode adjustment cannot make incompatible speed units comparable');
assert.ok(mixedTrend.series.every((v) => v === null), 'rolling speeds cannot mix incompatible units either');
assert.ok(RT.computeSessionFatigue(mixedTrendMatches).rows.find((r) => r.key === 'speed').cells
  .every((c) => c.n === 0 && c.dev === null && c.delta === null));
const unavailableCar = { car_sample_s: 0, sample_s: 90, demolished_s: 90,
  avg_speed: null, ground_pct: null, wall_pct: null, air_pct: null, avg_boost: null };
const unavailableMech = RT.computeMechanics([{ speed_unit: 'unknown', movement: unavailableCar }]);
assert.strictEqual(unavailableMech.metrics[0].n, 0);
assert.strictEqual(unavailableMech.split.n, 0, 'unavailable car fields cannot count as a known wall split');
assert.strictEqual(unavailableMech.demolishedS, 90);
const unqualifiedForfeit = { ...timeline, duration_s: 200, forfeit: true, goals: [{ t: 100, team: '' }] };
assert.strictEqual(RT.liveOdds([unqualifiedForfeit], '2v2', 240, 0, false).n, 0,
  'including forfeits must not weaken timeline qualification');
// R2 / PROD-01: display groups must never become comparison cohorts.
const variantGames = Array.from({ length: 40 }, (_, i) => ({
  mode: '2v2', variant: i < 20 ? 'Hoops' : 'Dropshot', result: i < 20 ? 'win' : 'loss',
  duration_s: 300, _sessIdx: i < 20 ? 1 : 5, me: {}, movement: { zero_boost_pct: i < 20 ? 5 : 40, avg_speed: i < 20 ? 500 : 1500 }
}));
assert.strictEqual(RT.modeKey(variantGames[0]), RT.modeKey(variantGames[39]));
assert.notStrictEqual(RT.modeGroup(variantGames[0]), RT.modeGroup(variantGames[39]));
assert.notStrictEqual(RT.modeGroup({ mode: '4v4' }), RT.modeGroup({ mode: 'Chaos' }));
assert.strictEqual(RT.modeGroup({ mode: '2v2' }), RT.modeGroup({ mode: '2v2', variant: 'Soccar' }));
assert.notStrictEqual(RT.modeGroup({ mode: '1v1', regulation_s: 180 }), RT.modeGroup({ mode: '1v1' }));
assert.ok(!RT.computeWinFactors(variantGames).rows.find((r) => r.key === 'zero_boost').clear);
assert.ok(RT.modeDeviations(variantGames, (m) => m.movement.zero_boost_pct).every((v) => v === 0));
assert.strictEqual(RT.computeMechanics(variantGames).metrics.find((m) => m.key === 'avg_speed').trend, 0);
assert.strictEqual(RT.computeSessionFatigue(variantGames).rows.find((r) => r.key === 'zero_boost').cells[2].delta, 0);
const withinVariants = fm.flatMap((m) => ['Hoops', 'Dropshot'].map((variant) => ({ ...m, mode: '2v2', variant })));
assert.ok(RT.computeWinFactors(withinVariants).clear.some((r) => r.key === 'zero_boost'), 'real within-cohort associations remain detectable');

// PROD-02: union only valid bounded active intervals; absent/null retains legacy semantics.
const iso = (t) => new Date(t).toISOString();
const active = (a, b) => ({ started_at: iso(base + a * 1000), ended_at: iso(base + b * 1000) });
const training = { ...active(0, 3600), active_intervals: [active(60, 90), active(0, 60), active(30, 80), active(300, 330)] };
assert.strictEqual(RT.trainingSeconds(training), 120);
assert.strictEqual(RT.trainingSeconds({ ...training, active_intervals: [] }), 0);
assert.strictEqual(RT.trainingSeconds({ ...training, active_intervals: null }), 3600);
assert.strictEqual(RT.trainingSeconds(active(0, 3600)), 3600);
assert.strictEqual(RT.trainingSeconds({ ...training, active_intervals: [null, {}, active(-10, 10), active(3590, 3610), active(20, 10)] }), 0);
const warmMatch = RT.annotate([{ ...timeline, started_at: iso(base + 3600 * 1000), ended_at: iso(base + 3900 * 1000) }]);
assert.strictEqual(RT.sessionsWithWarmup(warmMatch, [training])[0].warm, null, 'an hour with two active minutes cannot qualify');
assert.ok(RT.sessionsWithWarmup(warmMatch, [{ ...training, active_intervals: [active(0, 300)] }])[0].warm);
const activeSummary = RT.computeTrainingSummary([training], [], base + 4000 * 1000);
assert.deepStrictEqual([activeSummary.week, activeSummary.month, activeSummary.foci[0].seconds], [120, 120, 120]);

// PROD-03: demolition availability is independent of touches; absent telemetry is not zero.
const demosOnly = { ...timeline, hits: null, demolitions: [
  { t: 10, player: 'Synthetic opponent', mine: false }, { t: 20, player: 'Synthetic opponent', mine: false }
] };
const demos = RT.computeDemos([demosOnly, { ...timeline, demolitions: [] }, { ...timeline, hits: { points: [P(0, 0, 93)] } }]);
assert.deepStrictEqual([demos.matches, demos.taken, demos.takenPer, demos.often.n, demos.rarely.n], [2, 2, 1, 1, 1]);
assert.strictEqual(RT.computeDemos([timeline]).takenPer, null);

// PROD-04: every temporal card shares strict chronology, while final results remain usable.
const productTimeline = { ...timeline, first_goal: 'us', team_score: 2, opp_score: 1,
  goals: [G(30, ''), G(90, 'us'), G(240, 'us')] };
for (const m of invalidTimelines.concat([productTimeline, { ...timeline, goals: [G(100, 'us'), G(100, 'us')] }])) {
  assert.strictEqual(RT.goalsComplete(m), false);
  assert.strictEqual(RT.computeScoreStates([m]).n, 0);
  assert.strictEqual(RT.computeSituations([m]).firstUs.n + RT.computeSituations([m]).firstThem.n, 0);
  assert.strictEqual(RT.computeSituations([m]).kickoff.matches, 0);
  assert.strictEqual(RT.computeGoalsPerMinute([m]).matches, 0);
  assert.strictEqual(RT.computeKpis([m]).wins, 1);
}
assert.strictEqual(RT.computeSituations([{ ...timeline, first_goal: 'them' }]).firstUs.n, 1, 'derive first goal from qualified events, not stale metadata');
assert.strictEqual(RT.computeScoreStates([scoreless]).level.n, 1);

// PROD-06: actual 60-second buckets, and identical pace normalization for baseline and live.
for (const regulation_s of [180, 300]) {
  const sample = { ...timeline, mode: '1v1', regulation_s, duration_s: regulation_s, goals: [G(90, 'us')], me: { goals: 2 } };
  const bins = RT.computeGoalsPerMinute([sample]);
  assert.strictEqual(bins.us[1], 1);
  assert.strictEqual(bins.labels[1], '1–2′');
  assert.strictEqual(bins.labels.length, regulation_s / 60 + 1);
  assert.strictEqual(RT.computeGoalsPerMinute([{ ...sample, goals: [G(regulation_s, 'us')] }]).us[regulation_s / 60 - 1], 1);
  const overtime = { ...sample, overtime: true, overtime_s: 60, duration_s: regulation_s + 60, me: { goals: 2 * (regulation_s + 60) / regulation_s },
    goals: [G(regulation_s + 20, 'us', true)] };
  assert.strictEqual(RT.computeGoalsPerMinute([overtime]).us.at(-1), 1);
  const normalized = RT.baselineOf([sample, overtime], '1v1', 'Soccar', 50, regulation_s === 180, regulation_s);
  assert.strictEqual(normalized.me.goals.avg, 2);
  assert.strictEqual(RT.livePace(1, regulation_s / 2, false, regulation_s), normalized.me.goals.avg);
  assert.strictEqual(RT.livePace(2, regulation_s, false, regulation_s), normalized.me.goals.avg);
  assert.strictEqual(RT.livePace(3, regulation_s + 60, true, regulation_s), null);
  const raw = RT.baselineOf([overtime], '1v1', 'Soccar', 50, regulation_s === 180);
  assert.strictEqual(raw.me.goals.avg, overtime.me.goals);
}
const mixedMinutes = RT.computeGoalsPerMinute([180, 300].map((regulation_s) => ({ ...timeline, regulation_s, duration_s: regulation_s, goals: [G(90, 'us')] })));
assert.strictEqual(mixedMinutes.us[1], 2, 'same real minute in mixed populations');
console.log('analytics ok (R2 cohorts, active duration, availability, chronology and duel units)');

// RF-03: the persisted whole-second endpoints decide active duration and warm-up eligibility.
const fractionalTraining = { ...active(0.1, 300.3), active_intervals: [active(0.1, 60.9), active(61.1, 300.3)] };
assert.strictEqual(RT.trainingSeconds(fractionalTraining), 299, '300 precise seconds become 299 stored seconds at separate paused endpoints');
const persistedFractional = { ...active(0, 300), active_intervals: [active(0, 60), active(61, 300)] };
assert.strictEqual(RT.trainingSeconds(fractionalTraining), RT.trainingSeconds(persistedFractional));
const fractionalSession = RT.annotate([{ ...timeline, ...active(301, 601) }]);
assert.strictEqual(RT.sessionsWithWarmup(fractionalSession, [fractionalTraining])[0].warm, null);
assert.strictEqual(RT.trainingSeconds({ ...active(0.1, 60.3), active_intervals: [active(0.1, 30.9), active(31.1, 60.3)] }), 59);
assert.strictEqual(RT.trainingSeconds({ ...active(0.1, 60.9), active_intervals: null }), 60, 'legacy endpoints use the same second precision');
