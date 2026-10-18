// Native VM checks of the original rank UI functions with a minimal form/DOM and captured fetch.
// No browser layout, Chart rendering, socket or server is exercised.
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const I = require('../i18n.js');

async function run() {
  I.setLang('en');
  const nodes = new Map();
  function element() {
    let value = '', html = '';
    return {
      get value() { return value; }, set value(v) { value = String(v); },
      get innerHTML() { return html; }, set innerHTML(v) {
        html = v;
        const selected = v.match(/<option value="([^"]+)" selected/);
        if (selected) value = selected[1];
      },
      textContent: '', hidden: false, style: {}, classList: { toggle() {} },
      querySelector() { return node('pill-label'); },
      setAttribute() {}, removeAttribute() {}, addEventListener() {},
      showModal() {}, close() {},
      parentElement: { querySelector() { return null; }, appendChild() {} }
    };
  }
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, element());
    return nodes.get(id);
  }
  node('rank-form').elements = { playlist: element(), mmr: element() };
  const requests = [], chartConfigs = [];
  let fetchReply = async (url, opts) => {
    const body = JSON.parse(opts.body);
    return { ok: true, status: 201, headers: { get() { return 'application/json'; } }, json: async () => ({ ...body, id: 99, at: '2026-10-18T18:00:00Z' }) };
  };
  const now = Date.parse('2026-10-18T18:00:00Z');
  class Clock extends Date { static now() { return now; } }
  const scope = {
    RT_I18N: Object.assign({}, I, { detect() { return 'en'; } }), URLSearchParams, Date: Clock, console,
    location: { search: '', hash: '#/', pathname: '/' },
    localStorage: { getItem() { return null; }, setItem() {} },
    document: {
      readyState: 'loading', addEventListener() {},
      querySelector(sel) { return node(sel.slice(1)); }, getElementById: node,
      createElement: element
    },
    fetch(url, opts) { requests.push({ url, opts }); return fetchReply(url, opts); },
    setTimeout() {}, clearTimeout() {},
    window: { Chart: class { constructor(canvas, config) { chartConfigs.push(config); } destroy() {} } }
  };
  const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
  const marker = '})(typeof globalThis';
  assert.strictEqual(source.split(marker).length, 2, 'one app closure expected');
  // Expose original closure functions only in this VM. Production has no test hook.
  const exposed = `
    root.ui = { state: state, rankSummary: rankSummary, annotateRanks: annotateRanks,
      openRankDialog: openRankDialog, prefillRank: prefillRank, saveRank: saveRank,
      formTier: formTier, profileRanks: profileRanks, endedSession: endedSession,
      renderRanks: renderRanks, renderRankPrompt: renderRankPrompt, goalItems: goalItems, mpGoals: mpGoals, mpMovement: mpMovement, renderStatus: renderStatus };
    Object.assign(T, { us: '#123456', series: ['#123456', '#abcdef', '#654321'] });
  `;
  vm.runInNewContext(source.replace(marker, exposed + marker), scope, { filename: 'app.js' });
  const ui = scope.ui, s = ui.state;
  // Both actual history and match-page renderers must show raw values at either side of 300.
  for (const lang of ['fr', 'en']) {
    I.setLang(lang);
    const goals = [95, 299, 300, 3120.4].map((speed, i) => ({ t: 30 + i * 30, team: 'us', speed }));
    for (const speed_unit of ['unknown', undefined]) {
      const m = { result: 'win', duration_s: 300, team_score: 4, opp_score: 0, goals, speed_unit };
      for (const html of [ui.goalItems(m, goals), ui.mpGoals(m)]) {
        assert.ok(!html.includes('km/h'));
        goals.forEach((g) => assert.ok(html.includes(scope.RT.formatRawSpeed(g.speed, scope.RT.speedUnit(m)))));
        assert.ok(html.includes(I.t(speed_unit ? 'unit.rawSpeed' : 'unit.legacySpeed')));
      }
    }
    const legacyBase = { movement: { avg_speed: { n: 3, avg: 1234.5, unit: 'legacy' } } };
    const current = { speed_unit: 'unknown', movement: { avg_speed: 60 } };
    assert.ok(!ui.mpMovement(current, legacyBase).includes(scope.RT.fmtNum(1234.5, 0)),
      'historical average must not be compared against a new unknown-unit match');
    delete current.speed_unit;
    assert.ok(ui.mpMovement(current, legacyBase).includes(scope.RT.fmtNum(1234.5, 0)));
  }
  I.setLang('en');
  const a = 'epic|a|0', b = 'steam|b|0';
  s.profiles = [{ key: a, name: 'A', n: 2 }, { key: b, name: 'B', n: 1 }];
  s.config = { player_ids: ['Epic|A|0'] };
  s.profile = 'all'; s.loaded = true;
  s.ranks = ui.annotateRanks([
    { id: 1, at: '2026-10-17T18:00:00Z', profile: a, playlist: '2v2', mmr: 1200, tier: 15, division: 2 },
    { id: 2, at: '2026-10-17T19:00:00Z', profile: b, playlist: '2v2', mmr: 700, tier: 9, division: 1 },
    { id: 3, at: '2026-10-17T20:00:00Z', profile: '', playlist: '2v2', mmr: 300, tier: 3, division: 1 }
  ]);
  // M08: neither a summary nor a curve may connect different accounts, including legacy unknowns.
  const summary = ui.rankSummary(s.ranks);
  assert.strictEqual(summary.length, 3);
  assert.ok(summary.every((r) => r.prevMmr === null));
  ui.renderRanks();
  assert.ok(node('rank-cards').innerHTML.includes('Account unspecified'));
  assert.ok(node('rank-cards').innerHTML.includes('A'));
  assert.ok(node('rank-cards').innerHTML.includes('B'));
  assert.ok(!node('rank-cards').innerHTML.includes('−500'));
  // One point in each of three datasets, never a single artificial progression.
  const config = chartConfigs[chartConfigs.length - 1];
  assert.strictEqual(config.data.datasets.length, 3);
  assert.deepStrictEqual(Array.from(config.data.datasets, (d) => d.data.length), [1, 1, 1]);
  const ownDelta = ui.rankSummary(ui.annotateRanks(s.ranks.concat([
    { id: 8, at: '2026-10-18T16:00:00Z', profile: a, playlist: '2v2', mmr: 1250 },
    { id: 9, at: '2026-10-18T17:00:00Z', profile: a, playlist: '1v1', mmr: 900 }
  ])));
  const a2 = ownDelta.find((r) => r.profile === a && r.playlist === '2v2');
  assert.strictEqual(a2.mmr - a2.prevMmr, 50);
  assert.ok(ownDelta.filter((r) => r !== a2).every((r) => r.prevMmr === null));
  ui.openRankDialog('2v2');
  assert.strictEqual(node('rank-form').elements.mmr.value, '1200');
  assert.strictEqual(ui.formTier(), 15);
  assert.ok(node('rank-account').textContent.includes('A'));
  // A subsequent profile selection must not redirect the open form's A values into B.
  s.profile = b;
  assert.strictEqual(node('rank-account').textContent, I.t('rank.fixedTarget', { v: 'A (epic|a|0)' }));
  await ui.saveRank({ preventDefault() {} });
  assert.strictEqual(requests.length, 1);
  assert.strictEqual(requests[0].url, '/api/ranks');
  assert.deepStrictEqual(JSON.parse(requests[0].opts.body), { playlist: '2v2', profile: a, tier: 15, division: 2, mmr: 1200 });
  ui.openRankDialog('2v2');
  assert.strictEqual(node('rank-form').elements.mmr.value, '700');
  assert.strictEqual(ui.formTier(), 9);
  await ui.saveRank({ preventDefault() {} });
  assert.strictEqual(JSON.parse(requests[1].opts.body).profile, b);
  assert.strictEqual(JSON.parse(requests[1].opts.body).mmr, 700);
  s.profile = a;
  assert.ok(ui.profileRanks().every((r) => r.profile === a), 'unknown ranks do not silently become A');
  ui.openRankDialog('2v2');
  s.gen++; s.route = { player: 'bob', view: 'history' }; s.dataFor = 'bob';
  await ui.saveRank({ preventDefault() {} });
  assert.strictEqual(requests.length, 2, 'another player route cannot submit the old dialog');
  assert.strictEqual(node('rank-msg').textContent, I.t('rank.contextChanged'));
  s.route = { player: null, view: 'history' }; s.dataFor = ''; s.profile = a;
  // A pending response must not enter another player's state or close a newer dialog.
  let finish;
  fetchReply = () => new Promise((resolve) => { finish = resolve; });
  ui.openRankDialog('2v2');
  const pending = ui.saveRank({ preventDefault() {} });
  s.gen++; s.route = { player: 'bob', view: 'history' }; s.dataFor = 'bob'; s.ranks = [];
  finish({ ok: true, status: 201, headers: { get() { return 'application/json'; } }, json: async () => ({ id: 100, profile: a, playlist: '2v2', mmr: 1200, at: '2026-10-18T18:00:00Z' }) });
  await pending;
  assert.strictEqual(s.ranks.length, 0);
  s.route = { player: null, view: 'history' }; s.dataFor = ''; s.profile = a;
  // M09: casual and abandoned games extend the profile's session before ranked playlists are selected.
  const match = (id, start, end, tag, profile = a, result = 'win') => ({
    id, started_at: new Date(now + start * 60000).toISOString(), ended_at: new Date(now + end * 60000).toISOString(),
    duration_s: (end - start) * 60, tag, mode: '2v2', online: true, result, me: { primary_id: profile }
  });
  s.allRaw = [match(1, -75, -70, 'ranked'), match(2, -50, -45, 'casual'), match(3, -15, -5, 'casual')];
  s.all = scope.RT.annotate(s.allRaw); s.ranks = [];
  assert.strictEqual(ui.endedSession(), null);
  // Exactly 30 minutes of inactivity counts as over; 30-minute inter-match gaps stay in one session.
  s.allRaw = [match(1, -110, -100, 'ranked'), match(2, -80, -70, 'casual'), match(3, -40, -30, 'casual')];
  s.all = scope.RT.annotate(s.allRaw);
  let ended = ui.endedSession();
  assert.ok(ended);
  assert.deepStrictEqual(Array.from(ended.todo), ['2v2']);
  assert.strictEqual(ended.count['2v2'], 1);
  assert.strictEqual(ended.start, Date.parse(s.allRaw[0].started_at));
  s.ranks = ui.annotateRanks([{ id: 4, profile: b, playlist: '2v2', mmr: 900, at: new Date(now).toISOString() }]);
  assert.ok(ui.endedSession(), 'B noting a rank cannot satisfy A');
  s.ranks[0].profile = a;
  assert.strictEqual(ui.endedSession(), null);
  s.ranks = [];
  s.allRaw.push(match(4, -10, -5, 'casual', a, 'abandoned')); s.all = scope.RT.annotate(s.allRaw);
  assert.strictEqual(ui.endedSession(), null);
  s.allRaw = [match(1, -70, -60, 'ranked'), match(2, -10, -5, 'casual', b)];
  s.profile = 'all'; s.all = scope.RT.annotate(s.allRaw);
  assert.ok(ui.endedSession(), 'B activity does not extend A session when all accounts are shown');
  s.profile = b;
  assert.strictEqual(ui.endedSession(), null);
  s.profile = a;
  s.allRaw = [match(1, -70, -60, 'ranked'), match(2, -25, -20, 'casual')]; s.all = scope.RT.annotate(s.allRaw);
  assert.strictEqual(ui.endedSession(), null, 'a later casual-only session has no ranked playlist to note');
  // Local persistence is independent of delivery; hosted/other-player status never shows this queue.
  const callsBeforeStatus = requests.length;
  s.mode = 'local'; s.route = { player: null, view: 'history' }; s.dataFor = '';
  s.statusError = false;
  const delivery = { pending: 2, held: 3, rejected: 1, legacy_held: 4 };
  s.status = { connected: true, delivery };
  for (const lang of ['fr', 'en']) {
    I.setLang(lang);
    ui.renderStatus();
    const html = node('warnings').innerHTML;
    assert.ok(html.includes(I.t('st.deliveryTitle')));
    assert.ok(html.includes(I.t('st.deliveryCounts', delivery)));
    assert.ok(html.includes(I.t('st.deliveryHeld')));
    assert.ok(html.includes(I.t('st.deliveryRejected')));
    assert.ok(!html.includes('<button'), 'delivery notice is informational, never a send/rebind action');
  }
  I.setLang('en');
  s.mode = 'server'; ui.renderStatus();
  assert.ok(!node('warnings').innerHTML.includes(I.t('st.deliveryTitle')));
  s.mode = 'local'; s.dataFor = 'bob'; ui.renderStatus();
  assert.ok(!node('warnings').innerHTML.includes(I.t('st.deliveryTitle')));
  s.dataFor = ''; s.statusError = true; ui.renderStatus();
  assert.ok(!node('warnings').innerHTML.includes(I.t('st.deliveryTitle')), 'stale queue is not current status');
  s.statusError = false;
  for (const absent of [undefined, { pending: 1 }, { ...delivery, pending: -1 }, { ...delivery, held: '<script>' }]) {
    s.status.delivery = absent; ui.renderStatus();
    assert.ok(!node('warnings').innerHTML.includes(I.t('st.deliveryTitle')));
  }
  s.status.delivery = { pending: 0, held: 0, rejected: 0, legacy_held: 0 }; ui.renderStatus();
  assert.ok(node('warnings').innerHTML.includes(I.t('st.deliveryCounts', s.status.delivery)));
  assert.ok(!node('warnings').innerHTML.includes(I.t('st.deliveryHeld')));
  assert.strictEqual(requests.length, callsBeforeStatus, 'rendering status triggers no transport');
  await r2UI();
  console.log('profiles/session/delivery and R2 timer/field/tilt/rank UI contracts ok (minimal DOM, captured HTTP only)');
}

module.exports = run;
if (require.main === module) run().catch((err) => { console.error(err); process.exitCode = 1; });


// Execute the production timer state machine with a controlled clock and browser storage.
async function r2UI() {
  const initial = Date.parse('2026-10-18T12:00:00Z'), minute = 60000;
  let now = initial, rejectSave = false, rejectStatus = 0, pendingResolve = null, confirmDiscard = false;
  const exported = [], downloads = [], confirmations = [];
  const storage = new Map(), requests = [], nodes = new Map();
  const element = () => ({ hidden: false, innerHTML: '', textContent: '', style: {}, listeners: {},
    addEventListener(name, fn) { this.listeners[name] = fn; },
    classList: { add() {}, remove() {} }, appendChild() {}, querySelector() { return null; } });
  const node = (id) => { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); };
  class Clock extends Date { static now() { return now; } }
  const scope = {
    RT_I18N: Object.assign({}, I, { detect() { return 'en'; } }), URLSearchParams, Date: Clock, console,
    location: { search: '', hash: '#/', pathname: '/' },
    localStorage: { getItem(k) { return storage.get(k) || null; }, setItem(k, v) { storage.set(k, v); }, removeItem(k) { storage.delete(k); } },
    document: { readyState: 'loading', addEventListener() {}, querySelector(s) { return node(s); },
      body: { appendChild() {} }, createElement() { return { click() { downloads.push(this.download); }, remove() {} }; } },
    Blob, URL: { createObjectURL(blob) { exported.push(blob); return 'blob:synthetic-timer'; }, revokeObjectURL() {} },
    window: { confirm(text) { confirmations.push(text); return confirmDiscard; } }, setTimeout() {}, clearTimeout() {}, setInterval() {},
    fetch: async (url, opts) => {
      requests.push({ url, opts });
      if (pendingResolve) await new Promise((resolve) => { pendingResolve.resolve = resolve; });
      if (rejectSave) throw new Error('synthetic unavailable server');
      if (rejectStatus) return { ok: false, status: rejectStatus, text: async () => JSON.stringify({ error: 'synthetic permanent rejection' }) };
      return { ok: true, status: 201, headers: { get() { return 'application/json'; } }, json: async () => ({}) };
    }
  };
  const exposed = `
    var actualTimerCard = renderTimerCard;
    renderTimerCard = renderTimerPill = toast = loadTrainings = drawTouchMap = function () {};
    root.ui = { state: state, renderTimerCard: actualTimerCard, startTimer: startTimer, nextBlock: nextBlock, pauseTimer: pauseTimer,
      finishTimer: finishTimer, loadTimer: loadTimer, tickTimer: tickTimer, blockLeft: blockLeft, bindTraining: bindTraining,
      liveSessionCard: liveSessionCard, livePerf: livePerf, renderField: renderField, mpGoals: mpGoals, goalItems: goalItems };
  `;
  const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
  vm.runInNewContext(source.replace('})(typeof globalThis', exposed + '})(typeof globalThis'), scope);
  const ui = scope.ui, s = ui.state;
  ui.bindTraining();
  const timerAction = (timer) => node('#view-training').listeners.click({ target: { closest() { return { dataset: { timer } }; } } });
  const begin = (offset = 0) => {
    now = initial + offset; s.timer = null; requests.length = 0;
    s.planDraft = [{ focus: 'freeplay', minutes: 5 }, { focus: 'shooting', minutes: 5 }];
    ui.startTimer();
  };
  const posted = () => JSON.parse(requests.filter((r) => r.url === '/api/trainings').at(-1).opts.body);
  begin(); now += minute; ui.pauseTimer(now); now = initial + 60 * minute;
  assert.strictEqual(await ui.finishTimer(), true);
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 60, '60 active seconds with a 59-minute pause');
  assert.strictEqual(Date.parse(posted().started_at), initial, 'calendar start is never backdated to fake a short span');
  assert.strictEqual(Date.parse(posted().ended_at), now);
  assert.strictEqual(s.timer, null);
  assert.ok(!storage.has('rt.timer'));

  begin(); now += 30 * 1000; ui.pauseTimer(now); now += 10 * minute;
  assert.strictEqual(await ui.finishTimer(), false);
  assert.strictEqual(requests.length, 0, 'wall time alone cannot pass the >= 60 active seconds save threshold');

  begin(); now += minute; ui.pauseTimer(now); now += 2 * minute; ui.pauseTimer(now);
  now += minute; ui.nextBlock(now); now += minute; ui.pauseTimer(now);
  s.timer = ui.loadTimer(); // A paused reload cannot turn the open pause into activity.
  now += 10 * minute; ui.pauseTimer(now); now += minute;
  assert.strictEqual(await ui.finishTimer(), true);
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 240, 'pauses across blocks and reload are excluded');
  assert.strictEqual(posted().active_intervals.length, 3, 'adjacent block intervals are unioned before sending');
  assert.strictEqual(scope.RT.sessionsWithWarmup(scope.RT.annotate([{
    started_at: new Date(now).toISOString(), ended_at: new Date(now + 300000).toISOString(), result: 'win'
  }]), [posted()])[0].warm, null, 'four active minutes remain ineligible despite a long calendar span');

  begin(); now += minute; ui.pauseTimer(now); now += 3 * minute; ui.nextBlock(now);
  now += minute; await ui.finishTimer();
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 120, 'skipping a paused block resumes only at the transition');

  begin(); now += minute; s.timer = ui.loadTimer(); now += minute;
  await ui.finishTimer();
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 120, 'a running new-format timer survives reload');

  // Legacy timers have no interval locations: exclude all earlier activity, including unknown prior blocks.
  begin();
  storage.set('rt.timer', JSON.stringify({ blocks: s.timer.blocks, started: initial - 60 * minute, idx: 1,
    blockStart: initial - 5 * minute, paused: null, pausedMs: 2 * minute }));
  s.timer = ui.loadTimer();
  assert.strictEqual(s.timer.legacyActivityExcluded, true);
  now += minute; await ui.finishTimer();
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 60);
  assert.strictEqual(Date.parse(posted().started_at), initial - 60 * minute);
  storage.set('rt.timer', JSON.stringify({ blocks: [{ focus: 'freeplay', minutes: 5 }], started: initial,
    idx: 0, blockStart: initial, paused: initial + minute, pausedMs: 0 }));
  now = initial + 60 * minute; s.timer = ui.loadTimer();
  assert.strictEqual(await ui.finishTimer(), false, 'legacy paused activity is not reconstructed from guessed locations');
  assert.ok(s.timer && storage.has('rt.timer'), 'short legacy activity remains available until an explicit user decision');

  begin(); now += minute; rejectSave = true;
  assert.strictEqual(await ui.finishTimer(), false);
  const failedBody = posted();
  assert.ok(s.timer && storage.has('rt.timer'), 'failed save keeps the frozen timer in storage');
  ui.renderTimerCard();
  assert.ok(node('#train-plan').innerHTML.includes(I.t('trn.retrySave')));
  assert.ok(node('#train-plan').innerHTML.includes('data-timer="stop"'));
  assert.ok(node('#train-plan').innerHTML.includes('data-timer="export"'), 'a failed timer offers JSON recovery');
  assert.ok(node('#train-plan').innerHTML.includes('data-timer="discard"'), 'a failed timer has a confirmed exit');
  assert.ok(!node('#train-plan').innerHTML.includes(' disabled'));
  now += 20 * minute; s.timer = ui.loadTimer(); rejectSave = false;
  pendingResolve = {};
  const retry = ui.finishTimer();
  ui.renderTimerCard();
  assert.ok(node('#train-plan').innerHTML.includes(' disabled'), 'pending retry disables the retry action');
  const count = requests.length;
  assert.strictEqual(await ui.finishTimer(), false, 'do not issue concurrent retries');
  assert.strictEqual(requests.length, count);
  pendingResolve.resolve(); pendingResolve = null;
  assert.strictEqual(await retry, true);
  assert.deepStrictEqual(posted(), failedBody, 'retry after reload sends the same calendar bounds and intervals');

  begin(); now += minute; ui.nextBlock(now); now += minute;
  await ui.nextBlock(now); // Natural final-block completion uses the same active-save path.
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 120);

  // RF-03: use the actual timer's fractional endpoints, not an analytical reconstruction.
  begin(100); now = initial + 60900; ui.pauseTimer(now);
  now = initial + 61100; ui.pauseTimer(now); now = initial + 300300;
  await ui.finishTimer();
  const fractionBody = posted();
  assert.strictEqual(scope.RT.trainingSeconds(fractionBody), 299);
  assert.strictEqual(Date.parse(fractionBody.started_at), initial);
  assert.strictEqual(Date.parse(fractionBody.ended_at), initial + 300000);
  assert.deepStrictEqual(fractionBody.active_intervals, [
    { started_at: new Date(initial).toISOString(), ended_at: new Date(initial + 60000).toISOString() },
    { started_at: new Date(initial + 61000).toISOString(), ended_at: new Date(initial + 300000).toISOString() }
  ]);
  assert.strictEqual(scope.RT.sessionsWithWarmup(scope.RT.annotate([{
    started_at: new Date(initial + 301000).toISOString(), ended_at: new Date(initial + 601000).toISOString(), result: 'win'
  }]), [fractionBody])[0].warm, null, 'canonical 299 seconds do not qualify before or after persistence');
  begin(100); now = initial + 30900; ui.pauseTimer(now); now = initial + 31100; ui.pauseTimer(now); now = initial + 60300;
  assert.strictEqual(await ui.finishTimer(), false, '60 fractional active seconds produce only 59 stored seconds');
  assert.strictEqual(requests.length, 0, 'the one-minute threshold uses the exact canonical body');
  assert.strictEqual(scope.RT.trainingSeconds(s.timer.payload), 59);
  assert.ok(storage.has('rt.timer'));
  begin(100); now = initial + 30900; ui.pauseTimer(now); now = initial + 31100; ui.pauseTimer(now); now = initial + 61100;
  assert.strictEqual(await ui.finishTimer(), true);
  assert.strictEqual(scope.RT.trainingSeconds(posted()), 60);

  // RF-02: a real HTTP 400 retains exactly the rejected body, including after reload/retry.
  begin(); now += minute; rejectStatus = 400;
  assert.strictEqual(await ui.finishTimer(), false);
  const rejectedBody = posted();
  assert.strictEqual(s.timer.saveRejected, true);
  now += minute; s.timer = ui.loadTimer();
  assert.strictEqual(await ui.finishTimer(), false);
  assert.deepStrictEqual(posted(), rejectedBody);
  for (const lang of ['fr', 'en']) {
    I.setLang(lang); ui.renderTimerCard();
    assert.ok(node('#train-plan').innerHTML.includes(I.t('trn.rejectedSave')));
    assert.ok(node('#train-plan').innerHTML.includes(I.t('trn.exportTimer')));
    assert.ok(node('#train-plan').innerHTML.includes(I.t('trn.discardTimer')));
    confirmDiscard = false; timerAction('discard');
    assert.strictEqual(confirmations.at(-1), I.t('trn.confirmDiscardTimer'));
    assert.ok(s.timer, 'cancelling discard keeps the payload');
  }
  timerAction('export');
  assert.deepStrictEqual(JSON.parse(await exported.at(-1).text()), rejectedBody);
  assert.ok(downloads.at(-1).endsWith('.json'));
  assert.ok(storage.has('rt.timer'), 'export never discards the original');
  const kept = JSON.stringify(s.timer); ui.startTimer(); assert.strictEqual(JSON.stringify(s.timer), kept, 'a new start cannot overwrite retained activity');
  confirmDiscard = true; timerAction('discard'); assert.strictEqual(s.timer, null); assert.ok(!storage.has('rt.timer'));
  timerAction('start'); assert.ok(s.timer && !s.timer.ended, 'confirmed discard frees the actual UI timer');
  rejectStatus = 0;

  // A paused night is frozen with actual bounds. No rejected >12 h body is posted or truncated.
  begin(); now += minute; ui.pauseTimer(now); now = initial + 13 * 60 * minute;
  ui.pauseTimer(now); // Attempting to resume detects the bound before measuring new activity.
  assert.strictEqual(s.timer.issue, 'trn.limitTime');
  assert.strictEqual(Date.parse(s.timer.payload.ended_at) - Date.parse(s.timer.payload.started_at), 13 * 60 * minute);
  assert.strictEqual(scope.RT.trainingSeconds(s.timer.payload), 60);
  assert.strictEqual(await ui.finishTimer(), false); assert.strictEqual(requests.length, 0);
  const nightBody = JSON.stringify(s.timer.payload); now += 60 * minute; s.timer = ui.loadTimer();
  assert.strictEqual(await ui.finishTimer(), false); assert.strictEqual(JSON.stringify(s.timer.payload), nightBody);
  ui.renderTimerCard(); assert.ok(node('#train-plan').innerHTML.includes(I.t('trn.limitTime')));
  timerAction('export'); assert.strictEqual(JSON.stringify(JSON.parse(await exported.at(-1).text())), nightBody);
  confirmDiscard = false; timerAction('discard'); assert.ok(s.timer);
  confirmDiscard = true; timerAction('discard'); timerAction('start'); assert.strictEqual(s.timer.started, now);

  // Exactly 12 h is saveable, but a tick after the bound freezes all measured time for recovery.
  begin(); now = initial + 12 * 60 * minute; ui.tickTimer();
  assert.ok(s.timer.ended); assert.strictEqual(await ui.finishTimer(), true);
  assert.strictEqual(Date.parse(posted().ended_at) - Date.parse(posted().started_at), 12 * 60 * minute);
  begin(); now = initial + 12 * 60 * minute + 1000; ui.tickTimer();
  assert.ok(s.timer.ended); assert.strictEqual(await ui.finishTimer(), false); assert.strictEqual(requests.length, 0);
  assert.strictEqual(scope.RT.trainingSeconds(s.timer.payload), 12 * 3600 + 1, 'a delayed tab does not silently drop the overrun');

  const intervals = (n) => Array.from({ length: n }, (_, i) => ({
    started_at: new Date(initial + i * 2000).toISOString(), ended_at: new Date(initial + i * 2000 + 1000).toISOString()
  }));
  // Opening interval 4097 is blocked. The first 4096 remain intact and saveable explicitly.
  begin(); now = initial + 8192000;
  Object.assign(s.timer, { activeIntervals: intervals(4096), activeSince: null, paused: now - 1000 });
  ui.pauseTimer(now);
  assert.strictEqual(s.timer.issue, 'trn.limitIntervals'); assert.ok(s.timer.ended);
  assert.strictEqual(s.timer.payload.active_intervals.length, 4096);
  assert.strictEqual(scope.RT.trainingSeconds(s.timer.payload), 4096);
  assert.strictEqual(await ui.finishTimer(), true); assert.strictEqual(posted().active_intervals.length, 4096);
  // An old persisted 4097-interval timer is kept and exported, with no invalid POST and no silent clipping.
  begin(); now = initial + 8194000;
  Object.assign(s.timer, { activeIntervals: intervals(4097), activeSince: null, paused: now, ended: now });
  storage.set('rt.timer', JSON.stringify(s.timer)); s.timer = ui.loadTimer();
  assert.strictEqual(await ui.finishTimer(), false); assert.strictEqual(requests.length, 0);
  assert.strictEqual(s.timer.payload.active_intervals.length, 4097);
  timerAction('export'); assert.strictEqual(JSON.parse(await exported.at(-1).text()).active_intervals.length, 4097);
  confirmDiscard = false; timerAction('discard'); assert.strictEqual(s.timer.payload.active_intervals.length, 4097);
  confirmDiscard = true; timerAction('discard'); timerAction('start'); assert.ok(s.timer && !s.timer.ended);

  const session = { start: initial, n: 3, next: 4, record: { wins: 0, losses: 3, n: 3, wr: 0 }, mode: { n: 3 },
    streak: { type: 'loss', len: 3 }, later: { n: 0 }, overall: { wr: 0 } };
  s.all = []; s.ranks = []; s.profiles = [];
  for (const lang of ['fr', 'en']) {
    I.setLang(lang);
    for (const tilt of [0, 3, undefined, 4]) {
      s.config = { tilt_streak: tilt, goal: { season_start: '2027-01-01' } };
      const html = ui.liveSessionCard(session, '2v2', true);
      assert.strictEqual(html.includes(I.t('live.tiltWarn', { n: 3 })), tilt === 3 || tilt === undefined);
      assert.ok(html.includes(I.t('kpi.streakLosses', { n: 3 })), 'factual streak stays visible');
    }
    ui.renderField([{ result: 'loss', hits: null, demolitions: [
      { t: 10, player: 'Synthetic opponent', mine: false }, { t: 20, player: 'Synthetic opponent', mine: false }
    ] }]);
    assert.strictEqual(node('#field-grid').hidden, false, 'demolition card visible with no touches');
    assert.ok(node('#demo-facts').innerHTML.includes('Synthetic opponent'));
    assert.ok(node('#field-sub').textContent.includes(I.t('field.availability', { touches: 0, goals: 0, demos: 1 })));
    ui.renderField([{ hits: { points: [{ x: 0, y: 0 }] } }]);
    assert.ok(!node('#demo-facts').innerHTML.includes('+0'), 'missing demolition measurements are not zero');
    const unqualified = { duration_s: 300, team_score: 2, opp_score: 1, goals: [
      { t: 30, team: '' }, { t: 90, team: 'us' }, { t: 240, team: 'us' }
    ] };
    assert.ok(!ui.goalItems(unqualified, unqualified.goals).includes('class="sc"'), 'history cannot infer a running score from an unqualified timeline');
    const goalHTML = ui.mpGoals(unqualified);
    assert.ok(!goalHTML.includes('class="timeline"'));
    assert.ok(!goalHTML.includes('class="sc num"'));
    assert.ok(goalHTML.includes(I.t('common.unknown')));
    const m = { mode: '1v1', regulation_s: 180, duration_s: 90, me: { goals: 1 }, goals: [] };
    const historical = Array.from({ length: 3 }, () => ({ ...m, result: 'win', duration_s: 180, me: { goals: 2 } }));
    const baseline = scope.RT.baselineOf(historical, '1v1', 'Soccar', 50, true, 180);
    const html = ui.livePerf(m, baseline);
    assert.ok(html.includes(I.t('live.pace', { minutes: '3' })));
    assert.ok(!html.includes(I.t('live.pace', { minutes: '5' })));
  }
  I.setLang('en');
}
