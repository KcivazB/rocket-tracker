/* Rocket Tracker — dashboard.
 * Part 1: pure analytics (no DOM) — exported for node via module.exports.
 * Part 2: UI (only runs in a browser).
 */
(function (root) {
  'use strict';

  /* ===================================================================== */
  /* Part 1 — pure analytics                                               */
  /* ===================================================================== */

  var ROLL_WINDOW = 20;
  var ROLL_MIN = 5;
  var SESSION_GAP_MS = 30 * 60 * 1000;
  var UU_TO_KMH = 0.036;
  var DAY_MS = 86400000;

  var TAGS = ['ranked', 'casual', 'tournament', 'private', 'other'];
  var TAG_LABELS = { ranked: 'Classé', casual: 'Occasionnel', tournament: 'Tournoi', private: 'Privé', other: 'Autre' };
  var WEEKDAYS = ['Lun', 'Mar', 'Mer', 'Jeu', 'Ven', 'Sam', 'Dim'];

  function isNum(x) { return typeof x === 'number' && isFinite(x); }
  function num(x) { return isNum(x) ? x : 0; }
  function ratio(a, b) { return b > 0 ? a / b : null; }
  function mean(arr) {
    var s = 0, n = 0;
    for (var i = 0; i < arr.length; i++) if (isNum(arr[i])) { s += arr[i]; n++; }
    return n ? s / n : null;
  }
  function sum(arr) { var s = 0; for (var i = 0; i < arr.length; i++) if (isNum(arr[i])) s += arr[i]; return s; }
  function isDecided(m) { return m.result === 'win' || m.result === 'loss'; }
  function isWin(m) { return m.result === 'win'; }
  function me(m) { return m.me || {}; }
  function startMs(m) { var t = Date.parse(m.started_at); return isNum(t) ? t : 0; }
  function endMs(m) {
    var t = Date.parse(m.ended_at);
    return isNum(t) && t > 0 ? t : startMs(m) + num(m.duration_s) * 1000;
  }
  function kmh(uu) { return isNum(uu) ? uu * UU_TO_KMH : null; }
  // GoalSpeed unit is undocumented: values below 300 can only be km/h (300 uu/s is ~11 km/h).
  function goalKmh(v) { return isNum(v) ? (v < 300 ? v : v * UU_TO_KMH) : null; }
  /** Regulation length (s) of a match: duration minus overtime when in OT (handles a regulation inferred ≠ 300 s). */
  function regulationS(m) {
    var r = m && m.overtime ? num(m.duration_s) - num(m.overtime_s) : 300;
    return r > 0 ? r : 300;
  }
  /** True when goal g of match m was scored in overtime. */
  function isOtGoal(m, g) { return !!g.overtime || num(g.t) > regulationS(m); }
  /** False when the goal list misses goals (tracker started mid-match): its timings and first goal are unreliable. */
  function goalsComplete(m) { return !m.partial && (m.goals || []).length >= num(m.team_score) + num(m.opp_score); }

  /** Winrate summary over a list: {wins, losses, n, wr} (wr null when no decided match). */
  function wl(ms) {
    var w = 0, l = 0;
    for (var i = 0; i < ms.length; i++) { if (ms[i].result === 'win') w++; else if (ms[i].result === 'loss') l++; }
    return { wins: w, losses: l, n: w + l, wr: ratio(w, w + l) };
  }

  /** Sorts oldest → newest and annotates session info. Session = gap > 30 min between matches.
   *  Computed on the unfiltered list so filters do not break session continuity. */
  function annotate(matches) {
    var ms = (matches || []).filter(function (m) { return m && typeof m === 'object'; }).slice();
    ms.sort(function (a, b) { return startMs(a) - startMs(b) || num(a.id) - num(b.id); });
    var sess = -1, idx = 0, prev = null;
    for (var i = 0; i < ms.length; i++) {
      var m = ms[i];
      var t = startMs(m);
      if (!prev || t - endMs(prev) > SESSION_GAP_MS) { sess++; idx = 1; } else { idx++; }
      m._t = t;
      m._sess = sess;
      m._sessIdx = idx;
      m._prevResult = (idx > 1 && prev && isDecided(prev)) ? prev.result : null;
      prev = m;
    }
    return ms;
  }

  function modeKey(m) {
    if (m.variant && m.variant !== 'Soccar') return 'other';
    return (m.mode === '1v1' || m.mode === '2v2' || m.mode === '3v3') ? m.mode : 'other';
  }

  var DEFAULT_FILTERS = { mode: 'all', period: '30', tag: 'all', onlineOnly: true, excludeAbandoned: true };

  function filterMatches(ms, f, now) {
    f = f || DEFAULT_FILTERS;
    now = isNum(now) ? now : Date.now();
    var minT = f.period && f.period !== 'all' ? now - (+f.period) * DAY_MS : -Infinity;
    return ms.filter(function (m) {
      if (f.mode && f.mode !== 'all' && modeKey(m) !== f.mode) return false;
      if (f.tag && f.tag !== 'all' && (m.tag || 'other') !== f.tag) return false;
      if (f.onlineOnly && m.online === false) return false;
      if (f.excludeAbandoned && m.result === 'abandoned') return false;
      if ((m._t != null ? m._t : startMs(m)) < minT) return false;
      return true;
    });
  }

  function streaks(ms) {
    var best = 0, bestLoss = 0, run = 0, runType = null;
    for (var i = 0; i < ms.length; i++) {
      var r = ms[i].result;
      if (r !== 'win' && r !== 'loss') continue;
      if (r === runType) run++; else { runType = r; run = 1; }
      if (r === 'win' && run > best) best = run;
      if (r === 'loss' && run > bestLoss) bestLoss = run;
    }
    return { current: runType ? { type: runType, len: run } : null, bestWin: best, bestLoss: bestLoss };
  }

  function computeKpis(ms) {
    var r = wl(ms);
    var n = ms.length;
    var goals = sum(ms.map(function (m) { return me(m).goals; }));
    var shots = sum(ms.map(function (m) { return me(m).shots; }));
    var mvp = ms.filter(function (m) { return m.mvp; }).length;
    var sessions = {};
    ms.forEach(function (m) { sessions[m._sess] = 1; });
    var scores = ms.map(function (m) { return me(m).score; }).filter(isNum);
    return {
      n: n,
      wins: r.wins, losses: r.losses,
      abandoned: ms.filter(function (m) { return m.result === 'abandoned'; }).length,
      winrate: r.wr,
      goalDiffAvg: mean(ms.filter(isDecided).map(function (m) { return m.goal_diff; })),
      teamGoalsAvg: mean(ms.filter(isDecided).map(function (m) { return m.team_score; })),
      oppGoalsAvg: mean(ms.filter(isDecided).map(function (m) { return m.opp_score; })),
      goalsPer: mean(ms.map(function (m) { return me(m).goals; })),
      assistsPer: mean(ms.map(function (m) { return me(m).assists; })),
      savesPer: mean(ms.map(function (m) { return me(m).saves; })),
      shotsPer: mean(ms.map(function (m) { return me(m).shots; })),
      goals: goals, shots: shots,
      conversion: ratio(goals, shots),
      mvp: mvp,
      mvpRate: ratio(mvp, r.n),
      mvpOfWins: ratio(mvp, r.wins),
      avgScore: mean(scores),
      bestScore: scores.length ? Math.max.apply(null, scores) : null,
      streaks: streaks(ms),
      sessions: Object.keys(sessions).length,
      playSeconds: sum(ms.map(function (m) { return m.duration_s; }))
    };
  }

  /** Rolling mean of fn(m) over the last `win` matches (nulls ignored). */
  function rolling(ms, fn, win, minPeriods) {
    win = win || ROLL_WINDOW;
    minPeriods = minPeriods == null ? ROLL_MIN : minPeriods;
    var vals = ms.map(fn);
    var out = [];
    for (var i = 0; i < vals.length; i++) {
      var s = 0, c = 0;
      for (var j = Math.max(0, i - win + 1); j <= i; j++) if (isNum(vals[j])) { s += vals[j]; c++; }
      out.push(c >= Math.min(minPeriods, win) && c > 0 ? s / c : null);
    }
    return out;
  }

  function computeProgression(ms) {
    return {
      winrate: rolling(ms, function (m) { return isDecided(m) ? (isWin(m) ? 100 : 0) : null; }),
      goalDiff: rolling(ms, function (m) { return isDecided(m) ? m.goal_diff : null; }),
      score: rolling(ms, function (m) { return me(m).score; }),
      goals: rolling(ms, function (m) { return me(m).goals; }),
      assists: rolling(ms, function (m) { return me(m).assists; }),
      saves: rolling(ms, function (m) { return me(m).saves; }),
      shots: rolling(ms, function (m) { return me(m).shots; })
    };
  }

  function dayKey(t) {
    var d = new Date(t);
    return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  }
  function startOfDay(t) { var d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); }
  function startOfWeek(t) { var d = new Date(startOfDay(t)); var wd = (d.getDay() + 6) % 7; d.setDate(d.getDate() - wd); return d.getTime(); }

  /** Matches per day (or per week when the span is > 120 days) with W/L split and winrate. */
  function computeActivity(ms, period, now) {
    now = isNum(now) ? now : Date.now();
    if (!ms.length) return { unit: 'day', buckets: [] };
    var first = period && period !== 'all' ? startOfDay(now - (+period - 1) * DAY_MS) : startOfDay(ms[0]._t || startMs(ms[0]));
    var last = startOfDay(now);
    var weekly = (last - first) / DAY_MS > 120;
    var keyOf = weekly ? function (t) { return dayKey(startOfWeek(t)); } : function (t) { return dayKey(startOfDay(t)); };
    var buckets = [], byKey = {};
    var cursor = weekly ? startOfWeek(first) : first;
    var guard = 0;
    while (cursor <= last && guard++ < 2000) {
      var k = dayKey(cursor);
      var b = { key: k, t: cursor, wins: 0, losses: 0, other: 0 };
      buckets.push(b); byKey[k] = b;
      var d = new Date(cursor); d.setDate(d.getDate() + (weekly ? 7 : 1)); cursor = d.getTime();
    }
    ms.forEach(function (m) {
      var b = byKey[keyOf(m._t || startMs(m))];
      if (!b) return;
      if (m.result === 'win') b.wins++; else if (m.result === 'loss') b.losses++; else b.other++;
    });
    buckets.forEach(function (b) { b.total = b.wins + b.losses + b.other; b.winrate = ratio(b.wins, b.wins + b.losses); });
    return { unit: weekly ? 'week' : 'day', buckets: buckets };
  }

  function computeGoalDiffDist(ms) {
    var lim = 5, counts = {};
    for (var d = -lim; d <= lim; d++) counts[d] = 0;
    var n = 0;
    ms.forEach(function (m) {
      if (!isDecided(m) || !isNum(m.goal_diff)) return;
      var d = Math.max(-lim, Math.min(lim, Math.round(m.goal_diff)));
      counts[d]++; n++;
    });
    var out = [];
    for (var k = -lim; k <= lim; k++) {
      if (k === 0 && counts[0] === 0) continue;
      out.push({ diff: k, label: (k === -lim ? '≤ ' : k === lim ? '≥ ' : '') + (k > 0 ? '+' + k : String(k)), count: counts[k] });
    }
    return { n: n, bins: out };
  }

  function computeMental(ms) {
    var bySess = [];
    for (var i = 0; i < 8; i++) bySess.push({ label: i === 7 ? '8+' : String(i + 1), wins: 0, n: 0 });
    var hours = [], days = [];
    for (var h = 0; h < 24; h++) hours.push({ label: h + 'h', wins: 0, n: 0 });
    for (var d = 0; d < 7; d++) days.push({ label: WEEKDAYS[d], wins: 0, n: 0 });
    var afterWin = { wins: 0, n: 0 }, afterLoss = { wins: 0, n: 0 };
    var sessCount = {};
    ms.forEach(function (m) {
      sessCount[m._sess] = (sessCount[m._sess] || 0) + 1;
      if (!isDecided(m)) return;
      var w = isWin(m) ? 1 : 0;
      var b = bySess[Math.min(7, Math.max(1, m._sessIdx || 1) - 1)];
      b.n++; b.wins += w;
      var dt = new Date(m._t || startMs(m));
      hours[dt.getHours()].n++; hours[dt.getHours()].wins += w;
      var wd = (dt.getDay() + 6) % 7;
      days[wd].n++; days[wd].wins += w;
      if (m._prevResult === 'win') { afterWin.n++; afterWin.wins += w; }
      else if (m._prevResult === 'loss') { afterLoss.n++; afterLoss.wins += w; }
    });
    [bySess, hours, days].forEach(function (arr) { arr.forEach(function (b) { b.wr = ratio(b.wins, b.n); }); });
    afterWin.wr = ratio(afterWin.wins, afterWin.n);
    afterLoss.wr = ratio(afterLoss.wins, afterLoss.n);
    var sizes = Object.keys(sessCount).map(function (k) { return sessCount[k]; });
    return {
      bySession: bySess, byHour: hours, byWeekday: days, afterWin: afterWin, afterLoss: afterLoss,
      sessions: sizes.length,
      avgSessionLen: mean(sizes),
      longestSession: sizes.length ? Math.max.apply(null, sizes) : null
    };
  }

  /* ---------- days & hours insights ---------- */
  var WEEKDAY_NAMES = ['lundi', 'mardi', 'mercredi', 'jeudi', 'vendredi', 'samedi', 'dimanche'];
  var TIME_MIN_GAMES = 20;   // below this, no verdicts at all
  var TIME_SMOOTH_K = 10;    // pseudo-games pulling small samples toward the overall win rate
  var DAY_START_H = 6;       // hours are ordered from 6h so late-night sessions stay contiguous
  var TIME_MIN_GAP = 3;      // pts from the average below which a best/worst verdict isn't worth stating
  // "entre 20h et 23h" for a window [from, from+len).
  function hourRange(from, len) { return 'entre ' + from + 'h et ' + ((from + len) % 24) + 'h'; }
  /**
   * Best/worst weekday and hour window by win rate, plus a weekday x hour grid.
   * Rankings use a smoothed rate (wins + k*p0) / (n + k) so tiny samples don't win;
   * reported rates are the raw ones. Windows are 2-4 consecutive hours (circular).
   */
  function computeTimeInsights(ms) {
    var dec = (ms || []).filter(function (m) { return m && isDecided(m); });
    var total = dec.length, totalWins = dec.filter(isWin).length;
    var p0 = total ? totalWins / total : null;
    var grid = [], days = [], hours = [];
    for (var d = 0; d < 7; d++) {
      days.push({ idx: d, n: 0, wins: 0 });
      grid.push([]);
      for (var h0 = 0; h0 < 24; h0++) grid[d].push({ n: 0, wins: 0 });
    }
    for (var h = 0; h < 24; h++) hours.push({ n: 0, wins: 0 });
    dec.forEach(function (m) {
      var dt = new Date(m._t != null ? m._t : startMs(m));
      var wd = (dt.getDay() + 6) % 7, hr = dt.getHours(), w = isWin(m) ? 1 : 0;
      days[wd].n++; days[wd].wins += w;
      hours[hr].n++; hours[hr].wins += w;
      grid[wd][hr].n++; grid[wd][hr].wins += w;
    });
    var smooth = function (b) { return (b.wins + TIME_SMOOTH_K * p0) / (b.n + TIME_SMOOTH_K); };
    var describe = function (b) { b.wr = ratio(b.wins, b.n); b.delta = b.wr != null && p0 != null ? (b.wr - p0) * 100 : null; return b; };
    days.forEach(function (b) { describe(b); b.name = WEEKDAY_NAMES[b.idx]; });
    hours.forEach(describe);
    grid.forEach(function (row) { row.forEach(describe); });

    var out = { total: total, wr: p0, days: days, hours: hours, grid: grid, enough: total >= TIME_MIN_GAMES,
      bestDay: null, worstDay: null, bestWindow: null, worstWindow: null, busiestDay: null };
    if (!total) return out;
    out.busiestDay = days.slice().sort(function (a, b) { return b.n - a.n; })[0];
    if (!out.enough) return out;

    var dayMin = Math.max(5, Math.round(total * 0.05));
    var cand = days.filter(function (b) { return b.n >= dayMin; });
    cand.sort(function (a, b) { return smooth(b) - smooth(a) || b.n - a.n; });
    if (cand.length >= 2) {
      out.bestDay = cand[0];
      out.worstDay = cand[cand.length - 1];
      if (smooth(out.worstDay) >= smooth(out.bestDay)) out.worstDay = null;
    }

    var winMin = Math.max(8, Math.round(total * 0.08));
    var windows = [];
    for (var len = 2; len <= 4; len++) {
      for (var from = 0; from < 24; from++) {
        var b = { from: from, len: len, n: 0, wins: 0 };
        for (var k = 0; k < len; k++) { var hb = hours[(from + k) % 24]; b.n += hb.n; b.wins += hb.wins; }
        // Edge hours must carry a real share of the window, so the label only spans hours actually played.
        var edgeMin = Math.max(2, Math.ceil(b.n * 0.1));
        if (b.n < winMin || hours[from].n < edgeMin || hours[(from + len - 1) % 24].n < edgeMin) continue;
        b.score = smooth(b);
        windows.push(describe(b));
      }
    }
    if (windows.length) {
      windows.sort(function (a, b) { return b.score - a.score || b.n - a.n; });
      out.bestWindow = windows[0];
      var inBest = function (hr) { return (hr - out.bestWindow.from + 24) % 24 < out.bestWindow.len; };
      var overlaps = function (w) { for (var i = 0; i < w.len; i++) if (inBest((w.from + i) % 24)) return true; return false; };
      var rest = windows.filter(function (w) { return !overlaps(w); });
      var worst = rest.length ? rest[rest.length - 1] : null;
      if (worst && worst.score < out.bestWindow.score) {
        // Among near-ties at the bottom, prefer the larger sample.
        out.worstWindow = worst;
      }
      [out.bestWindow, out.worstWindow].forEach(function (w) { if (w) w.label = hourRange(w.from, w.len); });
    }
    // Only state verdicts that are clearly away from the average.
    if (out.bestDay && out.bestDay.delta < TIME_MIN_GAP) out.bestDay = null;
    if (out.worstDay && out.worstDay.delta > -TIME_MIN_GAP) out.worstDay = null;
    if (out.bestWindow && out.bestWindow.delta < TIME_MIN_GAP) out.bestWindow = null;
    if (out.worstWindow && out.worstWindow.delta > -TIME_MIN_GAP) out.worstWindow = null;
    return out;
  }

  function computeSituations(ms) {
    var dec = ms.filter(isDecided);
    var firstUs = wl(dec.filter(function (m) { return m.first_goal === 'us' && goalsComplete(m); }));
    var firstThem = wl(dec.filter(function (m) { return m.first_goal === 'them' && goalsComplete(m); }));
    var close = wl(dec.filter(function (m) { return isNum(m.goal_diff) && Math.abs(m.goal_diff) <= 1; }));
    var big = wl(dec.filter(function (m) { return isNum(m.goal_diff) && Math.abs(m.goal_diff) >= 3; }));
    var otMs = dec.filter(function (m) { return m.overtime; });
    var ot = wl(otMs);
    return {
      decided: dec.length,
      firstUs: firstUs, firstThem: firstThem,
      close: close, closeShare: ratio(close.n, dec.length),
      big: big,
      ot: ot, otShare: ratio(ot.n, dec.length), otAvgSeconds: mean(otMs.map(function (m) { return m.overtime_s; })),
      forfeitWon: ms.filter(function (m) { return m.forfeit && m.result === 'win'; }).length,
      forfeitLost: ms.filter(function (m) { return m.forfeit && m.result === 'loss'; }).length,
      abandoned: ms.filter(function (m) { return m.result === 'abandoned'; }).length,
      n: ms.length
    };
  }

  /** Goals by game minute (0–1 … 4–5, then overtime), ours vs theirs. */
  function computeGoalsPerMinute(ms) {
    var labels = ['0–1′', '1–2′', '2–3′', '3–4′', '4–5′', 'Prol.'];
    var us = [0, 0, 0, 0, 0, 0], them = [0, 0, 0, 0, 0, 0];
    var nGoals = 0, nMatches = 0;
    ms.forEach(function (m) {
      if (!goalsComplete(m)) return;
      nMatches++;
      var reg = regulationS(m);
      (m.goals || []).forEach(function (g) {
        if (!g) return;
        var t = num(g.t);
        // 5 regulation buckets (= minutes for the standard 300 s) + overtime.
        var b = isOtGoal(m, g) ? 5 : Math.max(0, Math.min(4, Math.floor(t / reg * 5)));
        if (g.team === 'us') us[b]++; else if (g.team === 'them') them[b]++; else return;
        nGoals++;
      });
    });
    return { labels: labels, us: us, them: them, matches: nMatches, goals: nGoals };
  }

  var MECH_METRICS = [
    { key: 'avg_speed', label: 'Vitesse moyenne', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'supersonic_pct', label: 'Supersonique', unit: '%', digits: 1, higherBetter: true },
    { key: 'air_pct', label: 'Temps en l’air', unit: '%', digits: 1, higherBetter: true },
    { key: 'avg_boost', label: 'Boost moyen', unit: '', digits: 0, higherBetter: null },
    { key: 'zero_boost_pct', label: 'À 0 boost', unit: '%', digits: 1, higherBetter: false },
    { key: 'hits_avg', label: 'Puissance de frappe', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true, src: 'hits' }
  ];

  function mechValue(m, metric) {
    var raw;
    if (metric.src === 'hits') raw = m.hits ? m.hits.avg_speed : null;
    else raw = m.movement ? m.movement[metric.key] : null;
    if (!isNum(raw)) return null;
    return metric.conv ? metric.conv(raw) : raw;
  }

  function computeMechanics(ms) {
    var withMove = ms.filter(function (m) { return m.movement || m.hits; });
    var metrics = MECH_METRICS.map(function (metric) {
      var vals = withMove.map(function (m) { return mechValue(m, metric); });
      var valid = vals.filter(isNum);
      var lastN = valid.slice(-ROLL_WINDOW), prevN = valid.slice(-2 * ROLL_WINDOW, -ROLL_WINDOW);
      var trend = (lastN.length >= ROLL_MIN && prevN.length >= ROLL_MIN) ? mean(lastN) - mean(prevN) : null;
      return {
        key: metric.key, label: metric.label, unit: metric.unit, digits: metric.digits, higherBetter: metric.higherBetter,
        avg: mean(valid), n: valid.length, trend: trend,
        series: rolling(withMove, function (m) { return mechValue(m, metric); })
      };
    });
    var mv = ms.filter(function (m) { return m.movement; }).map(function (m) { return m.movement; });
    var maxHit = ms.map(function (m) { return m.hits ? m.hits.max_speed : null; }).filter(isNum);
    return {
      n: withMove.length,
      metrics: metrics,
      split: {
        ground: mean(mv.map(function (x) { return x.ground_pct; })),
        wall: mean(mv.map(function (x) { return x.wall_pct; })),
        air: mean(mv.map(function (x) { return x.air_pct; }))
      },
      boostingPct: mean(mv.map(function (x) { return x.boosting_pct; })),
      fullBoostPct: mean(mv.map(function (x) { return x.full_boost_pct; })),
      powerslidePct: mean(mv.map(function (x) { return x.powerslide_pct; })),
      demolishedS: mean(mv.map(function (x) { return x.demolished_s; })),
      maxHitKmh: maxHit.length ? kmh(Math.max.apply(null, maxHit)) : null
    };
  }

  var STATFEED_LABELS = {
    Goal: 'Buts', Assist: 'Passes décisives', Save: 'Arrêts', EpicSave: 'Arrêts décisifs', Shot: 'Tirs cadrés',
    Demolish: 'Démolitions', Demolition: 'Démolitions', AerialGoal: 'Buts aériens', BackwardsGoal: 'Buts en marche arrière',
    BicycleGoal: 'Buts en retourné', LongGoal: 'Buts de loin', TurtleGoal: 'Buts en tortue', PoolShot: 'Coups de billard',
    HatTrick: 'Coups du chapeau', Playmaker: 'Meneur de jeu', Savior: 'Sauveur', OvertimeGoal: 'Buts en prolongation',
    MVP: 'MVP', Win: 'Victoires', FirstTouch: 'Premiers contacts', Clear: 'Dégagements', Center: 'Centres',
    AerialHit: 'Touches aériennes', BicycleHit: 'Retournés', HighFive: 'High five', LowFive: 'Low five',
    Juggle: 'Jongles', BreakoutDamage: 'Dégâts (Dropshot)', BreakoutDamageLarge: 'Gros dégâts (Dropshot)',
    SwishGoal: 'Swish (Hoops)', BulletGoal: 'Buts canon', FlipReset: 'Flip resets', LowFlipReset: 'Flip resets',
    CrossbarHit: 'Barres transversales', PostHit: 'Poteaux', UltraDemolish: 'Ultra démolitions',
    Unknown: 'Inconnu'
  };
  var BASIC_FEED = { Goal: 1, Assist: 1, Save: 1, Shot: 1, Win: 1, MVP: 1 };

  function statfeedLabel(name) {
    if (!name) return '—';
    if (STATFEED_LABELS[name]) return STATFEED_LABELS[name];
    return String(name).replace(/([a-z])([A-Z])/g, '$1 $2');
  }

  function computeStatfeed(ms, includeBasic) {
    var totals = {};
    ms.forEach(function (m) {
      var sf = m.statfeed || {};
      Object.keys(sf).forEach(function (k) { if (isNum(sf[k])) totals[k] = (totals[k] || 0) + sf[k]; });
    });
    var rows = Object.keys(totals).filter(function (k) { return includeBasic || !BASIC_FEED[k]; })
      .map(function (k) { return { key: k, label: statfeedLabel(k), count: totals[k], perMatch: ratio(totals[k], ms.length) }; })
      .filter(function (r) { return r.count > 0; });
    rows.sort(function (a, b) { return b.count - a.count || a.label.localeCompare(b.label); });
    return rows;
  }

  var ARENA_NAMES = {
    stadium: 'DFH Stadium', eurostadium: 'Mannfield', park: 'Beckwith Park', cs: 'Champions Field', 'cs_day': 'Champions Field',
    trainstation: 'Urban Central', utopiastadium: 'Utopia Coliseum', wasteland: 'Wasteland', neotokyo: 'Neo Tokyo',
    arc: 'Starbase ARC', farm: 'Farmstead', beach: 'Salty Shores', forbiddentemple: 'Forbidden Temple',
    underwater: 'AquaDome', hoopsstadium: 'Dunk House', shattershot: 'Core 707', throwbackstadium: 'Throwback Stadium',
    labs: 'Rocket Labs', chinatown: 'Forbidden Temple', street: 'Sovereign Heights', music: 'Neon Fields',
    outlaw: 'Deadeye Canyon', bb: 'Champions Field (NFL)', haunted: 'Haunted Mannfield', swoosh: 'Estadio Vida',
    fni: 'Futura Garden', woods: 'Drift Woods', hoopsstreet: 'The Block', ff: 'Estadio Vida'
  };
  var ARENA_VARIANTS = {
    day: 'Jour', night: 'Nuit', rainy: 'Pluie', rain: 'Pluie', snowy: 'Neige', snow: 'Neige', dawn: 'Aube', dusk: 'Crépuscule',
    foggy: 'Brume', stormy: 'Orage', winter: 'Hiver', toon: 'Toon', spooky: 'Spooky', season: 'Saison', fire: 'Feu',
    lava: 'Lave', dark: 'Nuit', grs: '', standard: '', p: ''
  };

  function prettyArena(code) {
    if (!code) return 'Arène inconnue';
    var raw = String(code).trim();
    var parts = raw.split('_').filter(Boolean);
    if (parts.length && parts[parts.length - 1].toLowerCase() === 'p') parts.pop();
    if (!parts.length) return raw;
    var base = parts[0].toLowerCase();
    var name = ARENA_NAMES[base];
    var rest = parts.slice(1);
    if (!name) {
      // fallback: cleaned name, split CamelCase
      return parts.join(' ').replace(/([a-z])([A-Z])/g, '$1 $2').replace(/\s+/g, ' ').trim()
        .replace(/^./, function (c) { return c.toUpperCase(); });
    }
    var variants = rest.map(function (p) {
      var v = ARENA_VARIANTS[p.toLowerCase()];
      return v == null ? p.replace(/([a-z])([A-Z])/g, '$1 $2') : v;
    }).filter(Boolean);
    return variants.length ? name + ' (' + variants.join(', ') + ')' : name;
  }

  function computeArenas(ms) {
    var g = {};
    ms.forEach(function (m) {
      var k = prettyArena(m.arena);
      var a = g[k] || (g[k] = { name: k, n: 0, wins: 0, losses: 0, diffs: [] });
      a.n++;
      if (m.result === 'win') a.wins++; else if (m.result === 'loss') a.losses++;
      if (isDecided(m) && isNum(m.goal_diff)) a.diffs.push(m.goal_diff);
    });
    return Object.keys(g).map(function (k) {
      var a = g[k];
      return { name: a.name, n: a.n, wins: a.wins, losses: a.losses, wr: ratio(a.wins, a.wins + a.losses), gdAvg: mean(a.diffs) };
    }).sort(function (a, b) { return b.n - a.n || a.name.localeCompare(b.name); });
  }

  /* ---------- season goal (calendar) ---------- */
  var GOAL_DEFAULT = { mode: '1v1', daily: 10, season_start: '', season_end: '' };
  var MAX_SEASON_DAYS = 400;
  function parseDay(s) {
    var r = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s || '');
    return r ? new Date(+r[1], +r[2] - 1, +r[3]).getTime() : null;
  }
  // Calendar arithmetic (DST-safe, unlike adding 24 h).
  function addDays(t, n) { var d = new Date(t); d.setDate(d.getDate() + n); return d.getTime(); }
  // Games that count toward the goal: online, finished (win/loss incl. forfeits), in the goal mode.
  function isGoalGame(m, goal) {
    return m.online !== false && isDecided(m) && (goal.mode === 'all' || m.mode === goal.mode);
  }
  /**
   * Daily-games goal over a season. Empty season_start = day of the first goal game (or today),
   * empty/invalid season_end = start + 99 days. Today counts as elapsed but is still "in progress":
   * the current streak doesn't break before the day is over.
   * manual: hand-entered [{day, mode, games, wins}], added on top of the tracked games.
   */
  function computeGoal(ms, goalCfg, now, manual) {
    var goal = Object.assign({}, GOAL_DEFAULT, goalCfg || {});
    var daily = isNum(goal.daily) && goal.daily > 0 ? Math.round(goal.daily) : GOAL_DEFAULT.daily;
    now = isNum(now) ? now : Date.now();
    var today = startOfDay(now);
    var gms = (ms || []).filter(function (m) { return m && isGoalGame(m, goal); });
    var tOf = function (m) { return m._t != null ? m._t : startMs(m); };
    var man = (manual || []).filter(function (e) {
      return e && parseDay(e.day) != null && num(e.games) > 0 && (goal.mode === 'all' || e.mode === goal.mode);
    });
    var firsts = gms.map(tOf).concat(man.map(function (e) { return parseDay(e.day); }));
    var start = parseDay(goal.season_start);
    if (start == null) start = firsts.length ? startOfDay(Math.min.apply(null, firsts)) : today;
    var end = parseDay(goal.season_end);
    if (end == null || end < start) end = addDays(start, 99);
    if (end > addDays(start, MAX_SEASON_DAYS - 1)) end = addDays(start, MAX_SEASON_DAYS - 1);

    var byDay = {};
    gms.forEach(function (m) {
      var k = dayKey(tOf(m));
      var d = byDay[k] || (byDay[k] = { count: 0, wins: 0, losses: 0 });
      d.count++;
      if (isWin(m)) d.wins++; else d.losses++;
    });
    man.forEach(function (e) {
      var d = byDay[e.day] || (byDay[e.day] = { count: 0, wins: 0, losses: 0 });
      var g = Math.round(num(e.games)), w = Math.min(g, Math.max(0, Math.round(num(e.wins))));
      d.count += g; d.wins += w; d.losses += g - w; d.manual = (d.manual || 0) + g;
    });
    var days = [];
    for (var t = start; t <= end; t = addDays(t, 1)) {
      var k = dayKey(t), d = byDay[k] || { count: 0, wins: 0, losses: 0 };
      days.push({ key: k, t: t, count: d.count, wins: d.wins, losses: d.losses, manual: d.manual || 0, met: d.count >= daily, future: t > today, today: t === today });
    }
    var total = 0, wins = 0, losses = 0, elapsed = 0, met = 0, best = 0, run = 0, remainingDays = 0;
    days.forEach(function (d) {
      total += d.count; wins += d.wins; losses += d.losses;
      if (d.t >= today) remainingDays++;
      if (d.future) return;
      elapsed++;
      if (d.met) { met++; run++; best = Math.max(best, run); } else if (!d.today) run = 0;
    });
    // Current streak: walk back from today (skipped while not met yet), or from the season end once it's over.
    var cur = 0;
    for (var i = days.length - 1; i >= 0; i--) {
      var dd = days[i];
      if (dd.future || (dd.today && !dd.met)) continue;
      if (!dd.met) break;
      cur++;
    }
    var target = days.length * daily;
    var todayStats = byDay[dayKey(today)] || { count: 0, wins: 0, losses: 0 };
    return {
      mode: goal.mode, daily: daily, start: start, end: end, days: days,
      total: total, target: target, wins: wins, losses: losses, wr: ratio(wins, wins + losses),
      elapsedDays: elapsed, daysMet: met, curStreak: cur, bestStreak: best,
      remainingDays: remainingDays,
      neededPerDay: remainingDays > 0 ? Math.max(0, Math.ceil((target - total) / remainingDays)) : null,
      avgPerDay: elapsed > 0 ? total / elapsed : null,
      projection: elapsed > 0 ? Math.round(total / elapsed * days.length) : null,
      today: { count: todayStats.count, wins: todayStats.wins, losses: todayStats.losses },
      before: today < start, after: today > end
    };
  }

  // Tracked online finished games of one local day, per mode: {'1v1': {games, wins}, ...}.
  function trackedByMode(ms, day) {
    var out = {};
    (ms || []).forEach(function (m) {
      if (!m || m.online === false || !isDecided(m) || dayKey(m._t != null ? m._t : startMs(m)) !== day) return;
      var o = out[m.mode] || (out[m.mode] = { games: 0, wins: 0 });
      o.games++;
      if (isWin(m)) o.wins++;
    });
    return out;
  }

  // tracker.gg profile URL from a Stats API PrimaryId ("Platform|Uid|Splitscreen").
  // Steam profiles are addressed by SteamID64, the other platforms by display name.
  var TRN_PLATFORMS = { steam: 'steam', epic: 'epic', ps4: 'psn', ps5: 'psn', psn: 'psn', xboxone: 'xbl', xbox: 'xbl', xbl: 'xbl', switch: 'switch' };
  function trackerUrl(name, primaryId) {
    var parts = String(primaryId || '').split('|');
    var plat = TRN_PLATFORMS[parts[0].toLowerCase()];
    var uid = parts[1] || '';
    if (!plat || !uid || uid === '0') return null; // bots, offline or unknown platform
    var ident = plat === 'steam' ? uid : String(name || '').trim();
    if (!ident) return null;
    return 'https://rocketleague.tracker.network/rocket-league/profile/' + plat + '/' + encodeURIComponent(ident) + '/overview';
  }

  function computeTeammates(ms, minGames) {
    minGames = minGames == null ? 3 : minGames;
    var g = {};
    ms.forEach(function (m) {
      (m.players || []).forEach(function (p) {
        if (!p || p.is_me || p.team !== m.my_team) return;
        var key = p.primary_id ? 'id:' + p.primary_id : 'name:' + String(p.name || '').toLowerCase();
        if (key === 'name:') return;
        var t = g[key] || (g[key] = { key: key, name: p.name || '?', pid: p.primary_id || '', n: 0, wins: 0, losses: 0, goals: 0, lastT: 0 });
        t.n++;
        if (m.result === 'win') t.wins++; else if (m.result === 'loss') t.losses++;
        t.goals += num(p.goals);
        var tt = m._t || startMs(m);
        if (tt >= t.lastT) { t.lastT = tt; if (p.name) t.name = p.name; }
      });
    });
    return Object.keys(g).map(function (k) { var t = g[k]; t.wr = ratio(t.wins, t.wins + t.losses); t.goalsPer = ratio(t.goals, t.n); return t; })
      .filter(function (t) { return t.n >= minGames; })
      .sort(function (a, b) { return b.n - a.n || (b.wr || 0) - (a.wr || 0); });
  }

  /* ---------- routing, history & match page (pure) ---------- */
  /** Hash route: '#/' dashboard, '#/historique' history, '#/match/<id>' match page (id null when invalid). */
  function parseRoute(hash) {
    var h = String(hash || '').replace(/^#\/?/, '').replace(/\/+$/, '');
    try { h = decodeURIComponent(h); } catch (e) { /* keep raw */ }
    if (h === 'historique') return { view: 'history', id: null };
    var r = /^match\/(\d+)$/.exec(h);
    if (r) return { view: 'match', id: +r[1] };
    if (/^match(\/|$)/.test(h)) return { view: 'match', id: null };
    return { view: 'dash', id: null };
  }

  var DEFAULT_HIST_FILTERS = { mode: 'all', result: 'all', tag: 'all', period: 'all', q: '', includeOffline: true };
  function tOfM(m) { return m._t != null ? m._t : startMs(m); }
  function normText(s) {
    var t = String(s == null ? '' : s).toLowerCase();
    return t.normalize ? t.normalize('NFD').replace(/[̀-ͯ]/g, '') : t;
  }
  function searchTerms(q) { return normText(q).split(/\s+/).filter(Boolean); }
  /** Searchable text of a match: every player name (me, teammates, opponents) and the arena. */
  function matchHaystack(m) {
    var parts = [prettyArena(m.arena), m.arena || ''];
    if (m.me && m.me.name) parts.push(m.me.name);
    (m.players || []).forEach(function (p) { if (p && p.name) parts.push(p.name); });
    return normText(parts.join(' \u0001 '));
  }
  function histMinT(f, now) { return f.period && f.period !== 'all' ? now - (+f.period) * DAY_MS : -Infinity; }
  function histFilters(f) { return Object.assign({}, DEFAULT_HIST_FILTERS, f || {}); }

  /** History filters (independent from the dashboard ones). All terms of the search must match. */
  function filterHistory(ms, f, now) {
    f = histFilters(f);
    now = isNum(now) ? now : Date.now();
    var minT = histMinT(f, now), terms = searchTerms(f.q);
    return (ms || []).filter(function (m) {
      if (!m || typeof m !== 'object') return false;
      if (f.mode !== 'all' && modeKey(m) !== f.mode) return false;
      if (f.result !== 'all' && m.result !== f.result) return false;
      if (f.tag !== 'all' && (m.tag || 'other') !== f.tag) return false;
      if (!f.includeOffline && m.online === false) return false;
      if (tOfM(m) < minT) return false;
      if (terms.length) {
        var h = matchHaystack(m);
        for (var i = 0; i < terms.length; i++) if (h.indexOf(terms[i]) < 0) return false;
      }
      return true;
    });
  }

  function manualModeKey(mode) { return (mode === '1v1' || mode === '2v2' || mode === '3v3') ? mode : 'other'; }
  /** Manual entries shown in the history: they have no result detail, players, arena nor tag,
   *  so they are hidden as soon as a result, tag or text filter is active. */
  function filterManualHistory(manual, f, now) {
    f = histFilters(f);
    now = isNum(now) ? now : Date.now();
    if (f.result !== 'all' || f.tag !== 'all' || searchTerms(f.q).length) return [];
    var minT = histMinT(f, now);
    return (manual || []).filter(function (e) {
      var t = e ? parseDay(e.day) : null;
      if (t == null || !(num(e.games) > 0)) return false;
      if (f.mode !== 'all' && manualModeKey(e.mode) !== f.mode) return false;
      return addDays(t, 1) > minT;
    });
  }

  /** Groups matches (+ manual entries) by local day, newest day and newest match first.
   *  Day totals include the manual games (manualGames > 0 tells it); diff = sum of goal_diff of decided matches. */
  function groupHistory(ms, manual) {
    var byKey = {};
    function grp(key) {
      return byKey[key] || (byKey[key] = { key: key, t: parseDay(key), matches: [], manual: [], games: 0, wins: 0, losses: 0, abandoned: 0, diff: 0, decided: 0, manualGames: 0 });
    }
    (ms || []).forEach(function (m) {
      var d = grp(dayKey(tOfM(m)));
      d.matches.push(m);
      d.games++;
      if (m.result === 'win') d.wins++; else if (m.result === 'loss') d.losses++; else d.abandoned++;
      if (isDecided(m)) { d.decided++; if (isNum(m.goal_diff)) d.diff += m.goal_diff; }
    });
    (manual || []).forEach(function (e) {
      if (!e || parseDay(e.day) == null) return;
      var g = Math.round(num(e.games));
      if (g <= 0) return;
      var w = Math.min(g, Math.max(0, Math.round(num(e.wins))));
      var d = grp(e.day);
      d.manual.push({ day: e.day, mode: e.mode, games: g, wins: w, losses: g - w });
      d.games += g; d.wins += w; d.losses += g - w; d.manualGames += g;
    });
    return Object.keys(byKey).sort().reverse().map(function (k) {
      var d = byKey[k];
      d.matches.sort(function (a, b) { return tOfM(b) - tOfM(a) || num(b.id) - num(a.id); });
      d.manual.sort(function (a, b) { return String(a.mode).localeCompare(String(b.mode)); });
      return d;
    });
  }

  /** First `limit` matches of the grouped list (days cut mid-way when needed). Manual-only days are kept
   *  while no further match is hidden before them. */
  function paginateGroups(groups, limit) {
    var out = [], shown = 0, total = 0;
    (groups || []).forEach(function (g) { total += g.matches.length; });
    for (var i = 0; i < (groups || []).length; i++) {
      var g = groups[i];
      if (shown >= limit && g.matches.length) break;
      var take = g.matches.slice(0, Math.max(0, limit - shown));
      shown += take.length;
      out.push(Object.assign({}, g, { shownMatches: take, truncated: take.length < g.matches.length }));
    }
    return { groups: out, shown: shown, total: total, remaining: total - shown };
  }

  function summarizeHistory(ms, manual) {
    var r = wl(ms || []);
    var mg = 0;
    (manual || []).forEach(function (e) { mg += Math.max(0, Math.round(num(e && e.games))); });
    return {
      n: (ms || []).length, wins: r.wins, losses: r.losses, wr: r.wr,
      abandoned: (ms || []).filter(function (m) { return m.result === 'abandoned'; }).length,
      diffAvg: mean((ms || []).filter(isDecided).map(function (m) { return m.goal_diff; })),
      manualGames: mg
    };
  }

  function indexOfId(all, id) {
    for (var i = 0; i < (all || []).length; i++) if (all[i] && all[i].id === id) return i;
    return -1;
  }
  /** Chronological neighbours across all matches (all = annotate() output). */
  function matchNeighbors(all, id) {
    var i = indexOfId(all, id);
    return { index: i, total: (all || []).length, prev: i > 0 ? all[i - 1] : null, next: i >= 0 && i < all.length - 1 ? all[i + 1] : null };
  }
  /** Session position, previous match of the session and win/loss streak right after this match (all modes). */
  function matchContext(all, id) {
    var i = indexOfId(all, id);
    if (i < 0) return null;
    var m = all[i], n = 0;
    all.forEach(function (x) { if (x._sess === m._sess) n++; });
    return {
      sessIdx: m._sessIdx || 1, sessN: n || 1,
      prevInSession: (m._sessIdx || 1) > 1 && i > 0 ? all[i - 1] : null,
      streak: streaks(all.slice(0, i + 1)).current
    };
  }

  var PERF_STATS = [
    { key: 'score', label: 'Score', digits: 0, higherBetter: true },
    { key: 'goals', label: 'Buts', digits: 0, higherBetter: true },
    { key: 'assists', label: 'Passes', digits: 0, higherBetter: true },
    { key: 'saves', label: 'Arrêts', digits: 0, higherBetter: true },
    { key: 'shots', label: 'Tirs', digits: 0, higherBetter: true },
    { key: 'touches', label: 'Touches', digits: 0, higherBetter: true },
    { key: 'demos', label: 'Démolitions', digits: 0, higherBetter: true }
  ];
  var MOVE_STATS = [
    { key: 'avg_speed', label: 'Vitesse moyenne', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'supersonic_pct', label: 'Supersonique', unit: '%', digits: 1, higherBetter: true },
    { key: 'avg_boost', label: 'Boost moyen', unit: '', digits: 0, higherBetter: null },
    { key: 'zero_boost_pct', label: 'À 0 boost', unit: '%', digits: 1, higherBetter: false },
    { key: 'full_boost_pct', label: 'À 100 boost', unit: '%', digits: 1, higherBetter: null },
    { key: 'boosting_pct', label: 'Boost actif', unit: '%', digits: 1, higherBetter: null },
    { key: 'powerslide_pct', label: 'Powerslide', unit: '%', digits: 1, higherBetter: null },
    { key: 'demolished_s', label: 'Temps démoli', unit: 's', digits: 1, higherBetter: false },
    { key: 'ground_pct', label: 'Au sol', unit: '%', digits: 1, split: true },
    { key: 'wall_pct', label: 'Sur les murs', unit: '%', digits: 1, split: true },
    { key: 'air_pct', label: 'En l’air', unit: '%', digits: 1, split: true }
  ];
  var HIT_STATS = [
    { key: 'count', label: 'Frappes', unit: '', digits: 0, higherBetter: null },
    { key: 'avg_speed', label: 'Frappe moyenne', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'max_speed', label: 'Frappe max.', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true }
  ];
  /** Value of stat s for match m from source 'me' | 'movement' | 'hits' (converted, null when missing). */
  function statValue(m, src, s) {
    var o = m ? m[src] : null;
    var raw = o ? o[s.key] : null;
    if (!isNum(raw)) return null;
    return s.conv ? s.conv(raw) : raw;
  }
  /** Averages of my previous (up to maxN) decided matches of the same mode & variant, strictly before match `id`
   *  in chronological order (all = annotate() output). Each average carries its own sample size n. */
  function matchBaseline(all, id, maxN) {
    maxN = maxN || 50;
    var i = indexOfId(all, id);
    if (i < 0) return null;
    var m = all[i], prev = [];
    var variant = m.variant || 'Soccar';
    for (var j = i - 1; j >= 0 && prev.length < maxN; j--) {
      var x = all[j];
      if (x && isDecided(x) && x.mode === m.mode && (x.variant || 'Soccar') === variant) prev.push(x);
    }
    function avgs(src, list) {
      var o = {};
      list.forEach(function (s) {
        var vals = prev.map(function (x) { return statValue(x, src, s); }).filter(isNum);
        o[s.key] = { avg: mean(vals), n: vals.length };
      });
      return o;
    }
    return {
      n: prev.length, mode: m.mode,
      me: avgs('me', PERF_STATS), movement: avgs('movement', MOVE_STATS), hits: avgs('hits', HIT_STATS)
    };
  }

  /* ---------- formatting (pure) ---------- */
  var nfCache = {};
  function nf(d) {
    var k = String(d);
    if (!nfCache[k]) nfCache[k] = new Intl.NumberFormat('fr-FR', { minimumFractionDigits: d, maximumFractionDigits: d });
    return nfCache[k];
  }
  function fmtNum(x, d) { return isNum(x) ? nf(d || 0).format(x) : '—'; }
  function fmtPct(r, d) { return isNum(r) ? nf(d || 0).format(r * 100) + '\u202f%' : '—'; }
  function fmtPct100(x, d) { return isNum(x) ? nf(d || 0).format(x) + '\u202f%' : '—'; }
  function fmtSigned(x, d) {
    if (!isNum(x)) return '—';
    var s = nf(d || 0).format(Math.abs(x));
    if (s === nf(d || 0).format(0)) return s;
    return (x > 0 ? '+' : '−') + s;
  }
  function fmtClock(sec) {
    if (!isNum(sec)) return '—';
    sec = Math.max(0, Math.round(sec));
    return Math.floor(sec / 60) + ':' + String(sec % 60).padStart(2, '0');
  }
  function fmtHours(sec) {
    if (!isNum(sec) || sec <= 0) return '0 min';
    var h = Math.floor(sec / 3600), mnt = Math.round((sec % 3600) / 60);
    return h ? h + '\u202fh' + (mnt ? ' ' + String(mnt).padStart(2, '0') : '') : mnt + '\u202fmin';
  }

  var api = {
    ROLL_WINDOW: ROLL_WINDOW, SESSION_GAP_MS: SESSION_GAP_MS, UU_TO_KMH: UU_TO_KMH, TAGS: TAGS, TAG_LABELS: TAG_LABELS,
    DEFAULT_FILTERS: DEFAULT_FILTERS, MECH_METRICS: MECH_METRICS,
    isNum: isNum, ratio: ratio, regulationS: regulationS, mean: mean, wl: wl, kmh: kmh, annotate: annotate, modeKey: modeKey, filterMatches: filterMatches,
    streaks: streaks, computeKpis: computeKpis, rolling: rolling, computeProgression: computeProgression,
    computeActivity: computeActivity, computeGoalDiffDist: computeGoalDiffDist, computeMental: computeMental,
    computeSituations: computeSituations, computeGoalsPerMinute: computeGoalsPerMinute, computeMechanics: computeMechanics,
    computeStatfeed: computeStatfeed, statfeedLabel: statfeedLabel, prettyArena: prettyArena, computeArenas: computeArenas,
    computeTeammates: computeTeammates, computeGoal: computeGoal, computeTimeInsights: computeTimeInsights, trackedByMode: trackedByMode, trackerUrl: trackerUrl, fmtNum: fmtNum, fmtPct: fmtPct, fmtPct100: fmtPct100, fmtSigned: fmtSigned,
    fmtClock: fmtClock, fmtHours: fmtHours, dayKey: dayKey,
    parseRoute: parseRoute, DEFAULT_HIST_FILTERS: DEFAULT_HIST_FILTERS, filterHistory: filterHistory, filterManualHistory: filterManualHistory,
    groupHistory: groupHistory, paginateGroups: paginateGroups, summarizeHistory: summarizeHistory, matchNeighbors: matchNeighbors,
    matchContext: matchContext, matchBaseline: matchBaseline, statValue: statValue, PERF_STATS: PERF_STATS, MOVE_STATS: MOVE_STATS, HIT_STATS: HIT_STATS
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.RT = api;

  if (typeof window === 'undefined' || typeof document === 'undefined') return;

  /* ===================================================================== */
  /* Part 2 — UI                                                           */
  /* ===================================================================== */

  var $ = function (sel, el) { return (el || document).querySelector(sel); };
  var $$ = function (sel, el) { return Array.prototype.slice.call((el || document).querySelectorAll(sel)); };
  function playerName(name, primaryId) {
    var url = trackerUrl(name, primaryId);
    var label = esc(name || '?');
    return url ? '<a class="trn" href="' + esc(url) + '" target="_blank" rel="noopener noreferrer" title="Voir le profil tracker.gg (MMR, rangs, peaks)">' + label + '</a>' : label;
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  var params = new URLSearchParams(location.search);
  var MOCK = params.get('mock');
  var PAGE_SIZE = 20;

  var state = {
    all: [],
    filtered: [],
    filters: loadFilters(),
    status: null,
    statusError: false,
    lastCount: null,
    shown: PAGE_SIZE,
    open: {},
    loaded: false,
    config: null,
    manual: [],
    route: parseRoute(location.hash),
    hist: loadHistFilters(),
    histShown: 100,
    histScroll: 0
  };
  var HIST_PAGE = 100;
  var charts = {};

  function loadFilters() {
    var f = Object.assign({}, DEFAULT_FILTERS);
    try {
      var s = JSON.parse(localStorage.getItem('rt.filters') || 'null');
      if (s && typeof s === 'object') Object.keys(f).forEach(function (k) { if (s[k] != null) f[k] = s[k]; });
    } catch (e) { /* storage unavailable */ }
    return f;
  }
  function saveFilters() { try { localStorage.setItem('rt.filters', JSON.stringify(state.filters)); } catch (e) { /* ignore */ } }
  function loadHistFilters() {
    var f = Object.assign({}, DEFAULT_HIST_FILTERS);
    try {
      var s = JSON.parse(localStorage.getItem('rt.history.filters') || 'null');
      if (s && typeof s === 'object') Object.keys(f).forEach(function (k) { if (s[k] != null && typeof s[k] === typeof f[k]) f[k] = s[k]; });
    } catch (e) { /* storage unavailable */ }
    return f;
  }
  function saveHistFilters() { try { localStorage.setItem('rt.history.filters', JSON.stringify(state.hist)); } catch (e) { /* ignore */ } }

  /* ---------- API (real or mock) ---------- */
  var mock = null, mockReady = null;
  function apiFetch(path, opts) {
    if (MOCK) return mockFetch(path, opts || {});
    return fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' }, cache: 'no-store' }, opts || {}))
      .then(function (r) {
        if (!r.ok) {
          return r.text().then(function (txt) {
            var detail = '';
            try { detail = (JSON.parse(txt) || {}).error || ''; } catch (e) { /* not JSON */ }
            throw new Error('HTTP ' + r.status + (detail ? ' : ' + detail : ''));
          });
        }
        if (r.status === 204) return null;
        var ct = r.headers.get('content-type') || '';
        return ct.indexOf('json') >= 0 ? r.json() : r.text();
      });
  }

  function mockFetch(path, opts) {
    var method = (opts.method || 'GET').toUpperCase();
    var ready = mockReady || (mockReady = (function () {
      mock = { matches: [], config: { player_names: ['Virgile'], player_ids: ['Epic|8f3a21c9|0'], default_tag: 'ranked', rl_install_dir: '', dashboard_port: 8765, rl_port: 49123, goal: { mode: '1v1', daily: 10, season_start: '', season_end: '' } }, t0: Date.now() };
      if (MOCK === 'empty') return Promise.resolve();
      var src = params.get('mockdata') || '../devtools/mock/mock-matches.json';
      return fetch(src, { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) {
        mock.matches = d;
        // Demo-only seeds: a few hand-entered days and one match tracked from mid-game (partial).
        if (!d.length || params.get('mockseed') === '0') return;
        var lastT = Date.parse(d[d.length - 1].started_at);
        mock.manual = [
          { day: dayKey(lastT), mode: '1v1', games: 7, wins: 4 },
          { day: dayKey(addDays(lastT, -2)), mode: '2v2', games: 3, wins: 1 },
          { day: dayKey(addDays(lastT, -9)), mode: '1v1', games: 5, wins: 3 }
        ];
        var p = d.slice().reverse().find(function (m) { return (m.goals || []).length >= 3 && m.result !== 'abandoned'; });
        if (p && !d.some(function (m) { return m.partial; })) { p.partial = true; p.goals = p.goals.slice(2); }
      });
    })());
    return ready.then(function () {
      var m;
      if (path === '/api/matches' && method === 'GET') return JSON.parse(JSON.stringify(mock.matches));
      if ((m = path.match(/^\/api\/matches\/(\d+)$/))) {
        var id = +m[1];
        var idx = mock.matches.findIndex(function (x) { return x.id === id; });
        if (idx < 0) throw new Error('HTTP 404');
        if (method === 'DELETE') { mock.matches.splice(idx, 1); return null; }
        if (method === 'PATCH') { Object.assign(mock.matches[idx], JSON.parse(opts.body || '{}')); return mock.matches[idx]; }
      }
      if (path === '/api/manual' && method === 'GET') return JSON.parse(JSON.stringify(mock.manual || []));
      if ((m = path.match(/^\/api\/manual\/(\d{4}-\d{2}-\d{2})\/(\w+)$/)) && method === 'PUT') {
        var b = JSON.parse(opts.body || '{}');
        mock.manual = (mock.manual || []).filter(function (e) { return !(e.day === m[1] && e.mode === m[2]); });
        if (b.games > 0) mock.manual.push({ day: m[1], mode: m[2], games: b.games, wins: b.wins || 0 });
        return { day: m[1], mode: m[2], games: b.games, wins: b.wins || 0 };
      }
      if (path === '/api/config') {
        if (method === 'PUT') { mock.config = JSON.parse(opts.body); return mock.config; }
        return mock.config;
      }
      if (path === '/api/status') {
        var ms = params.get('mockstatus') || 'live';
        var el = ((Date.now() - mock.t0) / 1000 + 117) % 330;
        var live = ms === 'live' ? {
          mode: '2v2', arena: 'EuroStadium_Night_P', time_seconds: Math.max(0, Math.round(300 - el)), overtime: false,
          team_score: 2, opp_score: 1, my_team: 0, me: { name: 'Virgile', score: 286, goals: 1, shots: 3, assists: 1, saves: 2 }
        } : null;
        return {
          version: '0.1.0-mock', connected: ms !== 'offline' && ms !== 'noini', transport: ms === 'offline' || ms === 'noini' ? '' : 'ws',
          in_match: !!live, live: live,
          ini: ms === 'noini' ? { path: 'C:\\Program Files\\Epic Games\\rocketleague\\TAGame\\Config\\DefaultStatsAPI.ini', found: true, packet_send_rate: 0, ok: false }
            : { path: 'C:\\Program Files\\Epic Games\\rocketleague\\TAGame\\Config\\DefaultStatsAPI.ini', found: true, packet_send_rate: 30, ok: true },
          identity: { names: mock.config.player_names, ids: mock.config.player_ids },
          match_count: mock.matches.length
        };
      }
      throw new Error('mock: unknown ' + method + ' ' + path);
    });
  }

  /* ---------- theme & Chart.js defaults ---------- */
  function cssVar(n) { return getComputedStyle(document.documentElement).getPropertyValue(n).trim(); }
  var T = {};
  function readTheme() {
    ['us', 'them', 'win', 'loss', 'text', 'text-2', 'muted', 'grid', 'axis', 'surface', 'surface-2', 'surface-3', 'neutral', 'border-strong', 'warn']
      .forEach(function (k) { T[k] = cssVar('--' + k); });
    var light = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
    // Categorical slots for the ground/wall/air split (validated: blue / aqua / yellow, both modes).
    T.series = light ? ['#2a78d6', '#1baf7a', '#eda100'] : ['#3987e5', '#199e70', '#c98500'];
    T.font = cssVar('--font') || 'system-ui, sans-serif';
  }
  function alpha(color, a) {
    if (!color) return color;
    if (color[0] === '#') {
      var h = color.length === 4 ? color.replace(/#(.)(.)(.)/, '#$1$1$2$2$3$3') : color;
      var r = parseInt(h.substr(1, 2), 16), g = parseInt(h.substr(3, 2), 16), b = parseInt(h.substr(5, 2), 16);
      return 'rgba(' + r + ',' + g + ',' + b + ',' + a + ')';
    }
    return color;
  }

  var refLinePlugin = {
    id: 'refLine',
    beforeDatasetsDraw: function (chart, args, opts) {
      if (!opts || opts.value == null) return;
      var scale = chart.scales[opts.axis || 'y'];
      if (!scale) return;
      var ca = chart.chartArea, ctx = chart.ctx;
      var p = scale.getPixelForValue(opts.value);
      if (p < ca.top - 1 || p > ca.bottom + 1) return;
      ctx.save();
      ctx.strokeStyle = opts.color || T['border-strong'];
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(ca.left, Math.round(p) + 0.5); ctx.lineTo(ca.right, Math.round(p) + 0.5); ctx.stroke();
      if (opts.label) {
        ctx.fillStyle = T.muted; ctx.font = '11px ' + T.font; ctx.textAlign = 'left'; ctx.textBaseline = 'bottom';
        ctx.fillText(opts.label, ca.left + 4, p - 3);
      }
      ctx.restore();
    }
  };
  var crosshairPlugin = {
    id: 'crosshair',
    afterDraw: function (chart, args, opts) {
      if (!opts || !opts.enabled) return;
      var act = chart.tooltip && chart.tooltip.getActiveElements ? chart.tooltip.getActiveElements() : [];
      if (!act.length) return;
      var x = act[0].element.x, ca = chart.chartArea, ctx = chart.ctx;
      ctx.save(); ctx.strokeStyle = T.axis; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(Math.round(x) + 0.5, ca.top); ctx.lineTo(Math.round(x) + 0.5, ca.bottom); ctx.stroke();
      ctx.restore();
    }
  };

  function applyChartDefaults() {
    if (!window.Chart) return;
    var C = window.Chart;
    C.defaults.font.family = T.font;
    C.defaults.font.size = 12;
    C.defaults.color = T.muted;
    C.defaults.borderColor = T.grid;
    C.defaults.maintainAspectRatio = false;
    C.defaults.responsive = true;
    C.defaults.animation = false; // instant, and robust when rAF is throttled (background tabs, headless)
    C.defaults.plugins.legend.display = false;
    var tt = C.defaults.plugins.tooltip;
    tt.backgroundColor = T['surface-3'];
    tt.borderColor = T['border-strong'];
    tt.borderWidth = 1;
    tt.titleColor = T.text;
    tt.bodyColor = T['text-2'];
    tt.footerColor = T.muted;
    tt.titleFont = { weight: '600', size: 12.5 };
    tt.bodyFont = { size: 12.5 };
    tt.footerFont = { size: 11.5, weight: '400' };
    tt.padding = 10;
    tt.cornerRadius = 8;
    tt.boxPadding = 5;
    tt.usePointStyle = true;
    tt.caretSize = 5;
    C.defaults.elements.line.borderWidth = 2;
    C.defaults.elements.line.borderCapStyle = 'round';
    C.defaults.elements.line.borderJoinStyle = 'round';
    C.defaults.elements.point.radius = 0;
    C.defaults.elements.point.hoverRadius = 5;
    C.defaults.elements.point.hoverBorderWidth = 2;
    C.defaults.elements.bar.borderRadius = 4;
    C.defaults.elements.bar.borderSkipped = 'start';
  }

  function baseScales(opts) {
    opts = opts || {};
    return {
      x: Object.assign({
        grid: { display: false, drawTicks: false },
        border: { color: T.axis },
        ticks: { color: T.muted, maxRotation: 0, autoSkip: true, autoSkipPadding: 12, padding: 6 }
      }, opts.x || {}),
      y: Object.assign({
        grid: { color: T.grid, drawTicks: false },
        border: { display: false },
        ticks: { color: T.muted, padding: 8, maxTicksLimit: 6 }
      }, opts.y || {})
    };
  }

  function chartEmpty(id, msg) {
    var canvas = document.getElementById(id);
    if (!canvas) return;
    var box = canvas.parentElement;
    var e = box.querySelector('.chart-empty');
    if (msg) {
      if (!e) { e = document.createElement('div'); e.className = 'chart-empty'; box.appendChild(e); }
      e.textContent = msg;
      canvas.style.visibility = 'hidden';
    } else {
      if (e) e.remove();
      canvas.style.visibility = '';
    }
  }

  function makeChart(id, config, emptyMsg) {
    if (charts[id]) { charts[id].destroy(); delete charts[id]; }
    if (!window.Chart) return null;
    chartEmpty(id, emptyMsg || null);
    if (emptyMsg) return null;
    var canvas = document.getElementById(id);
    if (!canvas) return null;
    config.plugins = (config.plugins || []).concat([refLinePlugin, crosshairPlugin]);
    charts[id] = new window.Chart(canvas, config);
    return charts[id];
  }

  function legendHtml(items) {
    return items.map(function (it) {
      return '<span><i class="' + (it.line ? 'line' : '') + '" style="background:' + it.color + '"></i>' + esc(it.label) + '</span>';
    }).join('');
  }

  function fmtDate(t, withTime) {
    var d = new Date(t);
    var o = { weekday: 'short', day: 'numeric', month: 'short' };
    if (withTime) { o.hour = '2-digit'; o.minute = '2-digit'; }
    return d.toLocaleString('fr-FR', o);
  }
  function hasData(arr) { return arr.some(function (v) { return v != null; }); }

  /* ---------- render: season goal ---------- */
  var GOAL_MODE_LABEL = { '1v1': '1v1', '2v2': '2v2', '3v3': '3v3', '4v4': '4v4', all: 'tous modes' };
  function plural(n, w) { return fmtNum(n) + ' ' + w + (n > 1 ? 's' : ''); }
  function goalKpi(label, value, sub, pct, cls) {
    return '<div class="card kpi goal-kpi' + (cls ? ' ' + cls : '') + '"><span class="kpi-label">' + label + '</span>' +
      '<span class="kpi-value">' + value + '</span>' +
      (pct != null ? '<div class="goal-meter" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(Math.min(1, pct) * 100) + '"><span style="width:' + (Math.min(1, Math.max(0, pct)) * 100).toFixed(1) + '%"></span></div>' : '') +
      '<span class="kpi-sub">' + sub + '</span></div>';
  }
  function renderGoal() {
    var g = computeGoal(state.all, state.config && state.config.goal, null, state.manual);
    var modeLbl = GOAL_MODE_LABEL[g.mode] || g.mode;
    var fmtD = function (t) { return new Date(t).toLocaleDateString('fr-FR', { day: 'numeric', month: 'short', year: 'numeric' }); };
    $('#goal-sub').textContent = g.daily + ' games de ' + modeLbl + ' par jour · du ' + fmtD(g.start) + ' au ' + fmtD(g.end) + ' (' + g.days.length + ' jours)';

    var t = g.today, left = Math.max(0, g.daily - t.count);
    var todaySub = g.before ? 'La saison commence le ' + fmtD(g.start)
      : g.after ? 'Saison terminée'
      : left === 0 ? '<span class="pos">Objectif du jour atteint ✓</span>' + (t.count > g.daily ? ' (+' + (t.count - g.daily) + ')' : '')
      : 'Encore ' + plural(left, 'game') + ' aujourd’hui';
    if (t.count) todaySub += ' · ' + t.wins + ' V – ' + t.losses + ' D';
    var html =
      goalKpi('Aujourd’hui', fmtNum(t.count) + '<small> / ' + g.daily + '</small>', todaySub, t.count / g.daily, left === 0 && !g.before && !g.after ? 'done' : '') +
      goalKpi('Games de la saison', fmtNum(g.total) + '<small> / ' + fmtNum(g.target) + '</small>',
        'Jour ' + g.elapsedDays + ' sur ' + g.days.length + (g.wr != null ? ' · ' + fmtPct(g.wr) + ' de victoires' : ''), g.target ? g.total / g.target : 0) +
      goalKpi('Jours validés', fmtNum(g.daysMet) + '<small> / ' + fmtNum(g.elapsedDays) + '</small>',
        g.elapsedDays ? fmtPct(g.daysMet / g.elapsedDays) + ' des jours écoulés' : 'Aucun jour écoulé', null) +
      goalKpi('Série en cours', plural(g.curStreak, 'jour'), 'Record : ' + plural(g.bestStreak, 'jour'), null, g.curStreak > 0 ? 'streak' : '') +
      goalKpi('Rythme', g.avgPerDay != null ? fmtNum(g.avgPerDay, 1) + '<small> / jour</small>' : '—',
        g.projection != null ? 'Projection fin de saison : <b>' + fmtNum(g.projection) + '</b> games' : 'Pas encore de données', null,
        g.projection != null ? (g.projection >= g.target ? 'on-track' : 'behind') : '') +
      goalKpi('Pour finir à ' + fmtNum(g.target), g.neededPerDay != null ? fmtNum(g.neededPerDay) + '<small> / jour</small>' : '—',
        g.neededPerDay == null ? 'Saison terminée' : g.total >= g.target ? '<span class="pos">Objectif de saison atteint ✓</span>' : plural(g.target - g.total, 'game') + ' restantes sur ' + plural(g.remainingDays, 'jour'), null);
    $('#goal-kpis').innerHTML = html;
    $('#goal-hint').hidden = !!(state.config && state.config.goal && state.config.goal.season_start);

    // One grid per month, Monday first.
    var months = [], cur = null;
    g.days.forEach(function (d) {
      var dt = new Date(d.t), mk = dt.getFullYear() * 12 + dt.getMonth();
      if (!cur || cur.mk !== mk) { cur = { mk: mk, days: [] }; months.push(cur); }
      cur.days.push(d);
    });
    var dow = ['L', 'M', 'M', 'J', 'V', 'S', 'D'];
    $('#goal-cal').innerHTML = months.map(function (mo) {
      var first = new Date(mo.days[0].t);
      var lead = (first.getDay() + 6) % 7;
      var cells = '';
      for (var i = 0; i < lead; i++) cells += '<span class="cal-day blank" aria-hidden="true"></span>';
      mo.days.forEach(function (d) {
        var cls = 'cal-day';
        if (d.future) cls += ' future';
        else if (d.met) cls += ' met';
        else if (d.count === 0) cls += d.today ? ' l0' : ' miss';
        else cls += ' l' + Math.min(3, 1 + Math.floor(d.count / g.daily * 3));
        if (d.today) cls += ' today';
        if (d.manual) cls += ' has-manual';
        var tip = new Date(d.t).toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' }) + ' · ' +
          (d.future ? 'à venir' : plural(d.count, 'game') + (d.count ? ' (' + d.wins + ' V – ' + d.losses + ' D)' : '') + (d.manual ? ' · dont ' + d.manual + ' saisie' + (d.manual > 1 ? 's' : '') + ' à la main' : '') + (d.met ? ' · objectif atteint' : ''));
        var inner = '<span class="dn">' + new Date(d.t).getDate() + '</span>' + (d.future ? '' : '<span class="dc">' + d.count + '</span>');
        cells += d.future
          ? '<span class="' + cls + '" title="' + esc(tip) + '" aria-label="' + esc(tip) + '">' + inner + '</span>'
          : '<button type="button" class="' + cls + '" data-day="' + d.key + '" title="' + esc(tip + ' · cliquer pour saisir des games') + '" aria-label="' + esc(tip + '. Saisir des games') + '">' + inner + '</button>';
      });
      var name = first.toLocaleDateString('fr-FR', { month: 'long', year: 'numeric' });
      var mTotal = mo.days.reduce(function (a, d) { return a + d.count; }, 0);
      var mMet = mo.days.filter(function (d) { return d.met; }).length;
      return '<div class="card cal-month"><div class="cal-head"><span class="card-title">' + esc(name.charAt(0).toUpperCase() + name.slice(1)) + '</span>' +
        '<span class="card-meta">' + plural(mTotal, 'game') + ' · ' + mMet + ' j validés</span></div>' +
        '<div class="cal-dow">' + dow.map(function (x) { return '<span>' + x + '</span>'; }).join('') + '</div>' +
        '<div class="cal-days">' + cells + '</div></div>';
    }).join('');
  }

  /* ---------- render: KPIs ---------- */
  function renderKpis(ms) {
    var k = computeKpis(ms);
    var st = k.streaks;
    var recent = ms.slice(-20);
    var curTxt = st.current ? st.current.len + (st.current.type === 'win' ? ' V' : ' D') : '—';
    var curCls = st.current ? (st.current.type === 'win' ? 'w' : 'l') : '';
    var curLabel = st.current ? (st.current.type === 'win' ? (st.current.len > 1 ? 'victoires d’affilée' : 'victoire') : (st.current.len > 1 ? 'défaites d’affilée' : 'défaite')) : '';
    var form = recent.map(function (m) {
      var cls = m.result === 'win' ? 'win' : m.result === 'loss' ? 'loss' : 'abandoned';
      var letter = m.result === 'win' ? 'V' : m.result === 'loss' ? 'D' : 'A';
      var tip = fmtDate(m._t, true) + ' · ' + (m.mode || '') + ' · ' + num(m.team_score) + '–' + num(m.opp_score);
      return '<span class="form-cell ' + cls + '" title="' + esc(tip) + '">' + letter + '</span>';
    }).join('');
    var html = '';
    html += '<div class="card kpi hero">' +
      '<span class="kpi-label">% de victoire</span>' +
      '<span class="kpi-value">' + fmtPct(k.winrate) + '</span>' +
      '<div class="kpi-wl"><span class="wl"><i style="background:var(--win)"></i>' + k.wins + ' V</span>' +
      '<span class="wl"><i style="background:var(--loss)"></i>' + k.losses + ' D</span>' +
      (k.abandoned ? '<span class="wl"><i style="background:var(--neutral)"></i>' + k.abandoned + ' abandon' + (k.abandoned > 1 ? 's' : '') + '</span>' : '') + '</div>' +
      '<div class="form"><div class="form-head"><span>Forme récente</span><span>' + recent.length + ' derniers · récent à droite</span></div>' +
      '<div class="form-strip" style="--cells:' + Math.max(recent.length, 10) + '">' + form + '</div></div>' +
      '</div>';
    html += tile('Matchs', fmtNum(k.n), k.sessions + ' session' + (k.sessions > 1 ? 's' : '') + ' · ' + fmtHours(k.playSeconds) + ' de jeu');
    html += tile('Diff. de buts / match', fmtSigned(k.goalDiffAvg, 2), fmtNum(k.teamGoalsAvg, 1) + ' marqués · ' + fmtNum(k.oppGoalsAvg, 1) + ' encaissés');
    html += tile('Score moyen', fmtNum(k.avgScore), 'Meilleur : ' + fmtNum(k.bestScore));
    html += tile('Série en cours', '<span class="streak-badge ' + curCls + '">' + curTxt + '</span>',
      (curLabel ? esc(curLabel) + ' · ' : '') + 'record ' + (st.bestWin ? st.bestWin + ' V' : '—'));
    html += '<div class="card kpi per-match"><span class="kpi-label">Par match</span><div class="pm-grid">' +
      pm('Buts', k.goalsPer) + pm('Passes', k.assistsPer) + pm('Arrêts', k.savesPer) + pm('Tirs', k.shotsPer) + '</div></div>';
    html += tile('Conversion des tirs', fmtPct(k.conversion), fmtNum(k.goals) + ' buts sur ' + fmtNum(k.shots) + ' tirs cadrés');
    html += tile('Taux de MVP', fmtPct(k.mvpRate), k.mvp + ' MVP · ' + fmtPct(k.mvpOfWins) + ' des victoires');
    $('#kpis').innerHTML = html;

    function tile(label, value, sub) {
      return '<div class="card kpi"><span class="kpi-label">' + esc(label) + '</span><span class="kpi-value">' + value + '</span>' +
        '<span class="kpi-sub">' + sub + '</span></div>';
    }
    function pm(label, v) { return '<div class="pm"><span class="pm-v">' + fmtNum(v, 2) + '</span><span class="pm-l">' + label + '</span></div>'; }
    function num(x) { return isNum(x) ? x : 0; }
  }

  /* ---------- render: progression ---------- */
  function rollingLine(id, data, color, ms, o) {
    o = o || {};
    var labels = ms.map(function (m, i) { return i + 1; });
    var empty = hasData(data) ? null : 'Pas encore assez de matchs (min. ' + ROLL_MIN + ')';
    return makeChart(id, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [{
          label: o.label, data: data, borderColor: color, backgroundColor: alpha(color, 0.10), fill: o.fill ? 'origin' : false,
          tension: 0.3, cubicInterpolationMode: 'monotone', pointHoverBackgroundColor: color, pointHoverBorderColor: T.surface, spanGaps: true
        }]
      },
      options: {
        interaction: { mode: 'index', intersect: false },
        scales: baseScales({ x: { ticks: { maxTicksLimit: 8, color: T.muted } }, y: o.y || {} }),
        plugins: {
          refLine: o.ref != null ? { value: o.ref, label: o.refLabel } : {},
          crosshair: { enabled: true },
          tooltip: {
            callbacks: {
              title: function (it) { var m = ms[it[0].dataIndex]; return 'Match n°' + (it[0].dataIndex + 1) + ' · ' + fmtDate(m._t, true); },
              label: function (it) { return ' ' + o.label + ' : ' + o.fmt(it.raw); },
              footer: function () { return 'Moyenne des ' + ROLL_WINDOW + ' derniers matchs'; }
            }
          }
        }
      }
    }, empty);
  }

  function lastVal(arr) { for (var i = arr.length - 1; i >= 0; i--) if (arr[i] != null) return arr[i]; return null; }

  function renderProgression(ms) {
    var p = computeProgression(ms);
    rollingLine('c-roll-wr', p.winrate, T.us, ms, {
      label: '% victoire', fill: true, ref: 50, refLabel: '50 %', fmt: function (v) { return fmtPct100(v); },
      y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: function (v) { return v + ' %'; } } }
    });
    $('#m-roll-wr').textContent = 'Actuel : ' + fmtPct100(lastVal(p.winrate));
    rollingLine('c-roll-gd', p.goalDiff, T.us, ms, {
      label: 'Diff. de buts', ref: 0, fmt: function (v) { return fmtSigned(v, 2); },
      y: { suggestedMin: -1.5, suggestedMax: 1.5, ticks: { color: T.muted, padding: 8, maxTicksLimit: 6, callback: function (v) { return fmtSigned(v, Number.isInteger(v) ? 0 : 1); } } }
    });
    $('#m-roll-gd').textContent = 'Actuel : ' + fmtSigned(lastVal(p.goalDiff), 2);
    rollingLine('c-roll-score', p.score, T.us, ms, { label: 'Score', fmt: function (v) { return fmtNum(v); }, y: { grace: '10%' } });
    $('#m-roll-score').textContent = 'Actuel : ' + fmtNum(lastVal(p.score));

    // Small multiples (one series each) rather than 4 overlapping colored lines.
    [['goals', 'Buts'], ['assists', 'Passes'], ['saves', 'Arrêts'], ['shots', 'Tirs']].forEach(function (x) {
      var data = p[x[0]];
      $('#mv-' + x[0]).textContent = fmtNum(lastVal(data), 2);
      makeChart('c-roll-' + x[0], {
        type: 'line',
        data: { labels: ms.map(function (m, i) { return i + 1; }), datasets: [{ label: x[1], data: data, borderColor: T.us, backgroundColor: alpha(T.us, 0.10), fill: 'origin',
          pointHoverBackgroundColor: T.us, pointHoverBorderColor: T.surface, tension: 0.3, cubicInterpolationMode: 'monotone', spanGaps: true }] },
        options: {
          interaction: { mode: 'index', intersect: false },
          scales: baseScales({ x: { display: false }, y: { beginAtZero: true, ticks: { color: T.muted, padding: 6, maxTicksLimit: 3, callback: function (v) { return fmtNum(v, Number.isInteger(v) ? 0 : 1); } } } }),
          plugins: {
            crosshair: { enabled: true },
            tooltip: { displayColors: false, callbacks: {
              title: function (it) { var m = ms[it[0].dataIndex]; return 'Match n°' + (it[0].dataIndex + 1) + ' · ' + fmtDate(m._t, true); },
              label: function (it) { return x[1] + ' / match : ' + fmtNum(it.raw, 2); }
            } }
          }
        }
      }, hasData(data) ? null : 'Pas assez de matchs');
    });
  }

  /* ---------- render: activity ---------- */
  function renderActivity(ms) {
    var a = computeActivity(ms, state.filters.period);
    var weekly = a.unit === 'week';
    $('#act-sub').textContent = weekly ? 'Matchs joués et % de victoire par semaine' : 'Matchs joués et % de victoire par jour';
    var labels = a.buckets.map(function (b) { return new Date(b.t).toLocaleDateString('fr-FR', { day: 'numeric', month: 'short' }); });
    var titleOf = function (i) {
      var b = a.buckets[i];
      return weekly ? 'Semaine du ' + new Date(b.t).toLocaleDateString('fr-FR', { day: 'numeric', month: 'long' })
        : new Date(b.t).toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' });
    };
    var hasOther = a.buckets.some(function (b) { return b.other > 0; });
    var items = [{ label: 'Victoires', color: T.win }, { label: 'Défaites', color: T.loss }];
    if (hasOther) items.push({ label: 'Abandons', color: T.neutral });
    $('#lg-act').innerHTML = legendHtml(items);
    var ds = [
      { label: 'Victoires', data: a.buckets.map(function (b) { return b.wins; }), backgroundColor: T.win },
      { label: 'Défaites', data: a.buckets.map(function (b) { return b.losses; }), backgroundColor: T.loss }
    ];
    if (hasOther) ds.push({ label: 'Abandons', data: a.buckets.map(function (b) { return b.other; }), backgroundColor: T.neutral });
    ds.forEach(function (d) { d.maxBarThickness = 24; d.borderColor = T.surface; d.borderWidth = { top: 2, bottom: 0, left: 0, right: 0 }; d.borderRadius = 3; d.borderSkipped = 'start'; d.stack = 's'; });
    makeChart('c-activity', {
      type: 'bar',
      data: { labels: labels, datasets: ds },
      options: {
        interaction: { mode: 'index', intersect: false },
        scales: baseScales({ x: { stacked: true, ticks: { maxTicksLimit: 10, color: T.muted, maxRotation: 0 } }, y: { stacked: true, beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { filter: function (it) { return it.raw > 0; }, callbacks: {
          title: function (it) { return titleOf(it[0].dataIndex); },
          label: function (it) { return ' ' + it.dataset.label + ' : ' + it.raw; },
          footer: function (it) { var b = a.buckets[it[0].dataIndex]; return b.total + ' match' + (b.total > 1 ? 's' : '') + ' · ' + fmtPct(b.winrate) + ' de victoire'; }
        } } }
      }
    }, a.buckets.length ? null : 'Aucune donnée');

    var wr = a.buckets.map(function (b) { return b.winrate == null ? null : b.winrate * 100; });
    makeChart('c-activity-wr', {
      type: 'line',
      data: { labels: labels, datasets: [{ label: '% victoire', data: wr, borderColor: alpha(T.us, 0.55), backgroundColor: T.us, spanGaps: true,
        pointRadius: a.buckets.length > 60 ? 2.5 : 4, pointBackgroundColor: T.us, pointBorderColor: T.surface, pointBorderWidth: 2, pointHoverRadius: 6,
        pointHoverBorderColor: T.surface, tension: 0, borderWidth: 1.5 }] },
      options: {
        interaction: { mode: 'nearest', axis: 'x', intersect: false },
        scales: baseScales({ x: { ticks: { maxTicksLimit: 6, color: T.muted, maxRotation: 0 } }, y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: function (v) { return v + ' %'; } } } }),
        plugins: { refLine: { value: 50 }, tooltip: { callbacks: {
          title: function (it) { return titleOf(it[0].dataIndex); },
          label: function (it) { var b = a.buckets[it.dataIndex]; return ' ' + fmtPct(b.winrate) + ' (' + b.wins + ' V – ' + b.losses + ' D)'; }
        } } }
      }
    }, hasData(wr) ? null : 'Aucun match décidé sur la période');
  }

  /* ---------- render: goals ---------- */
  function renderGoals(ms) {
    var d = computeGoalDiffDist(ms);
    $('#m-gd').textContent = d.n + ' match' + (d.n > 1 ? 's' : '') + ' décidé' + (d.n > 1 ? 's' : '');
    makeChart('c-gd', {
      type: 'bar',
      data: { labels: d.bins.map(function (b) { return b.label; }), datasets: [{
        label: 'Matchs', data: d.bins.map(function (b) { return b.count; }), maxBarThickness: 36,
        backgroundColor: d.bins.map(function (b) { return b.diff > 0 ? T.us : b.diff < 0 ? T.them : T.neutral; })
      }] },
      options: {
        scales: baseScales({ x: { title: { display: true, text: 'Écart final (vous − adversaires)', color: T.muted, font: { size: 11.5 } } }, y: { beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { callbacks: {
          title: function (it) { var b = d.bins[it[0].dataIndex]; return b.diff > 0 ? 'Victoire de ' + b.label.replace('+', '') + ' but' + (Math.abs(b.diff) > 1 ? 's' : '') : b.diff < 0 ? 'Défaite de ' + b.label.replace('−', '').replace('-', '') + ' but' + (Math.abs(b.diff) > 1 ? 's' : '') : 'Égalité'; },
          label: function (it) { return ' ' + it.raw + ' match' + (it.raw > 1 ? 's' : '') + ' (' + fmtPct(ratio(it.raw, d.n)) + ')'; }
        } } }
      }
    }, d.n ? null : 'Aucun match décidé');

    var g = computeGoalsPerMinute(ms);
    $('#lg-gpm').innerHTML = legendHtml([{ label: 'Marqués', color: T.us }, { label: 'Encaissés', color: T.them }]);
    makeChart('c-gpm', {
      type: 'bar',
      data: { labels: g.labels, datasets: [
        { label: 'Marqués', data: g.us, backgroundColor: T.us, maxBarThickness: 22, borderColor: T.surface, borderWidth: { left: 1, right: 1, top: 0, bottom: 0 }, borderSkipped: 'start' },
        { label: 'Encaissés', data: g.them, backgroundColor: T.them, maxBarThickness: 22, borderColor: T.surface, borderWidth: { left: 1, right: 1, top: 0, bottom: 0 }, borderSkipped: 'start' }
      ] },
      options: {
        interaction: { mode: 'index', intersect: false },
        datasets: { bar: { categoryPercentage: 0.6, barPercentage: 0.9 } },
        scales: baseScales({ x: { title: { display: true, text: 'Minute de jeu', color: T.muted, font: { size: 11.5 } } }, y: { beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { callbacks: {
          title: function (it) { var i = it[0].dataIndex; return i === 5 ? 'Prolongation' : 'Minute ' + g.labels[i]; },
          label: function (it) { return ' ' + it.dataset.label + ' : ' + it.raw + ' (' + fmtNum(ratio(it.raw, g.matches), 2) + ' / match)'; }
        } } }
      }
    }, g.goals ? null : 'Aucun but enregistré');
  }

  /* ---------- render: situations ---------- */
  function meterRow(label, r, cls) {
    return '<div class="sit-row"><span>' + label + ' <span class="muted">· ' + r.n + ' match' + (r.n > 1 ? 's' : '') + '</span></span><b>' + fmtPct(r.wr) + '</b>' +
      '<div class="meter ' + (cls || '') + '" role="img" aria-label="' + esc(fmtPct(r.wr)) + ' de victoire"><span style="width:' + (isNum(r.wr) ? (r.wr * 100).toFixed(1) : 0) + '%"></span></div></div>';
  }
  function renderSituations(ms) {
    var s = computeSituations(ms);
    var html = '';
    html += '<div class="card sit"><h3>Premier but</h3><div class="sit-rows">' +
      meterRow('Marqué en premier', s.firstUs) + meterRow('Encaissé en premier', s.firstThem, 'them') + '</div>' +
      '<div class="sit-foot">% de victoire selon qui ouvre le score' + (s.firstThem.wins ? ' · ' + s.firstThem.wins + ' remontada' + (s.firstThem.wins > 1 ? 's' : '') : '') + '</div></div>';
    html += '<div class="card sit"><h3>Matchs serrés</h3><div class="sit-rows">' +
      meterRow('Écart ≤ 1 but', s.close) + meterRow('Écart ≥ 3 buts', s.big) + '</div>' +
      '<div class="sit-foot">' + fmtPct(s.closeShare) + ' de vos matchs se jouent à un but</div></div>';
    html += '<div class="card sit"><h3>Prolongations</h3><div class="sit-rows">' + meterRow('En prolongation', s.ot) +
      '<div class="sit-row"><span>Part des matchs</span><b>' + fmtPct(s.otShare) + '</b></div>' +
      '<div class="sit-row"><span>Durée moyenne</span><b>' + (isNum(s.otAvgSeconds) ? fmtClock(s.otAvgSeconds) : '—') + '</b></div></div></div>';
    html += '<div class="card sit"><h3>Forfaits &amp; abandons</h3><div class="sit-rows">' +
      '<div class="sit-row"><span>Victoires par forfait adverse</span><b>' + s.forfeitWon + '</b></div>' +
      '<div class="sit-row"><span>Défaites par forfait</span><b>' + s.forfeitLost + '</b></div>' +
      '<div class="sit-row"><span>Matchs abandonnés</span><b>' + (state.filters.excludeAbandoned ? '<span class="muted" title="Filtre « Exclure abandons » actif">masqués</span>' : s.abandoned) + '</b></div></div>' +
      '<div class="sit-foot">Forfait = fin avant la dernière seconde du temps réglementaire</div></div>';
    $('#situations').innerHTML = html;
  }

  /* ---------- render: mental ---------- */
  function wrBars(id, buckets, opts) {
    opts = opts || {};
    var minN = opts.minN || 5;
    var data = buckets.map(function (b) { return b.wr == null ? null : b.wr * 100; });
    var colors = buckets.map(function (b) { return b.n >= minN ? T.us : alpha(T.us, 0.35); });
    return makeChart(id, {
      type: 'bar',
      data: { labels: buckets.map(function (b) { return b.label; }), datasets: [{ label: '% victoire', data: data, backgroundColor: colors, maxBarThickness: opts.thick || 24 }] },
      options: {
        scales: baseScales({ x: { ticks: { color: T.muted, maxRotation: 0, autoSkip: true, autoSkipPadding: 6 }, title: opts.xTitle ? { display: true, text: opts.xTitle, color: T.muted, font: { size: 11.5 } } : undefined },
          y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: function (v) { return v + ' %'; } } } }),
        plugins: { refLine: { value: 50 }, tooltip: { callbacks: {
          title: function (it) { return opts.title ? opts.title(it[0].dataIndex) : buckets[it[0].dataIndex].label; },
          label: function (it) { var b = buckets[it.dataIndex]; return ' ' + fmtPct(b.wr) + ' de victoire'; },
          footer: function (it) { var b = buckets[it[0].dataIndex]; return b.wins + ' V – ' + (b.n - b.wins) + ' D' + (b.n < minN ? ' · échantillon faible' : ''); }
        } } }
      }
    }, buckets.some(function (b) { return b.n > 0; }) ? null : 'Pas encore de données');
  }

  function renderMental(ms) {
    var m = computeMental(ms);
    var aw = m.afterWin, al = m.afterLoss;
    var verdict, bad = false;
    if (aw.n >= 10 && al.n >= 10) {
      var gap = (aw.wr - al.wr) * 100;
      if (gap >= 5) { bad = true; verdict = 'Vous gagnez <b>' + fmtNum(gap) + ' pts de moins</b> après une défaite. Une courte pause après un revers pourrait aider.'; }
      else if (gap <= -5) verdict = 'Vous rebondissez bien : <b>' + fmtNum(-gap) + ' pts de mieux</b> après une défaite qu’après une victoire.';
      else verdict = 'Pas d’effet « tilt » notable : votre % de victoire reste stable après une défaite.';
    } else {
      verdict = 'Il faut au moins 10 matchs dans chaque cas pour une analyse fiable.';
    }
    var best = m.bySession.filter(function (b) { return b.n >= 5 && b.wr != null; }).sort(function (a, b) { return b.wr - a.wr; })[0];
    $('#tilt').innerHTML =
      '<h3>Après le match précédent (même session)</h3>' +
      '<div class="tilt-pair">' +
      '<div class="tilt-box"><span class="lbl"><span class="res win">V</span>Après une victoire</span><div class="val">' + fmtPct(aw.wr) + '</div><span class="n">' + aw.n + ' match' + (aw.n > 1 ? 's' : '') + '</span></div>' +
      '<div class="tilt-box"><span class="lbl"><span class="res loss">D</span>Après une défaite</span><div class="val">' + fmtPct(al.wr) + '</div><span class="n">' + al.n + ' match' + (al.n > 1 ? 's' : '') + '</span></div>' +
      '</div>' +
      '<div class="tilt-verdict' + (bad ? ' bad' : '') + '">' + verdict + '</div>' +
      '<div class="tilt-sessions"><div><b>' + m.sessions + '</b>sessions</div><div><b>' + fmtNum(m.avgSessionLen, 1) + '</b>matchs / session</div><div><b>' + (best ? 'n°' + best.label : '—') + '</b>meilleur match de session</div></div>';
    $('#m-sess').textContent = 'barres pâles : < 5 matchs';
    wrBars('c-session', m.bySession, { xTitle: 'N° du match dans la session', title: function (i) { return i === 7 ? '8e match et suivants' : (i === 0 ? '1er' : (i + 1) + 'e') + ' match de la session'; } });
    // Day starts at 6h so late-night sessions stay contiguous; trim empty hours at both ends.
    var hours = m.byHour.slice(6).concat(m.byHour.slice(0, 6)).map(function (b, i) { return Object.assign({ h: (i + 6) % 24 }, b); });
    var firstH = hours.findIndex(function (b) { return b.n > 0; });
    var lastH = hours.length - 1 - hours.slice().reverse().findIndex(function (b) { return b.n > 0; });
    if (firstH >= 0) hours = hours.slice(Math.max(0, firstH - 1), Math.min(hours.length, lastH + 2));
    wrBars('c-hour', hours, { thick: 22, title: function (i) { var h = hours[i].h; return 'De ' + h + 'h à ' + ((h + 1) % 24) + 'h'; } });
    wrBars('c-weekday', m.byWeekday, { thick: 32, title: function (i) { return ['Lundi', 'Mardi', 'Mercredi', 'Jeudi', 'Vendredi', 'Samedi', 'Dimanche'][i]; } });
  }

  /* ---------- render: days & hours ---------- */
  function timeTile(cls, label, value, b, empty) {
    if (!b) return '<div class="card kpi time-tile ' + cls + '"><span class="kpi-label">' + label + '</span><span class="kpi-value muted">—</span><span class="kpi-sub">' + empty + '</span></div>';
    var d = b.delta, sign = d > 0 ? '+' : d < 0 ? '−' : '±';
    return '<div class="card kpi time-tile ' + cls + '"><span class="kpi-label">' + label + '</span>' +
      '<span class="kpi-value">' + value + '</span>' +
      '<span class="kpi-sub"><b>' + fmtPct(b.wr) + '</b> de victoires · ' + plural(b.n, 'match') +
      '<br><span class="' + (d >= 0 ? 'pos' : 'neg') + '">' + sign + fmtNum(Math.abs(d)) + ' pts</span> vs votre moyenne</span></div>';
  }
  function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }
  function renderTime(ms) {
    var t = computeTimeInsights(ms);
    var sum;
    if (!t.total) sum = 'Pas encore de match sur cette sélection.';
    else if (!t.enough) sum = 'Encore ' + plural(TIME_MIN_GAMES - t.total, 'match') + ' à jouer sur cette sélection pour une analyse fiable des jours et horaires.';
    else {
      var parts = [];
      if (t.bestDay) parts.push('le <b>' + t.bestDay.name + '</b>');
      if (t.bestWindow) parts.push('<b>' + t.bestWindow.label + '</b>');
      sum = parts.length ? 'En général, vous gagnez le plus ' + parts.join(', et ') + '.' : 'Pas d’écart marqué selon le jour ou l’heure pour l’instant.';
      if (t.worstDay || t.worstWindow) {
        var bad = [];
        if (t.worstDay) bad.push('le ' + t.worstDay.name);
        if (t.worstWindow) bad.push(t.worstWindow.label);
        sum += ' Moins bien ' + bad.join(' et ') + '.';
      }
    }
    $('#time-summary').innerHTML = sum + (t.total ? ' <span class="muted">Moyenne : ' + fmtPct(t.wr) + ' sur ' + plural(t.total, 'match') + ' (filtres ci-dessus appliqués).</span>' : '');
    var few = t.enough ? 'Pas d’écart marqué selon le jour' : 'Pas encore assez de matchs';
    var fewH = t.enough ? 'Pas d’écart marqué selon l’heure' : 'Pas encore assez de matchs';
    $('#time-tiles').innerHTML =
      timeTile('best', 'Meilleur jour', t.bestDay ? cap(t.bestDay.name) : '', t.bestDay, few) +
      timeTile('worst', 'Jour le plus difficile', t.worstDay ? cap(t.worstDay.name) : '', t.worstDay, few) +
      timeTile('best', 'Meilleur créneau', t.bestWindow ? t.bestWindow.from + 'h – ' + ((t.bestWindow.from + t.bestWindow.len) % 24) + 'h' : '', t.bestWindow, fewH) +
      timeTile('worst', 'Créneau le plus difficile', t.worstWindow ? t.worstWindow.from + 'h – ' + ((t.worstWindow.from + t.worstWindow.len) % 24) + 'h' : '', t.worstWindow, fewH);
    $('#m-busy').textContent = t.busiestDay && t.busiestDay.n ? 'Jour le plus joué : ' + t.busiestDay.name + ' (' + plural(t.busiestDay.n, 'match') + ')' : '';

    // Heatmap: weekday rows x hour columns (from 6h), trimmed to the played hours.
    var order = [];
    for (var i = 0; i < 24; i++) order.push((i + DAY_START_H) % 24);
    var played = order.filter(function (h) { return t.hours[h].n > 0; });
    var cols = order;
    if (played.length) {
      var a = order.indexOf(played[0]), z = order.indexOf(played[played.length - 1]);
      cols = order.slice(a, z + 1);
    }
    var minCell = 3;
    var head = '<div class="hm-corner"></div>' + cols.map(function (h) { return '<div class="hm-h">' + h + 'h</div>'; }).join('');
    var rows = t.grid.map(function (row, d) {
      return '<div class="hm-d">' + cap(WEEKDAY_NAMES[d]).slice(0, 3) + '.</div>' + cols.map(function (h) {
        var c = row[h];
        var tip = cap(WEEKDAY_NAMES[d]) + ' ' + h + 'h–' + ((h + 1) % 24) + 'h · ' +
          (c.n ? fmtPct(c.wr) + ' de victoires (' + c.wins + ' V – ' + (c.n - c.wins) + ' D)' + (c.n < minCell ? ' · échantillon faible' : '') : 'aucun match');
        var style = '', cls = 'hm-c';
        if (!c.n) cls += ' empty';
        else if (c.n < minCell) cls += ' few';
        else {
          // Diverging: distance from 50 % sets the strength, sign sets the hue.
          var dist = Math.min(1, Math.abs(c.wr - 0.5) / 0.3);
          var col = c.wr >= 0.5 ? 'var(--us)' : 'var(--loss)';
          style = ' style="background:color-mix(in srgb, ' + col + ' ' + Math.round(12 + dist * 78) + '%, var(--hm-mid))"';
          if (dist > 0.55) cls += ' strong';
        }
        return '<div class="' + cls + '"' + style + ' title="' + esc(tip) + '" aria-label="' + esc(tip) + '">' + (c.n ? c.n : '') + '</div>';
      }).join('');
    }).join('');
    var grid = $('#time-heatmap');
    grid.style.setProperty('--hm-cols', cols.length);
    grid.innerHTML = t.total ? head + rows : '<p class="muted small">Pas encore de données.</p>';
  }

  /* ---------- render: mechanics ---------- */
  function sparkline(canvas, data, color, label, fmtV) {
    if (!window.Chart) return;
    var id = canvas.id;
    if (charts[id]) charts[id].destroy();
    charts[id] = new window.Chart(canvas, {
      type: 'line',
      data: { labels: data.map(function (v, i) { return i + 1; }), datasets: [{ data: data, borderColor: color, backgroundColor: alpha(color, 0.10), fill: 'origin', borderWidth: 1.75, tension: 0.35, spanGaps: true, pointHoverRadius: 3.5, pointHoverBackgroundColor: color, pointHoverBorderColor: T.surface }] },
      options: {
        animation: false,
        interaction: { mode: 'index', intersect: false },
        layout: { padding: { top: 3, bottom: 1 } },
        scales: { x: { display: false }, y: { display: false, grace: '15%' } },
        plugins: { tooltip: { displayColors: false, callbacks: { title: function () { return ''; }, label: function (it) { return label + ' : ' + fmtV(it.raw); } } } }
      }
    });
  }

  function renderMechanics(ms) {
    var mc = computeMechanics(ms);
    var box = $('#mechanics');
    $('#mech-sub').textContent = mc.n ? 'Moyennes sur ' + mc.n + ' match' + (mc.n > 1 ? 's' : '') + ' avec données · courbe = tendance glissante 20 matchs' : 'Aucune donnée de mouvement (disponible seulement quand le jeu les transmet)';
    box.innerHTML = mc.metrics.map(function (m, i) {
      var v = m.unit === '%' ? fmtPct100(m.avg, m.digits) : fmtNum(m.avg, m.digits) + (m.unit ? '<span class="unit">' + m.unit + '</span>' : '');
      var tr = '';
      if (isNum(m.trend)) {
        var good = m.higherBetter == null ? null : (m.trend > 0) === m.higherBetter;
        var small = Math.abs(m.trend) < Math.pow(10, -m.digits) * 0.5;
        tr = small ? '≈ stable vs 20 précédents'
          : '<span class="' + (good == null ? '' : good ? 'up' : 'down') + '">' + (m.trend >= 0 ? '▲ ' : '▼ ') + fmtSigned(m.trend, m.digits) + (m.unit === '%' ? ' pt' : m.unit ? ' ' + m.unit : '') + '</span> vs 20 précédents';
      } else tr = m.n ? m.n + ' match' + (m.n > 1 ? 's' : '') : 'pas de données';
      return '<div class="card mech"><span class="kpi-label">' + esc(m.label) + '</span><span class="kpi-value">' + v + '</span>' +
        '<span class="trend">' + tr + '</span><div class="spark"><canvas id="sp-mech-' + i + '"></canvas></div></div>';
    }).join('');
    mc.metrics.forEach(function (m, i) {
      var cv = document.getElementById('sp-mech-' + i);
      if (hasData(m.series)) sparkline(cv, m.series, T.us, m.label, function (x) { return m.unit === '%' ? fmtPct100(x, m.digits) : fmtNum(x, m.digits) + (m.unit ? ' ' + m.unit : ''); });
      else { if (charts[cv.id]) { charts[cv.id].destroy(); delete charts[cv.id]; } }
    });
    // ground / wall / air split
    var s = mc.split;
    var tot = (s.ground || 0) + (s.wall || 0) + (s.air || 0);
    var segs = [
      { label: 'Au sol', v: s.ground, color: T.series[0] },
      { label: 'Sur les murs', v: s.wall, color: T.series[1] },
      { label: 'En l’air', v: s.air, color: T.series[2] }
    ];
    $('#split').innerHTML = tot > 0 ?
      '<div class="split-head"><span class="card-title">Répartition du temps</span><span class="legend">' +
      segs.map(function (g) { return '<span><i style="background:' + g.color + '"></i>' + g.label + ' ' + fmtPct100(g.v, 0) + '</span>'; }).join('') + '</span></div>' +
      '<div class="split-bar">' + segs.map(function (g) { return '<span style="width:' + ((g.v || 0) / tot * 100).toFixed(2) + '%;background:' + g.color + '" title="' + esc(g.label + ' : ' + fmtPct100(g.v, 1)) + '"></span>'; }).join('') + '</div>' +
      '<div class="detail-facts"><span>En boost <b>' + fmtPct100(mc.boostingPct, 1) + '</b></span><span>À 100 boost <b>' + fmtPct100(mc.fullBoostPct, 1) + '</b></span>' +
      '<span>Powerslide <b>' + fmtPct100(mc.powerslidePct, 1) + '</b></span><span>Temps détruit <b>' + fmtNum(mc.demolishedS, 1) + ' s</b> / match</span>' +
      '<span>Frappe la plus puissante <b>' + fmtNum(mc.maxHitKmh) + ' km/h</b></span></div>'
      : '<span class="muted small">Répartition sol / murs / air indisponible pour cette sélection.</span>';
  }

  /* ---------- render: statfeed ---------- */
  function renderFeed(ms) {
    var rows = computeStatfeed(ms, false).slice(0, 10);
    var box = $('#feed-box');
    box.style.height = Math.max(140, rows.length * 30 + 30) + 'px';
    makeChart('c-feed', {
      type: 'bar',
      data: { labels: rows.map(function (r) { return r.label; }), datasets: [{ data: rows.map(function (r) { return r.count; }), backgroundColor: T.us, maxBarThickness: 18 }] },
      options: {
        indexAxis: 'y',
        scales: {
          x: { beginAtZero: true, grid: { color: T.grid, drawTicks: false }, border: { display: false }, ticks: { precision: 0, color: T.muted, maxTicksLimit: 6 } },
          y: { grid: { display: false }, border: { color: T.axis }, ticks: { color: T['text-2'], font: { size: 12.5 } } }
        },
        plugins: { tooltip: { displayColors: false, callbacks: {
          label: function (it) { var r = rows[it.dataIndex]; return r.count + ' au total · ' + fmtNum(r.perMatch, 2) + ' par match'; },
          footer: function (it) { var r = rows[it[0].dataIndex]; return r.label !== r.key ? 'Événement : ' + r.key : ''; }
        } } }
      }
    }, rows.length ? null : 'Aucun événement spécial pour l’instant');
  }

  /* ---------- render: tables ---------- */
  function wrCell(wr) {
    return '<div class="wr-cell"><span class="num">' + fmtPct(wr) + '</span><span class="wr-bar"><span style="width:' + (isNum(wr) ? (wr * 100).toFixed(1) : 0) + '%"></span></span></div>';
  }
  function renderArenas(ms) {
    var all = computeArenas(ms);
    var rows = all.slice(0, 12);
    var restN = all.slice(12).reduce(function (t, a) { return t + a.n; }, 0);
    $('#t-arenas').innerHTML = '<thead><tr><th>Arène</th><th class="r">Matchs</th><th class="r hide-mobile">V–D</th><th class="r">% victoire</th><th class="r">Diff.</th></tr></thead><tbody>' +
      (rows.length ? rows.map(function (a) {
        return '<tr><td class="name">' + esc(a.name) + '</td><td class="r">' + a.n + '</td><td class="r hide-mobile">' + a.wins + '–' + a.losses + '</td><td class="r">' + wrCell(a.wr) + '</td><td class="r">' + fmtSigned(a.gdAvg, 1) + '</td></tr>';
      }).join('') + (restN ? '<tr><td class="muted" colspan="5">+ ' + (all.length - rows.length) + ' autre' + (all.length - rows.length > 1 ? 's' : '') + ' arène' + (all.length - rows.length > 1 ? 's' : '') + ' (' + restN + ' match' + (restN > 1 ? 's' : '') + ')</td></tr>' : '')
        : '<tr class="empty-row"><td colspan="5">Aucune arène</td></tr>') + '</tbody>';
  }
  function renderMates(ms) {
    var rows = computeTeammates(ms, 3).slice(0, 15);
    var overall = wl(ms).wr;
    $('#t-mates').innerHTML = '<thead><tr><th>Coéquipier</th><th class="r">Matchs</th><th class="r hide-mobile">V–D</th><th class="r">% victoire</th><th class="r" title="Écart avec votre % de victoire moyen">vs moy.</th><th class="r hide-mobile">Buts / match</th></tr></thead><tbody>' +
      (rows.length ? rows.map(function (t) {
        var d = isNum(t.wr) && isNum(overall) ? (t.wr - overall) * 100 : null;
        return '<tr><td class="name">' + playerName(t.name, t.pid) + '</td><td class="r">' + t.n + '</td><td class="r hide-mobile">' + t.wins + '–' + t.losses + '</td><td class="r">' + wrCell(t.wr) + '</td>' +
          '<td class="r">' + (d == null ? '—' : fmtSigned(d) + ' pts') + '</td><td class="r hide-mobile">' + fmtNum(t.goalsPer, 2) + '</td></tr>';
      }).join('') : '<tr class="empty-row"><td colspan="6">Aucun coéquipier avec au moins 3 matchs ensemble sur cette sélection</td></tr>') + '</tbody>';
  }

  function tagOptions(selected) {
    var tags = TAGS.slice();
    if (selected && tags.indexOf(selected) < 0) tags.push(selected);
    return tags.map(function (t) { return '<option value="' + esc(t) + '"' + (t === selected ? ' selected' : '') + '>' + esc(TAG_LABELS[t] || t) + '</option>'; }).join('');
  }
  var RES = { win: ['V', 'Victoire'], loss: ['D', 'Défaite'], abandoned: ['A', 'Abandonné'] };
  var TRASH = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg>';

  function renderMatches(ms) {
    var list = ms.slice().reverse();
    var shown = list.slice(0, state.shown);
    var cols = 9;
    var html = '<thead><tr><th style="width:28px"></th><th>Date</th><th>Mode</th><th class="c">Rés.</th><th class="c">Score</th><th class="r hide-mobile">B / P / A</th><th class="r hide-mobile">Points</th><th class="hide-mobile">Type</th><th style="width:40px"></th></tr></thead><tbody>';
    if (!shown.length) html += '<tr class="empty-row"><td colspan="' + cols + '">Aucun match pour ces filtres</td></tr>';
    shown.forEach(function (m) {
      var r = RES[m.result] || ['?', m.result || 'Inconnu'];
      var mm = m.me || {};
      var open = !!state.open[m.id];
      var chips = [];
      if (m.overtime) chips.push('<span class="chip" title="Prolongation">Prol.</span>');
      if (m.forfeit) chips.push('<span class="chip" title="Fin par forfait">Forfait</span>');
      if (m.mvp) chips.push('<span class="chip mvp">MVP</span>');
      if (m.online === false) chips.push('<span class="chip">Hors ligne</span>');
      html += '<tr class="row' + (open ? ' open' : '') + '" data-id="' + m.id + '" tabindex="0" aria-expanded="' + open + '">' +
        '<td><span class="caret">›</span></td>' +
        '<td class="nowrap"><a class="row-link" href="#/match/' + m.id + '" title="Ouvrir la page du match">' + esc(fmtDate(m._t, true)) + '</a></td>' +
        '<td><div>' + esc(m.mode || '—') + (m.variant && m.variant !== 'Soccar' ? ' ' + esc(m.variant) : '') + ' <span class="muted hide-mobile">· ' + esc(prettyArena(m.arena)) + '</span></div>' + (chips.length ? '<div class="chips">' + chips.join('') + '</div>' : '') + '</td>' +
        '<td class="c"><span class="res ' + esc(m.result) + '" title="' + esc(r[1]) + '">' + r[0] + '</span></td>' +
        '<td class="c"><span class="score">' + fmtNum(m.team_score) + '<span class="sep">–</span>' + fmtNum(m.opp_score) + '</span></td>' +
        '<td class="r hide-mobile num">' + fmtNum(mm.goals) + ' / ' + fmtNum(mm.assists) + ' / ' + fmtNum(mm.saves) + '</td>' +
        '<td class="r hide-mobile num">' + fmtNum(mm.score) + '</td>' +
        '<td class="hide-mobile"><select class="select tag-select" data-tag-for="' + m.id + '" aria-label="Type de match">' + tagOptions(m.tag) + '</select></td>' +
        '<td><button type="button" class="del-btn" data-del="' + m.id + '" title="Supprimer ce match" aria-label="Supprimer ce match">' + TRASH + '</button></td></tr>';
      if (open) html += '<tr class="detail"><td colspan="' + cols + '">' + matchDetail(m) + '</td></tr>';
    });
    html += '</tbody>';
    $('#t-matches').innerHTML = html;
    var more = $('#more-matches');
    more.hidden = list.length <= state.shown;
    more.textContent = 'Afficher plus (' + (list.length - Math.min(state.shown, list.length)) + ' restants)';
  }

  function goalTime(m, g) {
    var t = num(g.t), reg = regulationS(m);
    return isOtGoal(m, g) ? '+' + fmtClock(Math.max(0, t - reg)) : fmtClock(t);
  }
  function sortedGoals(m) {
    return (m.goals || []).filter(function (g) { return g && typeof g === 'object'; }).slice().sort(function (a, b) { return num(a.t) - num(b.t); });
  }
  /** Goal timeline: our goals above the axis, theirs below, overtime shaded. */
  function goalTimeline(m, goals) {
    var reg = regulationS(m);
    var total = Math.max(reg, reg + num(m.overtime_s), goals.length ? num(goals[goals.length - 1].t) : 0);
    var tl = '<div class="timeline" aria-hidden="true"><div class="axis"></div>';
    if (m.overtime) tl += '<div class="ot-zone" style="left:' + (reg / total * 100).toFixed(2) + '%;right:0"></div>';
    for (var mi = 0; mi * 60 <= reg; mi++) tl += '<span class="tick" style="left:' + (mi * 60 / total * 100).toFixed(2) + '%">' + mi + '′</span>';
    if (m.overtime && total > reg + 30) tl += '<span class="tick" style="left:' + ((reg + (total - reg) / 2) / total * 100).toFixed(2) + '%">Prol.</span>';
    goals.forEach(function (g) {
      tl += '<span class="g ' + (g.team === 'us' ? 'us' : 'them') + (g.me_scored ? ' me' : '') + '" style="left:' + (Math.min(1, num(g.t) / total) * 100).toFixed(2) + '%" title="' + esc(goalTime(m, g) + ' · ' + (g.scorer || '?')) + '"></span>';
    });
    return tl + '</div>';
  }
  function matchDetail(m) {
    var players = (m.players || []).slice();
    var ours = players.filter(function (p) { return p.team === m.my_team; }).sort(function (a, b) { return num(b.score) - num(a.score); });
    var theirs = players.filter(function (p) { return p.team !== m.my_team; }).sort(function (a, b) { return num(b.score) - num(a.score); });
    function prow(p) {
      return '<tr class="' + (p.is_me ? 'me' : '') + '"><td>' + playerName(p.name, p.primary_id) + '</td><td>' + fmtNum(p.score) + '</td><td>' + fmtNum(p.goals) + '</td><td>' + fmtNum(p.assists) + '</td><td>' + fmtNum(p.saves) + '</td><td class="hide-mobile">' + fmtNum(p.shots) + '</td><td class="hide-mobile">' + fmtNum(p.demos) + '</td></tr>';
    }
    var sb = '<table class="scoreboard"><thead><tr><th>Joueur</th><th>Pts</th><th>B</th><th>P</th><th>A</th><th class="hide-mobile">Tirs</th><th class="hide-mobile">Démo</th></tr></thead><tbody>' +
      '<tr class="team-row"><td colspan="7"><span class="team-dot" style="background:var(--us)"></span>Votre équipe · ' + fmtNum(m.team_score) + '</td></tr>' + ours.map(prow).join('') +
      '<tr class="team-row"><td colspan="7"><span class="team-dot" style="background:var(--them)"></span>Adversaires · ' + fmtNum(m.opp_score) + '</td></tr>' + theirs.map(prow).join('') +
      '</tbody></table>';

    var goals = sortedGoals(m);
    var tl = goalTimeline(m, goals);
    var us = 0, them = 0;
    var gl = goals.length ? '<ul class="goal-list">' + goals.map(function (g) {
      if (g.team === 'us') us++; else them++;
      return '<li><span class="t">' + goalTime(m, g) + '</span><span class="d" style="background:' + (g.team === 'us' ? 'var(--us)' : 'var(--them)') + '"></span>' +
        '<span class="who">' + esc(g.scorer || '?') + (g.me_scored ? ' <span class="chip">vous</span>' : '') + (g.assister ? ' <small>· passe ' + esc(g.assister) + '</small>' : '') +
        (isNum(g.speed) && g.speed > 0 ? ' <small>· ' + fmtNum(goalKmh(g.speed)) + ' km/h</small>' : '') + '</span>' +
        '<span class="sc">' + us + '–' + them + '</span></li>';
    }).join('') + '</ul>' : '<p class="muted small">' + (goalsComplete(m) ? 'Aucun but dans ce match.' : 'Aucun but enregistré.') + '</p>';
    if (!goalsComplete(m)) gl += '<p class="muted small">Suivi démarré en cours de match : chronologie incomplète (' + goals.length + ' but' + (goals.length > 1 ? 's' : '') + ' sur ' + (num(m.team_score) + num(m.opp_score)) + ').</p>';

    var facts = [];
    facts.push('<span>Durée <b>' + fmtClock(m.duration_s) + '</b></span>');
    facts.push('<span>Arène <b>' + esc(prettyArena(m.arena)) + '</b></span>');
    if (m.overtime) facts.push('<span>Prolongation <b>' + fmtClock(m.overtime_s) + '</b></span>');
    if (m.movement) {
      facts.push('<span>Vitesse moy. <b>' + fmtNum(kmh(m.movement.avg_speed)) + ' km/h</b></span>');
      facts.push('<span>Supersonique <b>' + fmtPct100(m.movement.supersonic_pct, 1) + '</b></span>');
      facts.push('<span>En l’air <b>' + fmtPct100(m.movement.air_pct, 1) + '</b></span>');
      facts.push('<span>Boost moy. <b>' + fmtNum(m.movement.avg_boost) + '</b></span>');
    }
    if (m.hits) facts.push('<span>Frappes <b>' + fmtNum(m.hits.count) + '</b> · moy. <b>' + fmtNum(kmh(m.hits.avg_speed)) + '</b> / max <b>' + fmtNum(kmh(m.hits.max_speed)) + ' km/h</b></span>');
    var sf = m.statfeed || {};
    var feed = Object.keys(sf).filter(function (k) { return k !== 'Win'; }).map(function (k) { return '<span class="chip">' + esc(statfeedLabel(k)) + (sf[k] > 1 ? ' ×' + sf[k] : '') + '</span>'; }).join('');

    return '<div class="detail-grid"><div><h4>Tableau des scores</h4>' + sb + '</div>' +
      '<div><h4>Chronologie des buts</h4>' + tl + gl + '</div></div>' +
      '<div class="detail-facts">' + facts.join('') + '</div>' +
      (feed ? '<h4>Fil de stats</h4><div class="chips">' + feed + '</div>' : '') +
      '<div class="detail-actions"><a class="btn btn-ghost" href="#/match/' + m.id + '">Voir le match <span aria-hidden="true">→</span></a></div>' +
      '<div class="detail-actions only-mobile"><label class="small muted">Type&nbsp;<select class="select tag-select" data-tag-for="' + m.id + '" aria-label="Type de match">' + tagOptions(m.tag) + '</select></label></div>';
  }

  /* ---------- routing ---------- */
  function syncNav() {
    var cur = state.route.view === 'dash' ? 'dash' : 'history';
    $$('[data-nav]').forEach(function (a) {
      if (a.getAttribute('data-nav') === cur) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    if (state.route.view === 'dash') document.title = 'Rocket Tracker';
    else if (state.route.view === 'history') document.title = 'Historique · Rocket Tracker';
  }
  function onRoute() {
    var r = parseRoute(location.hash);
    var prev = state.route;
    if (r.view === prev.view && r.id === prev.id) return;
    if (prev.view === 'history') state.histScroll = window.scrollY || window.pageYOffset || 0;
    state.route = r;
    renderAll();
    window.scrollTo(0, r.view === 'history' && prev.view === 'match' ? state.histScroll : 0);
    var h = r.view === 'history' ? $('#h-history') : r.view === 'match' ? $('#mp-title') : null;
    if (h) h.focus({ preventScroll: true });
  }

  /* ---------- shared match bits ---------- */
  var RES_LABEL = { win: ['V', 'Victoire'], loss: ['D', 'Défaite'], abandoned: ['Abandon', 'Abandonné'] };
  var PARTIAL_TIP = 'Suivi démarré en cours de match : stats et chronologie des buts incomplètes';
  function resBadge(m) {
    var r = RES_LABEL[m.result] || ['?', 'Résultat inconnu'];
    return '<span class="res ' + esc(m.result || '') + '" aria-hidden="true">' + r[0] + '</span><span class="sr-only">' + r[1] + '</span>';
  }
  function longDay(t) {
    var s = new Date(t).toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
    return s.charAt(0).toUpperCase() + s.slice(1);
  }
  function fmtTime(t) { return new Date(t).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' }); }
  function modeLabel(m) { return (m.mode || '—') + (m.variant && m.variant !== 'Soccar' ? ' ' + m.variant : ''); }
  function signCls(x) { return isNum(x) && x > 0 ? 'pos' : isNum(x) && x < 0 ? 'neg' : ''; }
  function matchBadges(m, long) {
    var b = [];
    if (m.mvp) b.push('<span class="chip mvp" title="Meilleur joueur du match">MVP</span>');
    if (m.overtime) b.push('<span class="chip" title="Prolongation' + (isNum(m.overtime_s) && m.overtime_s > 0 ? ' : ' + fmtClock(m.overtime_s) : '') + '">' + (long ? 'Prolongation' : 'Prol.') + '</span>');
    if (m.forfeit) {
      b.push(m.result === 'win' ? '<span class="chip" title="Victoire par forfait de l’équipe adverse">Forfait adv.</span>'
        : '<span class="chip" title="Défaite par forfait de votre équipe">Forfait</span>');
    }
    if (m.partial) b.push('<span class="chip warn" title="' + esc(PARTIAL_TIP) + '">Partiel</span>');
    if (m.online === false) b.push('<span class="chip" title="Match hors ligne (bots, local)">Hors ligne</span>');
    return b.join('');
  }
  function tagFilterOptions() {
    var tags = TAGS.slice();
    state.all.forEach(function (m) { if (m.tag && tags.indexOf(m.tag) < 0) tags.push(m.tag); });
    return '<option value="all">Tous</option>' + tags.map(function (t) { return '<option value="' + esc(t) + '">' + esc(TAG_LABELS[t] || t) + '</option>'; }).join('');
  }
  var DOT_SEP = '<span class="dot-sep" aria-hidden="true">·</span>';

  /* ---------- history view ---------- */
  function syncHistUi() {
    var f = state.hist;
    [['#h-mode', 'mode'], ['#h-result', 'result'], ['#h-period', 'period']].forEach(function (x) {
      $$(x[0] + ' button').forEach(function (b) { b.setAttribute('role', 'radio'); b.setAttribute('aria-checked', String(b.dataset.v === String(f[x[1]]))); });
    });
    var tagSel = $('#h-tag');
    tagSel.innerHTML = tagFilterOptions();
    tagSel.value = f.tag;
    if (tagSel.value !== f.tag) { f.tag = 'all'; tagSel.value = 'all'; }
    var q = $('#h-q');
    if (document.activeElement !== q && q.value !== f.q) q.value = f.q;
    $('#h-offline').checked = !!f.includeOffline;
  }

  function histRow(m) {
    var mm = m.me || {};
    var diff = isNum(m.goal_diff) ? m.goal_diff : (isNum(m.team_score) && isNum(m.opp_score) ? m.team_score - m.opp_score : null);
    var badges = matchBadges(m, false);
    return '<li><a class="hrow" href="#/match/' + m.id + '">' +
      '<span class="h-time num">' + esc(fmtTime(m._t)) + '</span>' +
      '<span class="h-mode">' + esc(modeLabel(m)) + '</span>' +
      '<span class="h-res">' + resBadge(m) + '</span>' +
      '<span class="h-score"><span class="score">' + fmtNum(m.team_score) + '<span class="sep">–</span>' + fmtNum(m.opp_score) + '</span>' +
        (diff != null ? ' <span class="h-diff ' + signCls(diff) + '">' + fmtSigned(diff) + '</span>' : '') + '</span>' +
      '<span class="h-arena"><span class="h-arena-name">' + esc(prettyArena(m.arena)) + '</span>' + (badges ? '<span class="chips">' + badges + '</span>' : '') + '</span>' +
      '<span class="h-stats num" title="Buts / Passes / Arrêts / Tirs">' + fmtNum(mm.goals) + ' / ' + fmtNum(mm.assists) + ' / ' + fmtNum(mm.saves) + ' / ' + fmtNum(mm.shots) + '</span>' +
      '<span class="h-pts num">' + fmtNum(mm.score) + '<small> pts</small></span>' +
      '<span class="h-dur num" title="Durée">' + fmtClock(m.duration_s) + '</span>' +
      '<span class="h-go" aria-hidden="true">›</span></a></li>';
  }
  function manualRow(e) {
    return '<li class="hist-manual"><span class="hm-icon" aria-hidden="true">✎</span><span>Saisie manuelle' + DOT_SEP + plural(e.games, 'game') + ' ' + esc(e.mode) + DOT_SEP +
      e.wins + ' V – ' + e.losses + ' D</span></li>';
  }
  function histDay(g) {
    var head = longDay(g.t);
    var tot = [plural(g.games, 'match'), g.wins + ' V – ' + g.losses + ' D'];
    if (g.abandoned) tot.push(plural(g.abandoned, 'abandon'));
    if (g.decided) tot.push('diff. <b class="' + signCls(g.diff) + '">' + fmtSigned(g.diff) + '</b>');
    var note = g.manualGames ? ' <span class="chip manual" title="Les totaux du jour incluent ' + plural(g.manualGames, 'game') + ' saisie' + (g.manualGames > 1 ? 's' : '') + ' à la main">✎ dont ' + g.manualGames + ' saisie' + (g.manualGames > 1 ? 's' : '') + '</span>' : '';
    var rows = g.shownMatches.map(histRow).join('') + (g.truncated ? '<li class="hist-trunc">Suite de la journée avec « Afficher plus »</li>' : g.manual.map(manualRow).join(''));
    return '<section class="card hist-day" aria-label="' + esc(head) + '"><header class="hist-day-head"><h2>' + esc(head) + '</h2>' +
      '<p class="hist-day-tot">' + tot.join(DOT_SEP) + note + '</p></header><ul class="hist-rows">' + rows + '</ul></section>';
  }
  function renderHistory() {
    syncHistUi();
    var f = state.hist, box = $('#hist-list'), more = $('#hist-more'), sum = $('#hist-summary');
    if (!state.loaded) { sum.textContent = ''; more.hidden = true; box.innerHTML = '<div class="hist-empty muted">Chargement…</div>'; return; }
    var ms = filterHistory(state.all, f);
    var man = filterManualHistory(state.manual, f);
    var groups = groupHistory(ms, man);
    var page = paginateGroups(groups, state.histShown);
    var s = summarizeHistory(ms, man);
    var parts = [];
    if (s.n) {
      parts.push('<b>' + fmtNum(s.n) + '</b> match' + (s.n > 1 ? 's' : ''));
      parts.push('<b>' + s.wins + '</b> V – <b>' + s.losses + '</b> D' + (s.abandoned ? ' <span class="muted">(+ ' + plural(s.abandoned, 'abandon') + ')</span>' : ''));
      parts.push('<b>' + fmtPct(s.wr) + '</b> de victoire');
      parts.push('diff. moyenne <b class="' + signCls(s.diffAvg) + '">' + fmtSigned(s.diffAvg, 2) + '</b>');
    } else parts.push('Aucun match suivi');
    if (s.manualGames) parts.push('<span class="muted">+ ' + plural(s.manualGames, 'game') + ' saisie' + (s.manualGames > 1 ? 's' : '') + ' à la main</span>');
    sum.innerHTML = parts.join(DOT_SEP);
    if (!groups.length) {
      more.hidden = true;
      box.innerHTML = !state.all.length && !state.manual.length
        ? '<div class="no-results"><p><strong>Aucun match enregistré pour l’instant.</strong></p><p class="muted">Jouez une partie : elle apparaîtra ici automatiquement.</p></div>'
        : '<div class="no-results"><p><strong>Aucun match ne correspond à ces filtres.</strong></p><p class="muted">Élargissez la période ou modifiez la recherche.</p>' +
          '<p><button type="button" class="btn" data-hist-reset>Réinitialiser les filtres</button></p></div>';
      return;
    }
    box.innerHTML = '<div class="hist-cols" aria-hidden="true"><span>Heure</span><span>Mode</span><span>Rés.</span><span>Score</span><span>Arène</span>' +
      '<span class="r">B / P / A / Tirs</span><span class="r">Points</span><span class="r">Durée</span><span></span></div>' + page.groups.map(histDay).join('');
    more.hidden = page.remaining <= 0;
    more.textContent = 'Afficher plus (' + plural(page.remaining, 'match') + ' restant' + (page.remaining > 1 ? 's' : '') + ')';
  }
  function onHistChanged() { saveHistFilters(); state.histShown = HIST_PAGE; renderHistory(); }
  function bindHistory() {
    function seg(id, key) {
      $(id).addEventListener('click', function (e) {
        var b = e.target.closest('button[data-v]');
        if (!b) return;
        state.hist[key] = b.dataset.v;
        onHistChanged();
      });
    }
    seg('#h-mode', 'mode');
    seg('#h-result', 'result');
    seg('#h-period', 'period');
    $('#h-tag').addEventListener('change', function (e) { state.hist.tag = e.target.value; onHistChanged(); });
    $('#h-offline').addEventListener('change', function (e) { state.hist.includeOffline = e.target.checked; onHistChanged(); });
    var qTimer = null;
    $('#h-q').addEventListener('input', function (e) {
      var v = e.target.value;
      clearTimeout(qTimer);
      qTimer = setTimeout(function () { state.hist.q = v; onHistChanged(); }, 160);
    });
    $('#hist-more').addEventListener('click', function () { state.histShown += HIST_PAGE; renderHistory(); });
    $('#view-history').addEventListener('click', function (e) {
      if (!e.target.closest('[data-hist-reset]')) return;
      state.hist = Object.assign({}, DEFAULT_HIST_FILTERS);
      $('#h-q').value = '';
      onHistChanged();
    });
  }

  /* ---------- match page ---------- */
  function navLink(t, label, rel, key) {
    if (!t) return '<span class="btn btn-ghost" aria-disabled="true">' + label + '</span>';
    var tip = fmtDate(t._t, true) + ' · ' + modeLabel(t) + ' · ' + num(t.team_score) + '–' + num(t.opp_score) + ' (touche ' + key + ')';
    return '<a class="btn btn-ghost" href="#/match/' + t.id + '" rel="' + rel + '" title="' + esc(tip) + '">' + label + '</a>';
  }
  function mpNav(nb) {
    return '<nav class="mp-nav" aria-label="Navigation entre les matchs">' +
      '<a class="btn btn-ghost" href="#/historique"><span aria-hidden="true">←</span> Historique</a>' +
      '<div class="mp-pn">' + navLink(nb.prev, '<span aria-hidden="true">‹</span> Précédent', 'prev', '←') +
      '<span class="mp-pos num" title="Position chronologique parmi tous vos matchs">' + fmtNum(nb.index + 1) + ' / ' + fmtNum(nb.total) + '</span>' +
      navLink(nb.next, 'Suivant <span aria-hidden="true">›</span>', 'next', '→') + '</div></nav>';
  }
  function mpHero(m, ctx) {
    var cls = m.result === 'win' || m.result === 'loss' || m.result === 'abandoned' ? m.result : '';
    var resTxt = m.result === 'win' ? (m.forfeit ? 'Victoire par forfait' : 'Victoire') : m.result === 'loss' ? (m.forfeit ? 'Défaite par forfait' : 'Défaite')
      : m.result === 'abandoned' ? 'Match abandonné' : 'Résultat inconnu';
    var dur = 'Durée <b>' + fmtClock(m.duration_s) + '</b>' + (m.overtime ? ' dont <b>' + fmtClock(m.overtime_s) + '</b> de prolongation' : '');
    var facts = [esc(m.mode || '—'), esc(m.variant || 'Soccar'), esc(prettyArena(m.arena)), m.online === false ? 'Hors ligne' : 'En ligne', dur];
    var ts = fmtNum(m.team_score), os = fmtNum(m.opp_score);
    var badges = matchBadges(m, true);
    var ctxParts = [];
    if (ctx) {
      ctxParts.push('Match <b>n°' + ctx.sessIdx + '</b> de la session (' + plural(ctx.sessN, 'match') + ')');
      var p = ctx.prevInSession;
      if (p) {
        var pr = RES_LABEL[p.result] ? RES_LABEL[p.result][1] : 'Résultat inconnu';
        ctxParts.push('Match précédent : <a href="#/match/' + p.id + '">' + esc(pr) + ' ' + fmtNum(p.team_score) + '–' + fmtNum(p.opp_score) + '</a> <span class="muted">(' + esc(modeLabel(p)) + ')</span>');
      } else ctxParts.push('Premier match de la session');
      if (ctx.streak) {
        var w = ctx.streak.type === 'win', n = ctx.streak.len;
        ctxParts.push((m.result === 'win' || m.result === 'loss' ? 'Série après ce match' : 'Série en cours à ce moment') + ' : <b class="' + (w ? 'pos' : 'neg') + '">' + n + ' ' + (w ? 'victoire' : 'défaite') + (n > 1 ? 's' : '') + '</b>' + (n > 1 ? ' d’affilée' : ''));
      }
    }
    return '<section class="card mp-hero" aria-labelledby="mp-title">' +
      '<div class="mp-meta"><h1 id="mp-title" tabindex="-1">' + esc(longDay(m._t)) + ' · ' + esc(fmtTime(m._t)) + '</h1>' +
        '<p class="mp-facts">' + facts.join(DOT_SEP) + '</p></div>' +
      '<div class="mp-center">' +
        '<div class="mp-score" role="img" aria-label="' + esc('Score : votre équipe ' + ts + ', adversaires ' + os) + '">' +
          '<div class="mp-team us"><span class="mp-n">' + ts + '</span><span class="mp-tl">Votre équipe</span></div>' +
          '<span class="mp-sep" aria-hidden="true">–</span>' +
          '<div class="mp-team them"><span class="mp-n">' + os + '</span><span class="mp-tl">Adversaires</span></div></div>' +
        '<div class="mp-res ' + cls + '">' + resTxt + '</div>' +
        (badges ? '<div class="chips">' + badges + '</div>' : '') +
      '</div>' +
      '<div class="mp-side"><label class="mp-tag"><span>Type</span><select class="select tag-select" data-tag-for="' + m.id + '" aria-label="Type de match">' + tagOptions(m.tag) + '</select></label>' +
        '<button type="button" class="btn btn-ghost btn-danger" data-mp-del="' + m.id + '">' + TRASH + 'Supprimer</button></div>' +
      (m.partial ? '<p class="mp-note">' + esc(PARTIAL_TIP) + '.</p>' : '') +
      (ctxParts.length ? '<div class="mp-context">' + ctxParts.map(function (x) { return '<span>' + x + '</span>'; }).join('') + '</div>' : '') +
      '</section>';
  }

  var SB_COLS = [['score', 'Score'], ['goals', 'Buts'], ['assists', 'Passes'], ['saves', 'Arrêts'], ['shots', 'Tirs'], ['touches', 'Touches'], ['demos', 'Démos']];
  function mpScoreboard(m) {
    var players = (m.players || []).filter(function (p) { return p && typeof p === 'object'; });
    if (!players.length && m.me && m.me.name) players = [Object.assign({ team: m.my_team, is_me: true }, m.me)];
    function team(us) {
      var list = players.filter(function (p) { return us ? p.team === m.my_team : p.team !== m.my_team; })
        .sort(function (a, b) { return num(b.score) - num(a.score); });
      var html = '<tbody><tr class="team-row"><th colspan="' + (SB_COLS.length + 1) + '" scope="colgroup"><span class="team-dot" style="background:var(--' + (us ? 'us' : 'them') + ')"></span>' +
        (us ? 'Votre équipe' : 'Adversaires') + ' · ' + fmtNum(us ? m.team_score : m.opp_score) + '</th></tr>';
      if (!list.length) return html + '<tr><td class="muted" colspan="' + (SB_COLS.length + 1) + '">Joueurs inconnus</td></tr></tbody>';
      html += list.map(function (p) {
        return '<tr' + (p.is_me ? ' class="me"' : '') + '><th scope="row">' + playerName(p.name, p.primary_id) + (p.is_me ? ' <span class="chip you">vous</span>' : '') + '</th>' +
          SB_COLS.map(function (c) { return '<td>' + fmtNum(p[c[0]]) + '</td>'; }).join('') + '</tr>';
      }).join('');
      html += '<tr class="tot"><th scope="row">Total</th>' + SB_COLS.map(function (c) {
        var vals = list.map(function (p) { return p[c[0]]; }).filter(isNum);
        return '<td>' + (vals.length ? fmtNum(vals.reduce(function (a, b) { return a + b; }, 0)) : '—') + '</td>';
      }).join('') + '</tr></tbody>';
      return html;
    }
    return '<section class="card mp-card mp-span" aria-labelledby="mp-h-sb"><div class="mp-head"><h2 id="mp-h-sb">Tableau des scores</h2>' +
      '<span class="card-meta">Cliquez sur un pseudo pour son profil tracker.gg</span></div>' +
      '<div class="table-scroll"><table class="mp-sb"><thead><tr><th scope="col">Joueur</th>' + SB_COLS.map(function (c) { return '<th scope="col">' + c[1] + '</th>'; }).join('') + '</tr></thead>' +
      team(true) + team(false) + '</table></div></section>';
  }

  function mpGoals(m) {
    var goals = sortedGoals(m);
    var complete = goalsComplete(m);
    var expected = num(m.team_score) + num(m.opp_score);
    var us = 0, them = 0;
    var list = goals.length ? '<ol class="goal-list mp-goal-list">' + goals.map(function (g) {
      var ours = g.team === 'us';
      if (ours) us++; else them++;
      var spd = isNum(g.speed) && g.speed > 0 ? fmtNum(goalKmh(g.speed)) + ' km/h' : '';
      return '<li><span class="t num">' + goalTime(m, g) + '</span><span class="d" style="background:var(--' + (ours ? 'us' : 'them') + ')"></span>' +
        '<span class="sr-only">' + (ours ? 'Votre équipe' : 'Adversaires') + '</span>' +
        '<span class="who"><b>' + esc(g.scorer || '?') + '</b>' + (g.me_scored ? ' <span class="chip you">vous</span>' : '') +
        (g.assister ? ' <small>· passe ' + esc(g.assister) + (g.me_assist ? ' <span class="chip you">vous</span>' : '') + '</small>' : '') + '</span>' +
        '<span class="spd num">' + spd + '</span>' +
        (complete ? '<span class="sc num" title="Score après ce but">' + us + '–' + them + '</span>' : '') + '</li>';
    }).join('') + '</ol>' : '<p class="muted small">' + (complete ? 'Aucun but dans ce match.' : 'Aucun but enregistré.') + '</p>';
    var note = complete ? '' : '<p class="mp-note">Liste incomplète : ' + plural(goals.length, 'but') + ' enregistré' + (goals.length > 1 ? 's' : '') + ' sur ' + expected +
      ' (suivi démarré en cours de match). Le score cumulé n’est pas affiché.</p>';
    return '<section class="card mp-card" aria-labelledby="mp-h-goals"><div class="mp-head"><h2 id="mp-h-goals">Buts</h2>' +
      '<span class="legend"><span><i style="background:var(--us)"></i>Votre équipe</span><span><i style="background:var(--them)"></i>Adversaires</span><span><i class="ring"></i>vous</span></span></div>' +
      goalTimeline(m, goals) + list + note + '</section>';
  }

  function cmpUnit(s) { return s.unit === '%' ? ' %' : s.unit ? ' ' + s.unit : ''; }
  /** One "this match vs average" row: value, average, signed delta (arrow + sign, colored when good/bad), bar + average tick. */
  function cmpRow(s, v, a) {
    var ad = s.avgDigits != null ? s.avgDigits : s.digits;
    var fv = function (x, d) { return isNum(x) ? fmtNum(x, d) + cmpUnit(s) : '—'; };
    var d = isNum(v) && isNum(a) ? v - a : null;
    var flat = d != null && Math.abs(d) < 0.5 * Math.pow(10, -ad);
    var good = d == null || flat || s.higherBetter == null ? null : (d > 0) === s.higherBetter;
    var cls = d == null ? '' : flat ? 'flat' : good == null ? 'neutral' : good ? 'up' : 'down';
    var txt = d == null ? '—' : flat ? '= moy.' : (d > 0 ? '↑ ' : '↓ ') + fmtSigned(d, ad) + (s.unit === '%' ? ' pt' : cmpUnit(s));
    var max = Math.max(isNum(v) ? v : 0, isNum(a) ? a : 0) * 1.15;
    var bar = '';
    if (max > 0) {
      bar = '<span class="cmp" aria-hidden="true" title="' + esc('Ce match : ' + fv(v, s.digits) + ' · moyenne : ' + fv(a, ad)) + '">' +
        (isNum(v) ? '<span class="cmp-fill" style="width:' + (v / max * 100).toFixed(1) + '%"></span>' : '') +
        (isNum(a) ? '<span class="cmp-avg" style="left:' + (a / max * 100).toFixed(1) + '%"></span>' : '') + '</span>';
    }
    return '<tr><th scope="row">' + esc(s.label) + '</th><td class="v">' + fv(v, s.digits) + '</td><td class="a">' + fv(a, ad) + '</td>' +
      '<td class="dl ' + cls + '">' + txt + '</td><td class="b">' + bar + '</td></tr>';
  }
  function cmpTable(rows) {
    return '<div class="table-scroll"><table class="cmp-table"><thead><tr><th scope="col"><span class="sr-only">Statistique</span></th><th scope="col">Ce match</th><th scope="col">Moyenne</th>' +
      '<th scope="col">Écart</th><th scope="col" class="b"><span class="legend"><span><i style="background:var(--us)"></i>ce match</span><span><i class="tick"></i>moyenne</span></span></th></tr></thead><tbody>' +
      rows.join('') + '</tbody></table></div>';
  }
  function mpPerf(m, base) {
    var head = '<div class="mp-head"><h2 id="mp-h-perf">Votre performance vs votre moyenne</h2>';
    var open = '<section class="card mp-card" aria-labelledby="mp-h-perf">';
    if (!base || base.n < 3) {
      return open + head + '</div><p class="muted small">Pas assez de matchs ' + esc(modeLabel(m)) + ' terminés avant celui-ci pour comparer (' + (base ? base.n : 0) + ' sur 3 minimum).</p></section>';
    }
    var perf = PERF_STATS.map(function (s) {
      return cmpRow(Object.assign({ avgDigits: s.key === 'score' ? 0 : 1 }, s), statValue(m, 'me', s), base.me[s.key].avg);
    });
    return open + head + '<span class="card-meta">Moyenne de vos ' + base.n + ' derniers matchs ' + esc(modeLabel(m)) + ' terminés avant celui-ci</span></div>' +
      cmpTable(perf) + '</section>';
  }
  function splitRow(label, g, w, a) {
    var vals = [g, w, a], tot = vals.reduce(function (s, x) { return s + (isNum(x) ? x : 0); }, 0);
    if (!(tot > 0)) return '';
    var names = ['Au sol', 'Sur les murs', 'En l’air'];
    return '<div class="mp-split-row"><span class="lbl">' + label + '</span><div class="split-bar">' + vals.map(function (v, i) {
      return '<span style="width:' + ((isNum(v) ? v : 0) / tot * 100).toFixed(2) + '%;background:' + T.series[i] + '" title="' + esc(names[i] + ' : ' + fmtPct100(v, 1)) + '"></span>';
    }).join('') + '</div><span class="vals num">' + vals.map(function (v) { return fmtNum(v, 0); }).join(' / ') + ' %</span></div>';
  }
  function mpMovement(m, base) {
    var mv = m.movement, h = m.hits;
    if (!mv && !h) return '';
    var bm = base ? base.movement : {}, bh = base ? base.hits : {};
    function avgOf(o, k) { return o && o[k] && o[k].n >= 3 ? o[k].avg : null; }
    var nMv = bm && bm.avg_speed ? bm.avg_speed.n : 0, nH = bh && bh.avg_speed ? bh.avg_speed.n : 0;
    var nRef = Math.max(nMv, nH);
    var sub = nRef >= 3 ? 'Comparé à vos ' + nRef + ' derniers matchs ' + esc(modeLabel(m)) + ' avec données' : 'Pas assez de matchs précédents avec données pour comparer';
    var html = '<section class="card mp-card" aria-labelledby="mp-h-mv"><div class="mp-head"><h2 id="mp-h-mv">Mouvement &amp; frappes</h2><span class="card-meta">' + sub + '</span></div>';
    if (mv) {
      var rowThis = splitRow('Ce match', mv.ground_pct, mv.wall_pct, mv.air_pct);
      var rowAvg = avgOf(bm, 'ground_pct') != null ? splitRow('Moyenne', bm.ground_pct.avg, bm.wall_pct.avg, bm.air_pct.avg) : '';
      if (rowThis) {
        html += '<div class="mp-split"><div class="mp-split-head"><span class="card-title">Répartition du temps</span><span class="legend">' +
          ['Au sol', 'Sur les murs', 'En l’air'].map(function (l, i) { return '<span><i style="background:' + T.series[i] + '"></i>' + l + '</span>'; }).join('') + '</span></div>' +
          rowThis + rowAvg + '</div>';
      }
    }
    var rows = [];
    if (mv) MOVE_STATS.forEach(function (s) { if (!s.split) rows.push(cmpRow(s, statValue(m, 'movement', s), avgOf(bm, s.key))); });
    if (h) {
      if (rows.length) rows.push('<tr class="sub"><th colspan="5" scope="rowgroup">Frappes de balle</th></tr>');
      HIT_STATS.forEach(function (s) { rows.push(cmpRow(s, statValue(m, 'hits', s), avgOf(bh, s.key))); });
    }
    return html + cmpTable(rows) + '</section>';
  }
  function mpFeed(m) {
    var sf = m.statfeed || {};
    var rows = Object.keys(sf).filter(function (k) { return k !== 'Win' && isNum(sf[k]) && sf[k] > 0; })
      .map(function (k) { return { key: k, label: statfeedLabel(k), n: sf[k] }; })
      .sort(function (a, b) { return b.n - a.n || a.label.localeCompare(b.label); });
    if (!rows.length) return '';
    return '<section class="card mp-card" aria-labelledby="mp-h-feed"><div class="mp-head"><h2 id="mp-h-feed">Fil de stats</h2><span class="card-meta">Vos événements dans ce match</span></div>' +
      '<ul class="mp-feed">' + rows.map(function (r) {
        return '<li' + (r.label !== r.key ? ' title="' + esc('Événement : ' + r.key) + '"' : '') + '><span>' + esc(r.label) + '</span><b class="num">×' + r.n + '</b></li>';
      }).join('') + '</ul></section>';
  }
  function renderMatchPage() {
    var box = $('#view-match');
    var id = state.route.id;
    if (!state.loaded) { box.innerHTML = '<div class="card mp-missing"><p class="muted">Chargement du match…</p></div>'; return; }
    var m = id != null ? findMatch(id) : null;
    if (!m) {
      document.title = 'Match introuvable · Rocket Tracker';
      box.innerHTML = '<div class="card mp-missing"><h1 id="mp-title" tabindex="-1">Match introuvable</h1>' +
        '<p class="muted">Ce match n’existe pas ou a été supprimé' + (id != null ? ' (n°' + id + ')' : '') + '.</p>' +
        '<p><a class="btn" href="#/historique"><span aria-hidden="true">←</span> Retour à l’historique</a></p></div>';
      return;
    }
    document.title = 'Match du ' + fmtDate(m._t, true) + ' · Rocket Tracker';
    var nb = matchNeighbors(state.all, m.id), ctx = matchContext(state.all, m.id), base = matchBaseline(state.all, m.id, 50);
    // Two independent columns (no holes when card heights differ); they stack on narrow screens.
    var right = mpMovement(m, base) + mpFeed(m);
    var left = mpGoals(m) + (right ? mpPerf(m, base) : '');
    if (!right) right = mpPerf(m, base);
    box.innerHTML = mpNav(nb) + mpHero(m, ctx) + mpScoreboard(m) + '<div class="mp-grid"><div class="mp-col">' + left + '</div><div class="mp-col">' + right + '</div></div>';
  }
  function bindMatchView() {
    var v = $('#view-match');
    v.addEventListener('change', function (e) {
      var sel = e.target.closest('[data-tag-for]');
      if (sel) updateTag(+sel.dataset.tagFor, sel.value, sel);
    });
    v.addEventListener('click', function (e) {
      var d = e.target.closest('[data-mp-del]');
      if (d) deleteMatch(+d.getAttribute('data-mp-del'), function () { location.hash = '#/historique'; });
    });
    document.addEventListener('keydown', function (e) {
      if (state.route.view !== 'match' || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      var t = e.target;
      if (t && t.closest && t.closest('input, select, textarea, [contenteditable], .table-scroll')) return;
      if (document.querySelector('dialog[open]')) return;
      var nb = matchNeighbors(state.all, state.route.id);
      var to = e.key === 'ArrowLeft' ? nb.prev : nb.next;
      if (to) { e.preventDefault(); location.hash = '#/match/' + to.id; }
    });
  }

  /* ---------- live banner & warnings ---------- */
  function renderStatus() {
    var s = state.status;
    var pill = $('#conn-pill');
    var label = pill.querySelector('.pill-label');
    var w = [];
    if (state.statusError) {
      pill.className = 'pill pill-warn'; label.textContent = 'Serveur injoignable';
      w.push(warning('!', 'Le serveur Rocket Tracker ne répond pas', 'Vérifiez que <code>rltracker</code> tourne (il démarre normalement avec Windows). La page se reconnectera automatiquement.'));
    } else if (s) {
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = 'Match en cours'; }
      else if (s.connected) { pill.className = 'pill pill-ok'; label.textContent = 'Connecté à Rocket League'; pill.title = 'Transport : ' + (s.transport === 'tcp' ? 'TCP' : s.transport === 'ws' ? 'WebSocket' : '—'); }
      else { pill.className = 'pill pill-muted'; label.textContent = 'Rocket League non détecté'; }
      var ini = s.ini || null;
      // Connected = the Stats API evidently works (the ini may sit in an undetected install dir): no setup warning.
      if (s.connected) { /* ok */ }
      else if (ini && !ini.found) {
        w.push(warning('!', 'API Stats de Rocket League non configurée', 'Fichier introuvable' + (ini.path ? ' (<code>' + esc(ini.path) + '</code>)' : '') + '. Lancez <code>rltracker setup</code>, puis redémarrez Rocket League.'));
      } else if (ini && !ini.ok) {
        w.push(warning('!', 'API Stats de Rocket League désactivée', 'PacketSendRate = ' + esc(ini.packet_send_rate) + '. Lancez <code>rltracker setup</code> (ou mettez PacketSendRate=30 dans <code>' + esc(ini.path || 'DefaultStatsAPI.ini') + '</code>), puis redémarrez Rocket League.'));
      } else if (!s.connected) {
        w.push(warning('i', 'En attente de Rocket League', 'Lancez le jeu : la connexion se fera automatiquement. Si le jeu est déjà ouvert, redémarrez-le après <code>rltracker setup</code>.', true));
      }
    }
    $('#warnings').innerHTML = w.join('');
    var ver = s && s.version ? 'Rocket Tracker v' + s.version : 'Rocket Tracker';
    $('#footer-version').textContent = ver + (MOCK ? ' · mode démo' : '');

    var live = $('#live');
    if (s && s.in_match && s.live) {
      var L = s.live, me = L.me || {};
      live.hidden = false;
      live.innerHTML =
        '<div><div class="live-tag"><span class="dot"></span>En direct</div><div class="live-meta">' + esc(L.mode || '') + ' · ' + esc(prettyArena(L.arena)) +
          (L.me ? ' · équipe ' + (L.my_team === 1 ? 'orange' : 'bleue') : '') + '</div></div>' +
        '<div class="live-score" aria-label="Score"><span class="us" title="Votre équipe">' + fmtNum(L.team_score) + '</span>' +
        '<span class="clock' + (L.overtime ? ' ot' : '') + '">' + (L.overtime ? '+' : '') + fmtClock(L.time_seconds) + '</span>' +
        '<span class="them" title="Adversaires">' + fmtNum(L.opp_score) + '</span></div>' +
        '<div class="live-me">' + [['Score', me.score], ['Buts', me.goals], ['Passes', me.assists], ['Arrêts', me.saves], ['Tirs', me.shots]]
          .map(function (x) { return '<span><b>' + fmtNum(x[1]) + '</b>' + x[0] + '</span>'; }).join('') + '</div>';
    } else {
      live.hidden = true;
      live.innerHTML = '';
    }
  }
  function warning(icon, title, body, info) {
    return '<div class="warning' + (info ? ' info' : '') + '"><span class="w-icon">' + icon + '</span><div><strong>' + esc(title) + '</strong><p>' + body + '</p></div></div>';
  }

  /* ---------- filters ---------- */
  function syncFilterUi() {
    var f = state.filters;
    $$('#f-mode button').forEach(function (b) { b.setAttribute('role', 'radio'); b.setAttribute('aria-checked', String(b.dataset.v === f.mode)); });
    $$('#f-period button').forEach(function (b) { b.setAttribute('role', 'radio'); b.setAttribute('aria-checked', String(b.dataset.v === String(f.period))); });
    var tagSel = $('#f-tag');
    var tags = TAGS.slice();
    state.all.forEach(function (m) { if (m.tag && tags.indexOf(m.tag) < 0) tags.push(m.tag); });
    tagSel.innerHTML = '<option value="all">Tous</option>' + tags.map(function (t) { return '<option value="' + esc(t) + '">' + esc(TAG_LABELS[t] || t) + '</option>'; }).join('');
    tagSel.value = f.tag;
    if (tagSel.value !== f.tag) { f.tag = 'all'; tagSel.value = 'all'; }
    $('#f-online').checked = !!f.onlineOnly;
    $('#f-abandoned').checked = !!f.excludeAbandoned;
  }

  function bindFilters() {
    function seg(id, key) {
      $(id).addEventListener('click', function (e) {
        var b = e.target.closest('button[data-v]');
        if (!b) return;
        state.filters[key] = b.dataset.v;
        onFiltersChanged();
      });
    }
    seg('#f-mode', 'mode');
    seg('#f-period', 'period');
    $('#f-tag').addEventListener('change', function (e) { state.filters.tag = e.target.value; onFiltersChanged(); });
    $('#f-online').addEventListener('change', function (e) { state.filters.onlineOnly = e.target.checked; onFiltersChanged(); });
    $('#f-abandoned').addEventListener('change', function (e) { state.filters.excludeAbandoned = e.target.checked; onFiltersChanged(); });
  }
  function onFiltersChanged() { saveFilters(); state.shown = PAGE_SIZE; renderAll(); }

  /* ---------- main render ---------- */
  function renderAll() {
    readTheme();
    applyChartDefaults();
    var view = state.route.view;
    syncNav();
    $('#view-history').hidden = view !== 'history';
    $('#view-match').hidden = view !== 'match';
    if (view !== 'dash') {
      $('#onboarding').hidden = true;
      $('#dashboard').hidden = true;
      try { if (view === 'history') renderHistory(); else renderMatchPage(); } catch (e) { console.error('render failed: ' + view, e); }
      return;
    }
    var has = state.all.length > 0;
    $('#onboarding').hidden = has || !state.loaded;
    // The season goal (and manual entry) is available even before the first tracked match.
    $('#dashboard').hidden = !state.loaded;
    $$('#dashboard > :not(#goal-section)').forEach(function (el) { if (!has) el.hidden = true; else if (el.id !== 'no-results' && el.id !== 'content') el.hidden = false; });
    try { renderGoal(); } catch (e) { console.error('render failed: renderGoal', e); }
    if (!has) { Object.keys(charts).forEach(function (k) { charts[k].destroy(); delete charts[k]; }); return; }
    syncFilterUi();
    var ms = filterMatches(state.all, state.filters);
    state.filtered = ms;
    var total = state.all.length;
    $('#filter-count').textContent = ms.length + ' match' + (ms.length > 1 ? 's' : '') + ' sur ' + total;
    var empty = ms.length === 0;
    $('#no-results').hidden = !empty;
    $('#content').hidden = empty;
    if (!empty) {
      var steps = [renderKpis, renderProgression, renderActivity, renderGoals, renderSituations, renderMental, renderTime, renderMechanics, renderFeed, renderArenas, renderMates];
      steps.forEach(function (fn) {
        try { fn(ms); } catch (e) { console.error('render failed:', fn.name, e); }
      });
    }
    renderMatches(ms);
  }

  /* ---------- data loading ---------- */
  function loadMatches() {
    return apiFetch('/api/matches').then(function (data) {
      state.all = annotate(Array.isArray(data) ? data : []);
      state.loaded = true;
      renderAll();
    }).catch(function (e) {
      console.warn('matches', e);
      state.loaded = true;
      if (!state.all.length) renderAll();
      toast('Impossible de charger les matchs (' + e.message + ')', true);
    });
  }
  function pollStatus() {
    return apiFetch('/api/status').then(function (s) {
      state.status = s; state.statusError = false;
      renderStatus();
      if (s && isNum(s.match_count)) {
        if (state.lastCount != null && s.match_count !== state.lastCount) loadMatches();
        state.lastCount = s.match_count;
      }
    }).catch(function () {
      state.statusError = true;
      renderStatus();
    });
  }

  /* ---------- table interactions ---------- */
  function bindTable() {
    var tbl = $('#t-matches');
    tbl.addEventListener('click', function (e) {
      var del = e.target.closest('[data-del]');
      if (del) { e.stopPropagation(); deleteMatch(+del.dataset.del); return; }
      if (e.target.closest('select, button, a')) return;
      var tr = e.target.closest('tr.row');
      if (!tr) return;
      toggleRow(+tr.dataset.id);
    });
    tbl.addEventListener('keydown', function (e) {
      if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('tr.row')) { e.preventDefault(); toggleRow(+e.target.dataset.id); }
    });
    tbl.addEventListener('change', function (e) {
      var sel = e.target.closest('[data-tag-for]');
      if (sel) updateTag(+sel.dataset.tagFor, sel.value, sel);
    });
    $('#more-matches').addEventListener('click', function () { state.shown += PAGE_SIZE; renderMatches(state.filtered); });
  }
  function toggleRow(id) {
    if (state.open[id]) delete state.open[id]; else state.open[id] = true;
    renderMatches(state.filtered);
    var row = $('#t-matches tr.row[data-id="' + id + '"]');
    if (row) row.focus({ preventScroll: true });
  }
  function findMatch(id) { return state.all.find(function (m) { return m.id === id; }); }
  function updateTag(id, tag, sel) {
    var m = findMatch(id);
    var prev = m ? m.tag : null;
    sel.disabled = true;
    apiFetch('/api/matches/' + id, { method: 'PATCH', body: JSON.stringify({ tag: tag }) }).then(function (upd) {
      if (m) m.tag = (upd && upd.tag) || tag;
      toast('Type mis à jour : ' + (TAG_LABELS[tag] || tag));
      renderAll();
    }).catch(function (e) {
      sel.value = prev; sel.disabled = false;
      toast('Échec de la mise à jour (' + e.message + ')', true);
    });
  }
  function deleteMatch(id, after) {
    var m = findMatch(id);
    if (!m) return;
    var desc = fmtDate(m._t, true) + ' · ' + (m.mode || '') + ' · ' + num(m.team_score) + '–' + num(m.opp_score);
    if (!window.confirm('Supprimer définitivement ce match ?\n\n' + desc)) return;
    apiFetch('/api/matches/' + id, { method: 'DELETE' }).then(function () {
      state.all = annotate(state.all.filter(function (x) { return x.id !== id; }));
      delete state.open[id];
      if (state.lastCount != null) state.lastCount = Math.max(0, state.lastCount - 1);
      toast('Match supprimé');
      if (after) after(); else renderAll();
    }).catch(function (e) { toast('Suppression impossible (' + e.message + ')', true); });
  }

  /* ---------- settings ---------- */
  var currentConfig = null;
  function openSettings() {
    var dlg = $('#settings');
    var form = $('#settings-form');
    var msg = $('#settings-msg');
    msg.textContent = 'Chargement…'; msg.className = 'form-msg';
    form.elements.default_tag.innerHTML = tagOptions('ranked');
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
    apiFetch('/api/config').then(function (c) {
      currentConfig = c || {};
      form.elements.player_names.value = (currentConfig.player_names || []).join(', ');
      form.elements.player_ids.value = (currentConfig.player_ids || []).join(', ');
      form.elements.default_tag.innerHTML = tagOptions(currentConfig.default_tag || 'ranked');
      form.elements.rl_install_dir.value = currentConfig.rl_install_dir || '';
      var goal = Object.assign({}, GOAL_DEFAULT, currentConfig.goal || {});
      form.elements.goal_mode.value = goal.mode;
      form.elements.goal_daily.value = goal.daily;
      form.elements.goal_start.value = goal.season_start || '';
      form.elements.goal_end.value = goal.season_end || '';
      msg.textContent = '';
    }).catch(function (e) { msg.textContent = 'Impossible de lire la configuration (' + e.message + ')'; msg.className = 'form-msg err'; });
  }
  function bindSettings() {
    var dlg = $('#settings');
    var form = $('#settings-form');
    $('#open-settings').addEventListener('click', openSettings);
    $$('[data-open-settings]').forEach(function (b) { b.addEventListener('click', openSettings); });
    $$('[data-close]', dlg).forEach(function (b) { b.addEventListener('click', function () { dlg.close ? dlg.close() : dlg.removeAttribute('open'); }); });
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var split = function (s) { return s.split(',').map(function (x) { return x.trim(); }).filter(Boolean); };
      var cfg = Object.assign({}, currentConfig || {}, {
        player_names: split(form.elements.player_names.value),
        player_ids: split(form.elements.player_ids.value),
        default_tag: form.elements.default_tag.value,
        rl_install_dir: form.elements.rl_install_dir.value.trim(),
        goal: {
          mode: form.elements.goal_mode.value,
          daily: parseInt(form.elements.goal_daily.value, 10) || GOAL_DEFAULT.daily,
          season_start: form.elements.goal_start.value,
          season_end: form.elements.goal_end.value
        }
      });
      var msg = $('#settings-msg');
      msg.textContent = 'Enregistrement…'; msg.className = 'form-msg';
      apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(cfg) }).then(function (c) {
        currentConfig = c || cfg;
        state.config = currentConfig;
        dlg.close ? dlg.close() : dlg.removeAttribute('open');
        toast('Réglages enregistrés');
        renderAll();
        pollStatus();
      }).catch(function (err) { msg.textContent = 'Échec de l’enregistrement (' + err.message + ')'; msg.className = 'form-msg err'; });
    });
  }

  /* ---------- manual games (day dialog) ---------- */
  var MANUAL_MODES = ['1v1', '2v2', '3v3', '4v4'];
  var dayDialogKey = null;
  function loadManual() {
    return apiFetch('/api/manual').then(function (d) { state.manual = Array.isArray(d) ? d : []; })
      .catch(function (e) { console.warn('manual', e); });
  }
  function manualFor(day, mode) {
    return state.manual.find(function (e) { return e.day === day && e.mode === mode; }) || { games: 0, wins: 0 };
  }
  function openDay(day) {
    dayDialogKey = day;
    var dlg = $('#day-dialog');
    var t = parseDay(day);
    var title = new Date(t).toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
    $('#day-title').textContent = title.charAt(0).toUpperCase() + title.slice(1);
    var tracked = trackedByMode(state.all, day);
    $('#day-rows').innerHTML = MANUAL_MODES.map(function (mode) {
      var tr = tracked[mode] || { games: 0, wins: 0 }, mn = manualFor(day, mode);
      return '<tr data-mode="' + mode + '"><th scope="row">' + mode + '</th>' +
        '<td class="r muted">' + (tr.games ? tr.games + ' <small>(' + tr.wins + ' V)</small>' : '—') + '</td>' +
        '<td><input type="number" min="0" max="500" step="1" inputmode="numeric" name="g-' + mode + '" value="' + num(mn.games) + '" aria-label="Games ' + mode + ' saisies"></td>' +
        '<td><input type="number" min="0" max="500" step="1" inputmode="numeric" name="w-' + mode + '" value="' + num(mn.wins) + '" aria-label="Victoires ' + mode + ' saisies"></td></tr>';
    }).join('');
    var msg = $('#day-msg'); msg.textContent = ''; msg.className = 'form-msg';
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
  }
  function bindDayDialog() {
    var dlg = $('#day-dialog'), form = $('#day-form');
    $('#goal-cal').addEventListener('click', function (e) {
      var b = e.target.closest('[data-day]');
      if (b) openDay(b.getAttribute('data-day'));
    });
    $$('[data-close]', dlg).forEach(function (b) { b.addEventListener('click', function () { dlg.close ? dlg.close() : dlg.removeAttribute('open'); }); });
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var day = dayDialogKey, msg = $('#day-msg'), changes = [];
      for (var i = 0; i < MANUAL_MODES.length; i++) {
        var mode = MANUAL_MODES[i];
        var g = parseInt(form.elements['g-' + mode].value || '0', 10), w = parseInt(form.elements['w-' + mode].value || '0', 10);
        if (!isNum(g) || !isNum(w) || g < 0 || w < 0 || g > 500) { msg.textContent = mode + ' : nombre invalide'; msg.className = 'form-msg err'; return; }
        if (w > g) { msg.textContent = mode + ' : plus de victoires que de games'; msg.className = 'form-msg err'; return; }
        var cur = manualFor(day, mode);
        if (g !== num(cur.games) || (g > 0 && w !== num(cur.wins))) changes.push({ mode: mode, games: g, wins: g ? w : 0 });
      }
      if (!changes.length) { dlg.close ? dlg.close() : dlg.removeAttribute('open'); return; }
      msg.textContent = 'Enregistrement…'; msg.className = 'form-msg';
      Promise.all(changes.map(function (c) {
        return apiFetch('/api/manual/' + day + '/' + c.mode, { method: 'PUT', body: JSON.stringify({ games: c.games, wins: c.wins }) });
      })).then(loadManual).then(function () {
        dlg.close ? dlg.close() : dlg.removeAttribute('open');
        toast('Games du ' + new Date(parseDay(day)).toLocaleDateString('fr-FR', { day: 'numeric', month: 'long' }) + ' enregistrées');
        renderAll();
      }).catch(function (err) {
        msg.textContent = 'Échec de l’enregistrement (' + err.message + ')'; msg.className = 'form-msg err';
        loadManual().then(renderAll);
      });
    });
  }

  var toastTimer = null;
  function toast(text, isErr) {
    var t = $('#toast');
    t.textContent = text;
    t.className = 'toast' + (isErr ? ' err' : '');
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.hidden = true; }, isErr ? 5000 : 2500);
  }

  /* ---------- boot ---------- */
  function boot() {
    bindFilters();
    bindTable();
    bindSettings();
    bindDayDialog();
    bindHistory();
    bindMatchView();
    window.addEventListener('hashchange', onRoute);
    syncNav();
    if (state.route.view !== 'dash') renderAll();
    if (MOCK) {
      $('#export-csv').addEventListener('click', function (e) { e.preventDefault(); toast('Export CSV indisponible en mode démo'); });
    }
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: light)');
      var onTheme = function () { renderAll(); renderStatus(); };
      if (mq.addEventListener) mq.addEventListener('change', onTheme); else if (mq.addListener) mq.addListener(onTheme);
    }
    readTheme();
    applyChartDefaults();
    var cfgReady = apiFetch('/api/config').then(function (c) { state.config = c || null; }).catch(function (e) { console.warn('config', e); });
    Promise.all([pollStatus(), Promise.all([cfgReady, loadManual()]).then(loadMatches)]).then(function () {
      if (state.status && isNum(state.status.match_count)) state.lastCount = state.status.match_count;
    });
    setInterval(pollStatus, 3000);
    // Re-render the activity window when the day changes (cheap, once per 10 min).
    setInterval(function () { if (state.all.length) renderAll(); }, 10 * 60 * 1000);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot); else boot();
})(typeof globalThis !== 'undefined' ? globalThis : this);
