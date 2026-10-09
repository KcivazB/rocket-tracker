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

  var I18N = root.RT_I18N || (typeof require === 'function' ? require('./i18n.js') : null);
  var tr = I18N.t;
  function L() { return I18N.locale(); }

  var TAGS = ['ranked', 'casual', 'tournament', 'private', 'other'];
  function tagLabel(tag) { return tag && I18N.has('tag.' + tag) ? tr('tag.' + tag) : (tag || '—'); }
  /** "3 matchs" / "3 matches": n followed by the plural form of unit (see unit.* keys). */
  function plural(n, unit) { return fmtNum(n) + ' ' + tr('unit.' + unit, { n: n }); }

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
    for (var h = 0; h < 24; h++) hours.push({ label: tr('time.hour', { h: h }), wins: 0, n: 0 });
    for (var d = 0; d < 7; d++) days.push({ label: tr('weekdays.short')[d], wins: 0, n: 0 });
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
  function weekdayName(i) { return tr('weekdays.long')[i]; }
  var TIME_MIN_GAMES = 20;   // below this, no verdicts at all
  var TIME_SMOOTH_K = 10;    // pseudo-games pulling small samples toward the overall win rate
  var DAY_START_H = 6;       // hours are ordered from 6h so late-night sessions stay contiguous
  var TIME_MIN_GAP = 3;      // pts from the average below which a best/worst verdict isn't worth stating
  // "entre 20h et 23h" / "between 20:00 and 23:00" for a window [from, from+len).
  function hourRange(from, len) { return tr('time.between', { from: from, to: (from + len) % 24 }); }
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
    days.forEach(function (b) { describe(b); b.name = weekdayName(b.idx); });
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
    var labels = ['0–1′', '1–2′', '2–3′', '3–4′', '4–5′', tr('goals.ot')];
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
    { key: 'avg_speed', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'supersonic_pct', unit: '%', digits: 1, higherBetter: true },
    { key: 'air_pct', unit: '%', digits: 1, higherBetter: true },
    { key: 'avg_boost', unit: '', digits: 0, higherBetter: null },
    { key: 'zero_boost_pct', unit: '%', digits: 1, higherBetter: false },
    { key: 'hits_avg', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true, src: 'hits' }
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
        key: metric.key, label: tr('mech.' + metric.key), unit: metric.unit, digits: metric.digits, higherBetter: metric.higherBetter,
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

  var BASIC_FEED = { Goal: 1, Assist: 1, Save: 1, Shot: 1, Win: 1, MVP: 1 };

  function statfeedLabel(name) {
    if (!name) return '—';
    if (I18N.has('feed.' + name)) return tr('feed.' + name);
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
  // Arena name suffix -> arena.* translation key ('' = not shown).
  var ARENA_VARIANTS = {
    day: 'day', night: 'night', rainy: 'rain', rain: 'rain', snowy: 'snow', snow: 'snow', dawn: 'dawn', dusk: 'dusk',
    foggy: 'fog', stormy: 'storm', winter: 'winter', toon: 'toon', spooky: 'spooky', season: 'season', fire: 'fire',
    lava: 'lava', dark: 'night', grs: '', standard: '', p: ''
  };

  function prettyArena(code) {
    if (!code) return tr('arena.unknown');
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
      return v == null ? p.replace(/([a-z])([A-Z])/g, '$1 $2') : v && tr('arena.' + v);
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
  /** Hash route: '#/' dashboard, '#/history' history, '#/match/<id>' match page (id null when invalid),
   *  '#/players' players & leaderboard. Server mode: '#/player/<handle>[/history|/match/<id>]' are another
   *  player's pages (player = handle, null for your own). */
  function parseRoute(hash) {
    var h = String(hash || '').replace(/^#\/?/, '').replace(/\/+$/, '');
    try { h = decodeURIComponent(h); } catch (e) { /* keep raw */ }
    // Routes of the French-only versions (bookmarks).
    h = h.replace(/^joueurs$/, 'players').replace(/^joueur\//, 'player/').replace(/(^|\/)historique$/, '$1history');
    var player = null;
    var pm = /^player\/([^/]+)(?:\/(.*))?$/.exec(h);
    if (pm) { player = pm[1].toLowerCase(); h = pm[2] || ''; }
    else if (h === 'players') return { view: 'players', id: null, player: null };
    if (h === 'history') return { view: 'history', id: null, player: player };
    var r = /^match\/(\d+)$/.exec(h);
    if (r) return { view: 'match', id: +r[1], player: player };
    if (/^match(\/|$)/.test(h)) return { view: 'match', id: null, player: player };
    return { view: 'dash', id: null, player: player };
  }
  /** Hash of a page ('' dashboard, 'history', 'match/<id>') of a player (null = your own). */
  function routeHash(player, path) {
    return '#/' + (player ? 'player/' + encodeURIComponent(player) + (path ? '/' : '') : '') + (path || '');
  }

  /* ---------- players & leaderboard (pure) ---------- */
  function playerHaystack(p) {
    return normText([p.name, p.handle].concat(p.game_names || []).join(' '));
  }
  /** Players whose name, handle or in-game names contain every search term. */
  function filterPlayers(ps, q) {
    var terms = searchTerms(q);
    return (ps || []).filter(function (p) {
      var hay = playerHaystack(p);
      return terms.every(function (t) { return hay.indexOf(t) >= 0; });
    });
  }
  var LB_SORTS = {
    name: function (r) { return normText(r.name); },
    games: function (r) { return r.games; },
    winrate: function (r) { return r.winrate; },
    goal_diff_avg: function (r) { return r.goal_diff_avg; },
    score_avg: function (r) { return r.score_avg; },
    goals_avg: function (r) { return r.goals_avg; },
    assists_avg: function (r) { return r.assists_avg; },
    saves_avg: function (r) { return r.saves_avg; },
    shots_avg: function (r) { return r.shots_avg; },
    mvp_rate: function (r) { return r.mvp_rate; }
  };
  /** Leaderboard rows with at least minGames decided games, sorted (dir -1 = descending) and ranked. */
  function rankLeaders(rows, key, dir, minGames) {
    var get = LB_SORTS[key] || LB_SORTS.winrate;
    dir = dir === 1 ? 1 : -1;
    var out = (rows || []).filter(function (r) { return num(r.games) >= (minGames || 0); }).map(function (r) {
      return Object.assign({}, r, { mvp_rate: ratio(num(r.mvps), num(r.games)) });
    });
    out.sort(function (a, b) {
      var x = get(a), y = get(b);
      if (x == null && y == null) return num(b.games) - num(a.games);
      if (x == null) return 1;
      if (y == null) return -1;
      var c = typeof x === 'string' ? x.localeCompare(y) : x - y;
      return c * dir || num(b.games) - num(a.games) || String(a.name).localeCompare(String(b.name));
    });
    out.forEach(function (r, i) { r.rank = i + 1; });
    return out;
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

  // lk: translation key of the label (see statLabel).
  var PERF_STATS = [
    { key: 'score', lk: 'perf.score', digits: 0, higherBetter: true },
    { key: 'goals', lk: 'perf.goals', digits: 0, higherBetter: true },
    { key: 'assists', lk: 'perf.assists', digits: 0, higherBetter: true },
    { key: 'saves', lk: 'perf.saves', digits: 0, higherBetter: true },
    { key: 'shots', lk: 'perf.shots', digits: 0, higherBetter: true },
    { key: 'touches', lk: 'perf.touches', digits: 0, higherBetter: true },
    { key: 'demos', lk: 'perf.demos', digits: 0, higherBetter: true }
  ];
  var MOVE_STATS = [
    { key: 'avg_speed', lk: 'mech.avg_speed', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'supersonic_pct', lk: 'mech.supersonic_pct', unit: '%', digits: 1, higherBetter: true },
    { key: 'avg_boost', lk: 'mech.avg_boost', unit: '', digits: 0, higherBetter: null },
    { key: 'zero_boost_pct', lk: 'mech.zero_boost_pct', unit: '%', digits: 1, higherBetter: false },
    { key: 'full_boost_pct', lk: 'mech.full_boost_pct', unit: '%', digits: 1, higherBetter: null },
    { key: 'boosting_pct', lk: 'mech.boosting_pct', unit: '%', digits: 1, higherBetter: null },
    { key: 'powerslide_pct', lk: 'mech.powerslide_pct', unit: '%', digits: 1, higherBetter: null },
    { key: 'demolished_s', lk: 'mech.demolished_s', unit: 's', digits: 1, higherBetter: false },
    { key: 'ground_pct', lk: 'mech.ground_pct', unit: '%', digits: 1, split: true },
    { key: 'wall_pct', lk: 'mech.wall_pct', unit: '%', digits: 1, split: true },
    { key: 'air_pct', lk: 'mech.air_split', unit: '%', digits: 1, split: true }
  ];
  var HIT_STATS = [
    { key: 'count', lk: 'hits.count', unit: '', digits: 0, higherBetter: null },
    { key: 'avg_speed', lk: 'hits.avg_speed', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true },
    { key: 'max_speed', lk: 'hits.max_speed', unit: 'km/h', conv: kmh, digits: 0, higherBetter: true }
  ];
  function statLabel(s) { return tr(s.lk); }
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

  /* ---------- formatting (pure, follows the language) ---------- */
  var nfCache = {};
  function nf(d) {
    var k = L() + '|' + d;
    if (!nfCache[k]) nfCache[k] = new Intl.NumberFormat(L(), { minimumFractionDigits: d, maximumFractionDigits: d });
    return nfCache[k];
  }
  function pct(s) { return tr('fmt.pct', { v: s }); }
  function fmtNum(x, d) { return isNum(x) ? nf(d || 0).format(x) : '—'; }
  function fmtPct(r, d) { return isNum(r) ? pct(nf(d || 0).format(r * 100)) : '—'; }
  function fmtPct100(x, d) { return isNum(x) ? pct(nf(d || 0).format(x)) : '—'; }
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
    return h ? h + ' h' + (mnt ? ' ' + String(mnt).padStart(2, '0') : '') : mnt + ' min';
  }

  var api = {
    ROLL_WINDOW: ROLL_WINDOW, SESSION_GAP_MS: SESSION_GAP_MS, UU_TO_KMH: UU_TO_KMH, TAGS: TAGS, tagLabel: tagLabel, plural: plural,
    DEFAULT_FILTERS: DEFAULT_FILTERS, MECH_METRICS: MECH_METRICS,
    isNum: isNum, ratio: ratio, regulationS: regulationS, mean: mean, wl: wl, kmh: kmh, annotate: annotate, modeKey: modeKey, filterMatches: filterMatches,
    streaks: streaks, computeKpis: computeKpis, rolling: rolling, computeProgression: computeProgression,
    computeActivity: computeActivity, computeGoalDiffDist: computeGoalDiffDist, computeMental: computeMental,
    computeSituations: computeSituations, computeGoalsPerMinute: computeGoalsPerMinute, computeMechanics: computeMechanics,
    computeStatfeed: computeStatfeed, statfeedLabel: statfeedLabel, prettyArena: prettyArena, computeArenas: computeArenas,
    computeTeammates: computeTeammates, computeGoal: computeGoal, computeTimeInsights: computeTimeInsights, trackedByMode: trackedByMode, trackerUrl: trackerUrl, fmtNum: fmtNum, fmtPct: fmtPct, fmtPct100: fmtPct100, fmtSigned: fmtSigned,
    fmtClock: fmtClock, fmtHours: fmtHours, dayKey: dayKey,
    parseRoute: parseRoute, routeHash: routeHash, filterPlayers: filterPlayers, rankLeaders: rankLeaders, DEFAULT_HIST_FILTERS: DEFAULT_HIST_FILTERS, filterHistory: filterHistory, filterManualHistory: filterManualHistory,
    groupHistory: groupHistory, paginateGroups: paginateGroups, summarizeHistory: summarizeHistory, matchNeighbors: matchNeighbors,
    matchContext: matchContext, matchBaseline: matchBaseline, statValue: statValue, PERF_STATS: PERF_STATS, MOVE_STATS: MOVE_STATS, HIT_STATS: HIT_STATS
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.RT = api;

  if (typeof window === 'undefined' || typeof document === 'undefined') return;

  /* ===================================================================== */
  /* Part 2 — UI                                                           */
  /* ===================================================================== */

  // Language: saved choice, else the browser's (see i18n.js). Changing it reloads the page.
  I18N.setLang(I18N.detect());

  var $ = function (sel, el) { return (el || document).querySelector(sel); };
  var $$ = function (sel, el) { return Array.prototype.slice.call((el || document).querySelectorAll(sel)); };
  function playerName(name, primaryId) {
    var url = trackerUrl(name, primaryId);
    var label = esc(name || '?');
    return url ? '<a class="trn" href="' + esc(url) + '" target="_blank" rel="noopener noreferrer" title="' + esc(tr('ui.trnTip')) + '">' + label + '</a>' : label;
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  var params = new URLSearchParams(location.search);
  var MOCK = params.get('mock');
  var PAGE_SIZE = 20;
  var DEFAULT_LB = { mode: 'all', period: '30', tag: 'all', min: 5, sort: 'winrate', dir: -1 };

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
    histScroll: 0,
    // Server mode (rltracker-server): signed-in user, whose data is loaded, players page.
    mode: 'local',
    me: null,
    dataFor: '',   // handle of the player whose data is loaded ('' = yours)
    gen: 0,        // bumped when dataFor changes; stale responses are dropped
    players: null,
    leaders: null,
    lb: loadLbFilters(),
    playersQ: ''
  };
  var HIST_PAGE = 100;
  var charts = {};

  function isServer() { return state.mode === 'server'; }
  /** Another player's pages are read-only. */
  function readOnly() { return !!state.route.player; }
  function apiBase() { return state.dataFor ? '/api/players/' + encodeURIComponent(state.dataFor) : '/api'; }
  /** Link to a page of the player being viewed ('' dashboard, 'history', 'match/<id>'). */
  function href(path) { return routeHash(state.route.player, path); }
  function viewedPlayer() {
    var h = state.route.player;
    return h ? (state.players || []).find(function (p) { return p.handle === h; }) || { handle: h, name: h, game_names: [] } : null;
  }
  function loadLbFilters() {
    var f = Object.assign({}, DEFAULT_LB);
    try {
      var s = JSON.parse(localStorage.getItem('rt.leaderboard') || 'null');
      if (s && typeof s === 'object') Object.keys(f).forEach(function (k) { if (s[k] != null && typeof s[k] === typeof f[k]) f[k] = s[k]; });
    } catch (e) { /* storage unavailable */ }
    return f;
  }
  function saveLbFilters() { try { localStorage.setItem('rt.leaderboard', JSON.stringify(state.lb)); } catch (e) { /* ignore */ } }

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
        if (r.status === 401) {
          // Server mode, session expired: sign in again and come back here.
          location.href = '/auth/login?next=' + encodeURIComponent(location.pathname + location.search + location.hash);
          return new Promise(function () {});
        }
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
      if (path === '/api/session') return { mode: 'local', version: '0.1.0-mock' };
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
    return d.toLocaleString(L(), o);
  }
  function fmtDay(t, o) { return new Date(t).toLocaleDateString(L(), o); }
  function hasData(arr) { return arr.some(function (v) { return v != null; }); }
  function wlShort(w, l) { return tr('wl.short', { w: w, l: l }); }

  /* ---------- render: season goal ---------- */
  function goalModeLabel(mode) { return mode === 'all' ? tr('goal.allModes') : mode; }
  function goalKpi(label, value, sub, pct, cls) {
    return '<div class="card kpi goal-kpi' + (cls ? ' ' + cls : '') + '"><span class="kpi-label">' + label + '</span>' +
      '<span class="kpi-value">' + value + '</span>' +
      (pct != null ? '<div class="goal-meter" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(Math.min(1, pct) * 100) + '"><span style="width:' + (Math.min(1, Math.max(0, pct)) * 100).toFixed(1) + '%"></span></div>' : '') +
      '<span class="kpi-sub">' + sub + '</span></div>';
  }
  function renderGoal() {
    var g = computeGoal(state.all, state.config && state.config.goal, null, state.manual);
    var fmtD = function (t) { return fmtDay(t, { day: 'numeric', month: 'short', year: 'numeric' }); };
    $('#goal-sub').textContent = tr('goal.sub', { daily: g.daily, mode: goalModeLabel(g.mode), from: fmtD(g.start), to: fmtD(g.end), days: g.days.length });

    var t = g.today, left = Math.max(0, g.daily - t.count);
    var perDay = '<small>' + tr('goal.perDay') + '</small>';
    var todaySub = g.before ? tr('goal.startsOn', { date: fmtD(g.start) })
      : g.after ? tr('goal.seasonOver')
      : left === 0 ? '<span class="pos">' + tr('goal.dailyReached') + '</span>' + (t.count > g.daily ? ' (+' + (t.count - g.daily) + ')' : '')
      : tr('goal.leftToday', { games: plural(left, 'game') });
    if (t.count) todaySub += ' · ' + wlShort(t.wins, t.losses);
    var html =
      goalKpi(tr('goal.today'), fmtNum(t.count) + '<small> / ' + g.daily + '</small>', todaySub, t.count / g.daily, left === 0 && !g.before && !g.after ? 'done' : '') +
      goalKpi(tr('goal.seasonGames'), fmtNum(g.total) + '<small> / ' + fmtNum(g.target) + '</small>',
        tr('goal.dayOf', { d: g.elapsedDays, total: g.days.length }) + (g.wr != null ? ' · ' + tr('goal.wonPct', { pct: fmtPct(g.wr) }) : ''), g.target ? g.total / g.target : 0) +
      goalKpi(tr('goal.daysMet'), fmtNum(g.daysMet) + '<small> / ' + fmtNum(g.elapsedDays) + '</small>',
        g.elapsedDays ? tr('goal.ofElapsed', { pct: fmtPct(g.daysMet / g.elapsedDays) }) : tr('goal.noneElapsed'), null) +
      goalKpi(tr('goal.streak'), plural(g.curStreak, 'day'), tr('goal.best', { v: plural(g.bestStreak, 'day') }), null, g.curStreak > 0 ? 'streak' : '') +
      goalKpi(tr('goal.pace'), g.avgPerDay != null ? fmtNum(g.avgPerDay, 1) + perDay : '—',
        g.projection != null ? tr('goal.projection', { v: '<b>' + fmtNum(g.projection) + '</b>' }) : tr('goal.noData'), null,
        g.projection != null ? (g.projection >= g.target ? 'on-track' : 'behind') : '') +
      goalKpi(tr('goal.toReach', { v: fmtNum(g.target) }), g.neededPerDay != null ? fmtNum(g.neededPerDay) + perDay : '—',
        g.neededPerDay == null ? tr('goal.seasonOver') : g.total >= g.target ? '<span class="pos">' + tr('goal.seasonReached') + '</span>'
          : tr('goal.leftIn', { games: plural(g.target - g.total, 'game'), days: plural(g.remainingDays, 'day'), n: g.target - g.total }), null);
    $('#goal-kpis').innerHTML = html;
    $('#goal-hint').hidden = readOnly() || !!(state.config && state.config.goal && state.config.goal.season_start);

    // One grid per month, Monday first.
    var months = [], cur = null;
    g.days.forEach(function (d) {
      var dt = new Date(d.t), mk = dt.getFullYear() * 12 + dt.getMonth();
      if (!cur || cur.mk !== mk) { cur = { mk: mk, days: [] }; months.push(cur); }
      cur.days.push(d);
    });
    var dow = tr('weekdays.initial');
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
        var tip = fmtDay(d.t, { weekday: 'long', day: 'numeric', month: 'long' }) + ' · ' +
          (d.future ? tr('goal.upcoming') : plural(d.count, 'game') + (d.count ? ' (' + wlShort(d.wins, d.losses) + ')' : '') +
            (d.manual ? ' · ' + tr('goal.byHand', { n: d.manual }) : '') + (d.met ? ' · ' + tr('goal.reached') : ''));
        var inner = '<span class="dn">' + new Date(d.t).getDate() + '</span>' + (d.future ? '' : '<span class="dc">' + d.count + '</span>');
        cells += d.future || readOnly()
          ? '<span class="' + cls + '" title="' + esc(tip) + '" aria-label="' + esc(tip) + '">' + inner + '</span>'
          : '<button type="button" class="' + cls + '" data-day="' + d.key + '" title="' + esc(tip + ' · ' + tr('goal.clickToAdd')) + '" aria-label="' + esc(tip + '. ' + tr('goal.addGames')) + '">' + inner + '</button>';
      });
      var name = fmtDay(first, { month: 'long', year: 'numeric' });
      var mTotal = mo.days.reduce(function (a, d) { return a + d.count; }, 0);
      var mMet = mo.days.filter(function (d) { return d.met; }).length;
      return '<div class="card cal-month"><div class="cal-head"><span class="card-title">' + esc(name.charAt(0).toUpperCase() + name.slice(1)) + '</span>' +
        '<span class="card-meta">' + plural(mTotal, 'game') + ' · ' + tr('goal.monthMet', { v: mMet }) + '</span></div>' +
        '<div class="cal-dow">' + dow.map(function (x) { return '<span>' + x + '</span>'; }).join('') + '</div>' +
        '<div class="cal-days">' + cells + '</div></div>';
    }).join('');
  }

  /* ---------- render: KPIs ---------- */
  function renderKpis(ms) {
    var k = computeKpis(ms);
    var st = k.streaks;
    var recent = ms.slice(-20);
    var curTxt = st.current ? st.current.len + ' ' + tr('res.' + st.current.type + '.short') : '—';
    var curCls = st.current ? (st.current.type === 'win' ? 'w' : 'l') : '';
    var curLabel = st.current ? tr(st.current.type === 'win' ? 'kpi.streakWins' : 'kpi.streakLosses', { n: st.current.len }) : '';
    var form = recent.map(function (m) {
      var cls = m.result === 'win' ? 'win' : m.result === 'loss' ? 'loss' : 'abandoned';
      var tip = fmtDate(m._t, true) + ' · ' + (m.mode || '') + ' · ' + num(m.team_score) + '–' + num(m.opp_score);
      return '<span class="form-cell ' + cls + '" title="' + esc(tip) + '">' + tr('res.' + cls + '.short') + '</span>';
    }).join('');
    var html = '';
    html += '<div class="card kpi hero">' +
      '<span class="kpi-label">' + tr('kpi.winrate') + '</span>' +
      '<span class="kpi-value">' + fmtPct(k.winrate) + '</span>' +
      '<div class="kpi-wl"><span class="wl"><i style="background:var(--win)"></i>' + k.wins + ' ' + tr('res.win.short') + '</span>' +
      '<span class="wl"><i style="background:var(--loss)"></i>' + k.losses + ' ' + tr('res.loss.short') + '</span>' +
      (k.abandoned ? '<span class="wl"><i style="background:var(--neutral)"></i>' + plural(k.abandoned, 'abandon') + '</span>' : '') + '</div>' +
      '<div class="form"><div class="form-head"><span>' + tr('kpi.form') + '</span><span>' + tr('kpi.formSub', { v: recent.length }) + '</span></div>' +
      '<div class="form-strip" style="--cells:' + Math.max(recent.length, 10) + '">' + form + '</div></div>' +
      '</div>';
    html += tile(tr('kpi.matches'), fmtNum(k.n), plural(k.sessions, 'session') + ' · ' + tr('kpi.played', { t: fmtHours(k.playSeconds) }));
    html += tile(tr('kpi.goalDiff'), fmtSigned(k.goalDiffAvg, 2), tr('kpi.scoredConceded', { s: fmtNum(k.teamGoalsAvg, 1), c: fmtNum(k.oppGoalsAvg, 1) }));
    html += tile(tr('kpi.avgScore'), fmtNum(k.avgScore), tr('kpi.bestScore', { v: fmtNum(k.bestScore) }));
    html += tile(tr('kpi.streak'), '<span class="streak-badge ' + curCls + '">' + curTxt + '</span>',
      (curLabel ? esc(curLabel) + ' · ' : '') + tr('kpi.bestStreak', { v: st.bestWin ? st.bestWin + ' ' + tr('res.win.short') : '—' }));
    html += '<div class="card kpi per-match"><span class="kpi-label">' + tr('kpi.perMatch') + '</span><div class="pm-grid">' +
      pm(tr('perf.goals'), k.goalsPer) + pm(tr('perf.assists'), k.assistsPer) + pm(tr('perf.saves'), k.savesPer) + pm(tr('perf.shots'), k.shotsPer) + '</div></div>';
    html += tile(tr('kpi.conversion'), fmtPct(k.conversion), tr('kpi.conversionSub', { g: fmtNum(k.goals), s: fmtNum(k.shots) }));
    html += tile(tr('kpi.mvpRate'), fmtPct(k.mvpRate), tr('kpi.mvpSub', { v: k.mvp, pct: fmtPct(k.mvpOfWins) }));
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
    var empty = hasData(data) ? null : tr('prog.notEnough', { min: ROLL_MIN });
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
              title: function (it) { var m = ms[it[0].dataIndex]; return tr('prog.matchNo', { i: it[0].dataIndex + 1 }) + ' · ' + fmtDate(m._t, true); },
              label: function (it) { return ' ' + tr('fmt.labelValue', { label: o.label, v: o.fmt(it.raw) }); },
              footer: function () { return tr('prog.avgOfLast', { w: ROLL_WINDOW }); }
            }
          }
        }
      }
    }, empty);
  }

  function lastVal(arr) { for (var i = arr.length - 1; i >= 0; i--) if (arr[i] != null) return arr[i]; return null; }

  function renderProgression(ms) {
    var p = computeProgression(ms);
    var pctTick = function (v) { return pct(v); };
    rollingLine('c-roll-wr', p.winrate, T.us, ms, {
      label: tr('prog.winrate'), fill: true, ref: 50, refLabel: pct(50), fmt: function (v) { return fmtPct100(v); },
      y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: pctTick } }
    });
    $('#m-roll-wr').textContent = tr('prog.current', { v: fmtPct100(lastVal(p.winrate)) });
    rollingLine('c-roll-gd', p.goalDiff, T.us, ms, {
      label: tr('prog.goalDiff'), ref: 0, fmt: function (v) { return fmtSigned(v, 2); },
      y: { suggestedMin: -1.5, suggestedMax: 1.5, ticks: { color: T.muted, padding: 8, maxTicksLimit: 6, callback: function (v) { return fmtSigned(v, Number.isInteger(v) ? 0 : 1); } } }
    });
    $('#m-roll-gd').textContent = tr('prog.current', { v: fmtSigned(lastVal(p.goalDiff), 2) });
    rollingLine('c-roll-score', p.score, T.us, ms, { label: tr('perf.score'), fmt: function (v) { return fmtNum(v); }, y: { grace: '10%' } });
    $('#m-roll-score').textContent = tr('prog.current', { v: fmtNum(lastVal(p.score)) });

    // Small multiples (one series each) rather than 4 overlapping colored lines.
    ['goals', 'assists', 'saves', 'shots'].forEach(function (key) {
      var data = p[key], label = tr('perf.' + key);
      $('#mv-' + key).textContent = fmtNum(lastVal(data), 2);
      makeChart('c-roll-' + key, {
        type: 'line',
        data: { labels: ms.map(function (m, i) { return i + 1; }), datasets: [{ label: label, data: data, borderColor: T.us, backgroundColor: alpha(T.us, 0.10), fill: 'origin',
          pointHoverBackgroundColor: T.us, pointHoverBorderColor: T.surface, tension: 0.3, cubicInterpolationMode: 'monotone', spanGaps: true }] },
        options: {
          interaction: { mode: 'index', intersect: false },
          scales: baseScales({ x: { display: false }, y: { beginAtZero: true, ticks: { color: T.muted, padding: 6, maxTicksLimit: 3, callback: function (v) { return fmtNum(v, Number.isInteger(v) ? 0 : 1); } } } }),
          plugins: {
            crosshair: { enabled: true },
            tooltip: { displayColors: false, callbacks: {
              title: function (it) { var m = ms[it[0].dataIndex]; return tr('prog.matchNo', { i: it[0].dataIndex + 1 }) + ' · ' + fmtDate(m._t, true); },
              label: function (it) { return tr('prog.perMatch', { label: label, v: fmtNum(it.raw, 2) }); }
            } }
          }
        }
      }, hasData(data) ? null : tr('prog.notEnoughShort'));
    });
  }

  /* ---------- render: activity ---------- */
  function renderActivity(ms) {
    var a = computeActivity(ms, state.filters.period);
    var weekly = a.unit === 'week';
    $('#act-sub').textContent = tr(weekly ? 'act.subWeek' : 'act.subDay');
    var labels = a.buckets.map(function (b) { return fmtDay(b.t, { day: 'numeric', month: 'short' }); });
    var titleOf = function (i) {
      var b = a.buckets[i];
      return weekly ? tr('act.weekOf', { date: fmtDay(b.t, { day: 'numeric', month: 'long' }) })
        : fmtDay(b.t, { weekday: 'long', day: 'numeric', month: 'long' });
    };
    var hasOther = a.buckets.some(function (b) { return b.other > 0; });
    var items = [{ label: tr('act.wins'), color: T.win }, { label: tr('act.losses'), color: T.loss }];
    if (hasOther) items.push({ label: tr('act.abandons'), color: T.neutral });
    $('#lg-act').innerHTML = legendHtml(items);
    var ds = [
      { label: tr('act.wins'), data: a.buckets.map(function (b) { return b.wins; }), backgroundColor: T.win },
      { label: tr('act.losses'), data: a.buckets.map(function (b) { return b.losses; }), backgroundColor: T.loss }
    ];
    if (hasOther) ds.push({ label: tr('act.abandons'), data: a.buckets.map(function (b) { return b.other; }), backgroundColor: T.neutral });
    ds.forEach(function (d) { d.maxBarThickness = 24; d.borderColor = T.surface; d.borderWidth = { top: 2, bottom: 0, left: 0, right: 0 }; d.borderRadius = 3; d.borderSkipped = 'start'; d.stack = 's'; });
    makeChart('c-activity', {
      type: 'bar',
      data: { labels: labels, datasets: ds },
      options: {
        interaction: { mode: 'index', intersect: false },
        scales: baseScales({ x: { stacked: true, ticks: { maxTicksLimit: 10, color: T.muted, maxRotation: 0 } }, y: { stacked: true, beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { filter: function (it) { return it.raw > 0; }, callbacks: {
          title: function (it) { return titleOf(it[0].dataIndex); },
          label: function (it) { return ' ' + tr('fmt.labelValue', { label: it.dataset.label, v: it.raw }); },
          footer: function (it) { var b = a.buckets[it[0].dataIndex]; return plural(b.total, 'match') + ' · ' + tr('goal.wonPct', { pct: fmtPct(b.winrate) }); }
        } } }
      }
    }, a.buckets.length ? null : tr('common.noData'));

    var wr = a.buckets.map(function (b) { return b.winrate == null ? null : b.winrate * 100; });
    makeChart('c-activity-wr', {
      type: 'line',
      data: { labels: labels, datasets: [{ label: tr('prog.winrate'), data: wr, borderColor: alpha(T.us, 0.55), backgroundColor: T.us, spanGaps: true,
        pointRadius: a.buckets.length > 60 ? 2.5 : 4, pointBackgroundColor: T.us, pointBorderColor: T.surface, pointBorderWidth: 2, pointHoverRadius: 6,
        pointHoverBorderColor: T.surface, tension: 0, borderWidth: 1.5 }] },
      options: {
        interaction: { mode: 'nearest', axis: 'x', intersect: false },
        scales: baseScales({ x: { ticks: { maxTicksLimit: 6, color: T.muted, maxRotation: 0 } }, y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: function (v) { return pct(v); } } } }),
        plugins: { refLine: { value: 50 }, tooltip: { callbacks: {
          title: function (it) { return titleOf(it[0].dataIndex); },
          label: function (it) { var b = a.buckets[it.dataIndex]; return ' ' + fmtPct(b.winrate) + ' (' + wlShort(b.wins, b.losses) + ')'; }
        } } }
      }
    }, hasData(wr) ? null : tr('act.noDecidedPeriod'));
  }

  /* ---------- render: goals ---------- */
  function renderGoals(ms) {
    var d = computeGoalDiffDist(ms);
    $('#m-gd').textContent = tr('goals.decided', { n: d.n, v: fmtNum(d.n) });
    makeChart('c-gd', {
      type: 'bar',
      data: { labels: d.bins.map(function (b) { return b.label; }), datasets: [{
        label: tr('kpi.matches'), data: d.bins.map(function (b) { return b.count; }), maxBarThickness: 36,
        backgroundColor: d.bins.map(function (b) { return b.diff > 0 ? T.us : b.diff < 0 ? T.them : T.neutral; })
      }] },
      options: {
        scales: baseScales({ x: { title: { display: true, text: tr('goals.marginAxis'), color: T.muted, font: { size: 11.5 } } }, y: { beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { callbacks: {
          title: function (it) {
            // Label "+3", "−2" or "≥ +5": keep the ≥/≤ and the number, drop the sign.
            var b = d.bins[it[0].dataIndex], k = Math.abs(b.diff);
            var goals = b.label.replace(/[+−-]/g, '').replace('≤', '≥') + ' ' + tr('unit.goal', { n: k });
            return b.diff > 0 ? tr('goals.wonBy', { v: goals }) : b.diff < 0 ? tr('goals.lostBy', { v: goals }) : tr('goals.draw');
          },
          label: function (it) { return ' ' + plural(it.raw, 'match') + ' (' + fmtPct(ratio(it.raw, d.n)) + ')'; }
        } } }
      }
    }, d.n ? null : tr('goals.noDecided'));

    var g = computeGoalsPerMinute(ms);
    $('#lg-gpm').innerHTML = legendHtml([{ label: tr('goals.scored'), color: T.us }, { label: tr('goals.conceded'), color: T.them }]);
    makeChart('c-gpm', {
      type: 'bar',
      data: { labels: g.labels, datasets: [
        { label: tr('goals.scored'), data: g.us, backgroundColor: T.us, maxBarThickness: 22, borderColor: T.surface, borderWidth: { left: 1, right: 1, top: 0, bottom: 0 }, borderSkipped: 'start' },
        { label: tr('goals.conceded'), data: g.them, backgroundColor: T.them, maxBarThickness: 22, borderColor: T.surface, borderWidth: { left: 1, right: 1, top: 0, bottom: 0 }, borderSkipped: 'start' }
      ] },
      options: {
        interaction: { mode: 'index', intersect: false },
        datasets: { bar: { categoryPercentage: 0.6, barPercentage: 0.9 } },
        scales: baseScales({ x: { title: { display: true, text: tr('goals.minuteAxis'), color: T.muted, font: { size: 11.5 } } }, y: { beginAtZero: true, ticks: { precision: 0, color: T.muted, padding: 8, maxTicksLimit: 6 } } }),
        plugins: { tooltip: { callbacks: {
          title: function (it) { var i = it[0].dataIndex; return i === 5 ? tr('goals.overtime') : tr('goals.minute', { v: g.labels[i] }); },
          label: function (it) { return ' ' + tr('fmt.labelValue', { label: it.dataset.label, v: it.raw }) + ' (' + tr('goals.perMatch', { v: fmtNum(ratio(it.raw, g.matches), 2) }) + ')'; }
        } } }
      }
    }, g.goals ? null : tr('goals.none'));
  }

  /* ---------- render: situations ---------- */
  function meterRow(label, r, cls) {
    return '<div class="sit-row"><span>' + label + ' <span class="muted">· ' + plural(r.n, 'match') + '</span></span><b>' + fmtPct(r.wr) + '</b>' +
      '<div class="meter ' + (cls || '') + '" role="img" aria-label="' + esc(tr('sit.wrAria', { v: fmtPct(r.wr) })) + '"><span style="width:' + (isNum(r.wr) ? (r.wr * 100).toFixed(1) : 0) + '%"></span></div></div>';
  }
  function sitRow(label, v) { return '<div class="sit-row"><span>' + label + '</span><b>' + v + '</b></div>'; }
  function renderSituations(ms) {
    var s = computeSituations(ms);
    var html = '';
    html += '<div class="card sit"><h3>' + tr('sit.firstGoal') + '</h3><div class="sit-rows">' +
      meterRow(tr('sit.scoredFirst'), s.firstUs) + meterRow(tr('sit.concededFirst'), s.firstThem, 'them') + '</div>' +
      '<div class="sit-foot">' + tr('sit.firstGoalFoot') + (s.firstThem.wins ? ' · ' + tr('sit.comebacks', { n: s.firstThem.wins, v: fmtNum(s.firstThem.wins) }) : '') + '</div></div>';
    html += '<div class="card sit"><h3>' + tr('sit.close') + '</h3><div class="sit-rows">' +
      meterRow(tr('sit.margin1'), s.close) + meterRow(tr('sit.margin3'), s.big) + '</div>' +
      '<div class="sit-foot">' + tr('sit.closeFoot', { pct: fmtPct(s.closeShare) }) + '</div></div>';
    html += '<div class="card sit"><h3>' + tr('sit.ot') + '</h3><div class="sit-rows">' + meterRow(tr('sit.inOt'), s.ot) +
      sitRow(tr('sit.otShare'), fmtPct(s.otShare)) +
      sitRow(tr('sit.otLength'), isNum(s.otAvgSeconds) ? fmtClock(s.otAvgSeconds) : '—') + '</div></div>';
    html += '<div class="card sit"><h3>' + tr('sit.forfeits') + '</h3><div class="sit-rows">' +
      sitRow(tr('sit.forfeitWon'), s.forfeitWon) + sitRow(tr('sit.forfeitLost'), s.forfeitLost) +
      sitRow(tr('sit.abandoned'), state.filters.excludeAbandoned ? '<span class="muted" title="' + esc(tr('sit.hiddenTip')) + '">' + tr('sit.hidden') + '</span>' : s.abandoned) + '</div>' +
      '<div class="sit-foot">' + tr('sit.forfeitFoot') + '</div></div>';
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
      data: { labels: buckets.map(function (b) { return b.label; }), datasets: [{ label: tr('prog.winrate'), data: data, backgroundColor: colors, maxBarThickness: opts.thick || 24 }] },
      options: {
        scales: baseScales({ x: { ticks: { color: T.muted, maxRotation: 0, autoSkip: true, autoSkipPadding: 6 }, title: opts.xTitle ? { display: true, text: opts.xTitle, color: T.muted, font: { size: 11.5 } } : undefined },
          y: { min: 0, max: 100, ticks: { stepSize: 25, color: T.muted, padding: 8, callback: function (v) { return pct(v); } } } }),
        plugins: { refLine: { value: 50 }, tooltip: { callbacks: {
          title: function (it) { return opts.title ? opts.title(it[0].dataIndex) : buckets[it[0].dataIndex].label; },
          label: function (it) { var b = buckets[it.dataIndex]; return ' ' + tr('mental.wrOf', { v: fmtPct(b.wr) }); },
          footer: function (it) { var b = buckets[it[0].dataIndex]; return wlShort(b.wins, b.n - b.wins) + (b.n < minN ? ' · ' + tr('common.smallSample') : ''); }
        } } }
      }
    }, buckets.some(function (b) { return b.n > 0; }) ? null : tr('goal.noData'));
  }

  function renderMental(ms) {
    var m = computeMental(ms);
    var aw = m.afterWin, al = m.afterLoss;
    var verdict, bad = false;
    if (aw.n >= 10 && al.n >= 10) {
      var gap = (aw.wr - al.wr) * 100;
      if (gap >= 5) { bad = true; verdict = tr('mental.tilt', { v: fmtNum(gap) }); }
      else if (gap <= -5) verdict = tr('mental.bounce', { v: fmtNum(-gap) });
      else verdict = tr('mental.steady');
    } else {
      verdict = tr('mental.needMore');
    }
    var best = m.bySession.filter(function (b) { return b.n >= 5 && b.wr != null; }).sort(function (a, b) { return b.wr - a.wr; })[0];
    $('#tilt').innerHTML =
      '<h3>' + tr('mental.afterPrev') + '</h3>' +
      '<div class="tilt-pair">' +
      '<div class="tilt-box"><span class="lbl"><span class="res win">' + tr('res.win.short') + '</span>' + tr('mental.afterWin') + '</span><div class="val">' + fmtPct(aw.wr) + '</div><span class="n">' + plural(aw.n, 'match') + '</span></div>' +
      '<div class="tilt-box"><span class="lbl"><span class="res loss">' + tr('res.loss.short') + '</span>' + tr('mental.afterLoss') + '</span><div class="val">' + fmtPct(al.wr) + '</div><span class="n">' + plural(al.n, 'match') + '</span></div>' +
      '</div>' +
      '<div class="tilt-verdict' + (bad ? ' bad' : '') + '">' + verdict + '</div>' +
      '<div class="tilt-sessions"><div><b>' + m.sessions + '</b>' + tr('unit.session', { n: m.sessions }) + '</div><div><b>' + fmtNum(m.avgSessionLen, 1) + '</b>' + tr('mental.perSession') + '</div><div><b>' + (best ? tr('mental.bestNo', { v: best.label }) : '—') + '</b>' + tr('mental.bestOfSession') + '</div></div>';
    $('#m-sess').textContent = tr('mental.pale');
    wrBars('c-session', m.bySession, { xTitle: tr('mental.matchNoAxis'), title: function (i) { return i === 7 ? tr('mental.eighthPlus') : tr('mental.nthMatch', { v: ordinal(i + 1) }); } });
    // Day starts at 6h so late-night sessions stay contiguous; trim empty hours at both ends.
    var hours = m.byHour.slice(6).concat(m.byHour.slice(0, 6)).map(function (b, i) { return Object.assign({ h: (i + 6) % 24 }, b); });
    var firstH = hours.findIndex(function (b) { return b.n > 0; });
    var lastH = hours.length - 1 - hours.slice().reverse().findIndex(function (b) { return b.n > 0; });
    if (firstH >= 0) hours = hours.slice(Math.max(0, firstH - 1), Math.min(hours.length, lastH + 2));
    wrBars('c-hour', hours, { thick: 22, title: function (i) { var h = hours[i].h; return tr('time.fromTo', { a: h, b: (h + 1) % 24 }); } });
    wrBars('c-weekday', m.byWeekday, { thick: 32, title: function (i) { return cap(weekdayName(i)); } });
  }

  /* ---------- render: days & hours ---------- */
  function timeTile(cls, label, value, b, empty) {
    if (!b) return '<div class="card kpi time-tile ' + cls + '"><span class="kpi-label">' + label + '</span><span class="kpi-value muted">—</span><span class="kpi-sub">' + empty + '</span></div>';
    var d = b.delta, sign = d > 0 ? '+' : d < 0 ? '−' : '±';
    return '<div class="card kpi time-tile ' + cls + '"><span class="kpi-label">' + label + '</span>' +
      '<span class="kpi-value">' + value + '</span>' +
      '<span class="kpi-sub">' + tr('goal.wonPct', { pct: '<b>' + fmtPct(b.wr) + '</b>' }) + ' · ' + plural(b.n, 'match') +
      '<br>' + tr('time.vsAvg', { v: '<span class="' + (d >= 0 ? 'pos' : 'neg') + '">' + sign + fmtNum(Math.abs(d)) + ' pts</span>' }) + '</span></div>';
  }
  function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }
  /** "1er", "2e" / "1st", "2nd". */
  function ordinal(n) {
    if (I18N.getLang() === 'fr') return n + (n === 1 ? 'er' : 'e');
    var s = ['th', 'st', 'nd', 'rd'], v = n % 100;
    return n + (s[(v - 20) % 10] || s[v] || s[0]);
  }
  function renderTime(ms) {
    var t = computeTimeInsights(ms);
    var sum;
    if (!t.total) sum = tr('time.noMatch');
    else if (!t.enough) sum = tr('time.needMore', { v: plural(TIME_MIN_GAMES - t.total, 'match') });
    else {
      var parts = [];
      if (t.bestDay) parts.push(tr('time.onDay', { day: '<b>' + t.bestDay.name + '</b>' }));
      if (t.bestWindow) parts.push('<b>' + t.bestWindow.label + '</b>');
      sum = parts.length ? tr('time.winMost', { v: parts.join(tr('time.andComma')) }) : tr('time.noDiff');
      if (t.worstDay || t.worstWindow) {
        var bad = [];
        if (t.worstDay) bad.push(tr('time.onDay', { day: t.worstDay.name }));
        if (t.worstWindow) bad.push(t.worstWindow.label);
        sum += ' ' + tr('time.lessSo', { v: bad.join(tr('time.and')) });
      }
    }
    $('#time-summary').innerHTML = sum + (t.total ? ' <span class="muted">' + tr('time.average', { pct: fmtPct(t.wr), v: plural(t.total, 'match') }) + '</span>' : '');
    var few = t.enough ? tr('time.noDiffDay') : tr('time.notEnough');
    var fewH = t.enough ? tr('time.noDiffHour') : tr('time.notEnough');
    var slot = function (w) { return w ? tr('time.slot', { a: w.from, b: (w.from + w.len) % 24 }) : ''; };
    $('#time-tiles').innerHTML =
      timeTile('best', tr('time.bestDay'), t.bestDay ? cap(t.bestDay.name) : '', t.bestDay, few) +
      timeTile('worst', tr('time.worstDay'), t.worstDay ? cap(t.worstDay.name) : '', t.worstDay, few) +
      timeTile('best', tr('time.bestSlot'), slot(t.bestWindow), t.bestWindow, fewH) +
      timeTile('worst', tr('time.worstSlot'), slot(t.worstWindow), t.worstWindow, fewH);
    $('#m-busy').textContent = t.busiestDay && t.busiestDay.n ? tr('time.busiest', { day: t.busiestDay.name, v: plural(t.busiestDay.n, 'match') }) : '';

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
    var head = '<div class="hm-corner"></div>' + cols.map(function (h) { return '<div class="hm-h">' + h + '</div>'; }).join('');
    var rows = t.grid.map(function (row, d) {
      return '<div class="hm-d">' + tr('weekdays.short')[d] + '</div>' + cols.map(function (h) {
        var c = row[h];
        var tip = cap(weekdayName(d)) + ' ' + tr('time.range', { a: h, b: (h + 1) % 24 }) + ' · ' +
          (c.n ? tr('goal.wonPct', { pct: fmtPct(c.wr) }) + ' (' + wlShort(c.wins, c.n - c.wins) + ')' + (c.n < minCell ? ' · ' + tr('common.smallSample') : '') : tr('time.noMatchCell'));
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
    grid.innerHTML = t.total ? head + rows : '<p class="muted small">' + tr('goal.noData') + '.</p>';
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
        plugins: { tooltip: { displayColors: false, callbacks: { title: function () { return ''; }, label: function (it) { return tr('fmt.labelValue', { label: label, v: fmtV(it.raw) }); } } } }
      }
    });
  }

  function renderMechanics(ms) {
    var mc = computeMechanics(ms);
    var box = $('#mechanics');
    $('#mech-sub').textContent = mc.n ? tr('mech.sub', { v: plural(mc.n, 'match') }) : tr('mech.none');
    box.innerHTML = mc.metrics.map(function (m, i) {
      var v = m.unit === '%' ? fmtPct100(m.avg, m.digits) : fmtNum(m.avg, m.digits) + (m.unit ? '<span class="unit">' + m.unit + '</span>' : '');
      var trend = '';
      if (isNum(m.trend)) {
        var good = m.higherBetter == null ? null : (m.trend > 0) === m.higherBetter;
        var small = Math.abs(m.trend) < Math.pow(10, -m.digits) * 0.5;
        trend = small ? tr('mech.stable')
          : tr('mech.vsPrev', { v: '<span class="' + (good == null ? '' : good ? 'up' : 'down') + '">' + (m.trend >= 0 ? '▲ ' : '▼ ') + fmtSigned(m.trend, m.digits) + (m.unit === '%' ? ' pt' : m.unit ? ' ' + m.unit : '') + '</span>' });
      } else trend = m.n ? plural(m.n, 'match') : tr('mech.noData');
      return '<div class="card mech"><span class="kpi-label">' + esc(m.label) + '</span><span class="kpi-value">' + v + '</span>' +
        '<span class="trend">' + trend + '</span><div class="spark"><canvas id="sp-mech-' + i + '"></canvas></div></div>';
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
      { label: tr('mech.ground_pct'), v: s.ground, color: T.series[0] },
      { label: tr('mech.wall_pct'), v: s.wall, color: T.series[1] },
      { label: tr('mech.air_split'), v: s.air, color: T.series[2] }
    ];
    var fact = function (label, v) { return '<span>' + label + ' <b>' + v + '</b></span>'; };
    $('#split').innerHTML = tot > 0 ?
      '<div class="split-head"><span class="card-title">' + tr('mech.split') + '</span><span class="legend">' +
      segs.map(function (g) { return '<span><i style="background:' + g.color + '"></i>' + g.label + ' ' + fmtPct100(g.v, 0) + '</span>'; }).join('') + '</span></div>' +
      '<div class="split-bar">' + segs.map(function (g) { return '<span style="width:' + ((g.v || 0) / tot * 100).toFixed(2) + '%;background:' + g.color + '" title="' + esc(tr('fmt.labelValue', { label: g.label, v: fmtPct100(g.v, 1) })) + '"></span>'; }).join('') + '</div>' +
      '<div class="detail-facts">' + fact(tr('mech.boosting_pct'), fmtPct100(mc.boostingPct, 1)) + fact(tr('mech.full_boost_pct'), fmtPct100(mc.fullBoostPct, 1)) +
      fact(tr('mech.powerslide_pct'), fmtPct100(mc.powerslidePct, 1)) +
      '<span>' + tr('mech.demolished_s') + ' <b>' + fmtNum(mc.demolishedS, 1) + ' s</b> ' + tr('mech.perMatch') + '</span>' +
      fact(tr('mech.hardestHit'), fmtNum(mc.maxHitKmh) + ' km/h') + '</div>'
      : '<span class="muted small">' + tr('mech.noSplit') + '</span>';
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
          label: function (it) { var r = rows[it.dataIndex]; return tr('feed.total', { v: r.count, p: fmtNum(r.perMatch, 2) }); },
          footer: function (it) { var r = rows[it[0].dataIndex]; return r.label !== r.key ? tr('feed.event', { v: r.key }) : ''; }
        } } }
      }
    }, rows.length ? null : tr('feed.none'));
  }

  /* ---------- render: tables ---------- */
  function wrCell(wr) {
    return '<div class="wr-cell"><span class="num">' + fmtPct(wr) + '</span><span class="wr-bar"><span style="width:' + (isNum(wr) ? (wr * 100).toFixed(1) : 0) + '%"></span></span></div>';
  }
  function wlHead() { return tr('res.win.short') + '–' + tr('res.loss.short'); }
  function renderArenas(ms) {
    var all = computeArenas(ms);
    var rows = all.slice(0, 12);
    var restN = all.slice(12).reduce(function (t, a) { return t + a.n; }, 0);
    var others = all.length - rows.length;
    $('#t-arenas').innerHTML = '<thead><tr><th>' + tr('tbl.arena') + '</th><th class="r">' + tr('kpi.matches') + '</th><th class="r hide-mobile">' + wlHead() + '</th><th class="r">' + tr('kpi.winrate') + '</th><th class="r">' + tr('tbl.diff') + '</th></tr></thead><tbody>' +
      (rows.length ? rows.map(function (a) {
        return '<tr><td class="name">' + esc(a.name) + '</td><td class="r">' + a.n + '</td><td class="r hide-mobile">' + a.wins + '–' + a.losses + '</td><td class="r">' + wrCell(a.wr) + '</td><td class="r">' + fmtSigned(a.gdAvg, 1) + '</td></tr>';
      }).join('') + (restN ? '<tr><td class="muted" colspan="5">' + tr('tbl.otherArenas', { n: others, v: others, m: plural(restN, 'match') }) + '</td></tr>' : '')
        : '<tr class="empty-row"><td colspan="5">' + tr('tbl.noArena') + '</td></tr>') + '</tbody>';
  }
  function renderMates(ms) {
    var rows = computeTeammates(ms, 3).slice(0, 15);
    var overall = wl(ms).wr;
    $('#t-mates').innerHTML = '<thead><tr><th>' + tr('tbl.mate') + '</th><th class="r">' + tr('kpi.matches') + '</th><th class="r hide-mobile">' + wlHead() + '</th><th class="r">' + tr('kpi.winrate') + '</th><th class="r" title="' + esc(tr('tbl.vsAvgTip')) + '">' + tr('tbl.vsAvg') + '</th><th class="r hide-mobile">' + tr('tbl.goalsPer') + '</th></tr></thead><tbody>' +
      (rows.length ? rows.map(function (t) {
        var d = isNum(t.wr) && isNum(overall) ? (t.wr - overall) * 100 : null;
        return '<tr><td class="name">' + playerName(t.name, t.pid) + '</td><td class="r">' + t.n + '</td><td class="r hide-mobile">' + t.wins + '–' + t.losses + '</td><td class="r">' + wrCell(t.wr) + '</td>' +
          '<td class="r">' + (d == null ? '—' : fmtSigned(d) + ' pts') + '</td><td class="r hide-mobile">' + fmtNum(t.goalsPer, 2) + '</td></tr>';
      }).join('') : '<tr class="empty-row"><td colspan="6">' + tr('tbl.noMate') + '</td></tr>') + '</tbody>';
  }

  function tagOptions(selected) {
    var tags = TAGS.slice();
    if (selected && tags.indexOf(selected) < 0) tags.push(selected);
    return tags.map(function (t) { return '<option value="' + esc(t) + '"' + (t === selected ? ' selected' : '') + '>' + esc(tagLabel(t)) + '</option>'; }).join('');
  }
  var TRASH = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/></svg>';
  /** [badge letter, label] of a result. */
  function resText(result) {
    return result === 'win' || result === 'loss' || result === 'abandoned' ? [tr('res.' + result + '.short'), tr('res.' + result)] : ['?', result || tr('common.unknown')];
  }

  function renderMatches(ms) {
    var list = ms.slice().reverse();
    var shown = list.slice(0, state.shown);
    var cols = 9;
    var html = '<thead><tr><th style="width:28px"></th><th>' + tr('tbl.date') + '</th><th>' + tr('tbl.mode') + '</th><th class="c">' + tr('tbl.res') + '</th><th class="c">' + tr('perf.score') + '</th>' +
      '<th class="r hide-mobile" title="' + esc(tr('tbl.gasTip')) + '">' + tr('tbl.gas') + '</th><th class="r hide-mobile">' + tr('tbl.points') + '</th><th class="hide-mobile">' + tr('tbl.type') + '</th><th style="width:40px"></th></tr></thead><tbody>';
    if (!shown.length) html += '<tr class="empty-row"><td colspan="' + cols + '">' + tr('tbl.noMatch') + '</td></tr>';
    shown.forEach(function (m) {
      var r = resText(m.result);
      var mm = m.me || {};
      var open = !!state.open[m.id];
      var chips = [];
      if (m.overtime) chips.push('<span class="chip" title="' + esc(tr('goals.overtime')) + '">' + tr('goals.ot') + '</span>');
      if (m.forfeit) chips.push('<span class="chip" title="' + esc(tr('badge.forfeitTip')) + '">' + tr('badge.forfeit') + '</span>');
      if (m.mvp) chips.push('<span class="chip mvp">MVP</span>');
      if (m.online === false) chips.push('<span class="chip">' + tr('badge.offline') + '</span>');
      html += '<tr class="row' + (open ? ' open' : '') + '" data-id="' + m.id + '" tabindex="0" aria-expanded="' + open + '">' +
        '<td><span class="caret">›</span></td>' +
        '<td class="nowrap"><a class="row-link" href="' + href('match/' + m.id) + '" title="' + esc(tr('tbl.openMatch')) + '">' + esc(fmtDate(m._t, true)) + '</a></td>' +
        '<td><div>' + esc(m.mode || '—') + (m.variant && m.variant !== 'Soccar' ? ' ' + esc(m.variant) : '') + ' <span class="muted hide-mobile">· ' + esc(prettyArena(m.arena)) + '</span></div>' + (chips.length ? '<div class="chips">' + chips.join('') + '</div>' : '') + '</td>' +
        '<td class="c"><span class="res ' + esc(m.result) + '" title="' + esc(r[1]) + '">' + r[0] + '</span></td>' +
        '<td class="c"><span class="score">' + fmtNum(m.team_score) + '<span class="sep">–</span>' + fmtNum(m.opp_score) + '</span></td>' +
        '<td class="r hide-mobile num">' + fmtNum(mm.goals) + ' / ' + fmtNum(mm.assists) + ' / ' + fmtNum(mm.saves) + '</td>' +
        '<td class="r hide-mobile num">' + fmtNum(mm.score) + '</td>' +
        (readOnly() ? '<td class="hide-mobile muted">' + esc(tagLabel(m.tag)) + '</td><td></td></tr>'
          : '<td class="hide-mobile"><select class="select tag-select" data-tag-for="' + m.id + '" aria-label="' + esc(tr('tbl.matchType')) + '">' + tagOptions(m.tag) + '</select></td>' +
            '<td><button type="button" class="del-btn" data-del="' + m.id + '" title="' + esc(tr('tbl.delete')) + '" aria-label="' + esc(tr('tbl.delete')) + '">' + TRASH + '</button></td></tr>');
      if (open) html += '<tr class="detail"><td colspan="' + cols + '">' + matchDetail(m) + '</td></tr>';
    });
    html += '</tbody>';
    $('#t-matches').innerHTML = html;
    var more = $('#more-matches');
    more.hidden = list.length <= state.shown;
    more.textContent = tr('tbl.showMore', { v: list.length - Math.min(state.shown, list.length) });
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
    if (m.overtime && total > reg + 30) tl += '<span class="tick" style="left:' + ((reg + (total - reg) / 2) / total * 100).toFixed(2) + '%">' + tr('goals.ot') + '</span>';
    goals.forEach(function (g) {
      tl += '<span class="g ' + (g.team === 'us' ? 'us' : 'them') + (g.me_scored ? ' me' : '') + '" style="left:' + (Math.min(1, num(g.t) / total) * 100).toFixed(2) + '%" title="' + esc(goalTime(m, g) + ' · ' + (g.scorer || '?')) + '"></span>';
    });
    return tl + '</div>';
  }
  function goalItems(m, goals) {
    var us = 0, them = 0;
    return goals.map(function (g) {
      if (g.team === 'us') us++; else them++;
      return '<li><span class="t">' + goalTime(m, g) + '</span><span class="d" style="background:' + (g.team === 'us' ? 'var(--us)' : 'var(--them)') + '"></span>' +
        '<span class="who">' + esc(g.scorer || '?') + (g.me_scored ? ' <span class="chip">' + tr('common.you') + '</span>' : '') + (g.assister ? ' <small>· ' + tr('match.assist', { v: esc(g.assister) }) + '</small>' : '') +
        (isNum(g.speed) && g.speed > 0 ? ' <small>· ' + fmtNum(goalKmh(g.speed)) + ' km/h</small>' : '') + '</span>' +
        '<span class="sc">' + us + '–' + them + '</span></li>';
    }).join('');
  }
  function matchDetail(m) {
    var players = (m.players || []).slice();
    var ours = players.filter(function (p) { return p.team === m.my_team; }).sort(function (a, b) { return num(b.score) - num(a.score); });
    var theirs = players.filter(function (p) { return p.team !== m.my_team; }).sort(function (a, b) { return num(b.score) - num(a.score); });
    function prow(p) {
      return '<tr class="' + (p.is_me ? 'me' : '') + '"><td>' + playerName(p.name, p.primary_id) + '</td><td>' + fmtNum(p.score) + '</td><td>' + fmtNum(p.goals) + '</td><td>' + fmtNum(p.assists) + '</td><td>' + fmtNum(p.saves) + '</td><td class="hide-mobile">' + fmtNum(p.shots) + '</td><td class="hide-mobile">' + fmtNum(p.demos) + '</td></tr>';
    }
    var sb = '<table class="scoreboard"><thead><tr><th>' + tr('match.player') + '</th><th>' + tr('match.pts') + '</th><th title="' + esc(tr('perf.goals')) + '">' + tr('match.g') + '</th><th title="' + esc(tr('perf.assists')) + '">' + tr('match.a') + '</th>' +
      '<th title="' + esc(tr('perf.saves')) + '">' + tr('match.s') + '</th><th class="hide-mobile">' + tr('perf.shots') + '</th><th class="hide-mobile">' + tr('match.demo') + '</th></tr></thead><tbody>' +
      '<tr class="team-row"><td colspan="7"><span class="team-dot" style="background:var(--us)"></span>' + tr('match.yourTeam') + ' · ' + fmtNum(m.team_score) + '</td></tr>' + ours.map(prow).join('') +
      '<tr class="team-row"><td colspan="7"><span class="team-dot" style="background:var(--them)"></span>' + tr('match.opponents') + ' · ' + fmtNum(m.opp_score) + '</td></tr>' + theirs.map(prow).join('') +
      '</tbody></table>';

    var goals = sortedGoals(m);
    var tl = goalTimeline(m, goals);
    var gl = goals.length ? '<ul class="goal-list">' + goalItems(m, goals) + '</ul>' : '<p class="muted small">' + tr(goalsComplete(m) ? 'match.noGoal' : 'match.noGoalRecorded') + '</p>';
    if (!goalsComplete(m)) gl += '<p class="muted small">' + tr('match.partialTimeline', { v: plural(goals.length, 'goal'), total: num(m.team_score) + num(m.opp_score) }) + '</p>';

    var facts = [];
    var fact = function (k, v) { facts.push('<span>' + tr(k) + ' <b>' + v + '</b></span>'); };
    fact('match.duration', fmtClock(m.duration_s));
    fact('tbl.arena', esc(prettyArena(m.arena)));
    if (m.overtime) fact('goals.overtime', fmtClock(m.overtime_s));
    if (m.movement) {
      fact('match.avgSpeed', fmtNum(kmh(m.movement.avg_speed)) + ' km/h');
      fact('mech.supersonic_pct', fmtPct100(m.movement.supersonic_pct, 1));
      fact('mech.air_split', fmtPct100(m.movement.air_pct, 1));
      fact('match.avgBoost', fmtNum(m.movement.avg_boost));
    }
    if (m.hits) facts.push('<span>' + tr('match.hits', { n: '<b>' + fmtNum(m.hits.count) + '</b>', avg: '<b>' + fmtNum(kmh(m.hits.avg_speed)) + '</b>', max: '<b>' + fmtNum(kmh(m.hits.max_speed)) + ' km/h</b>' }) + '</span>');
    var sf = m.statfeed || {};
    var feed = Object.keys(sf).filter(function (k) { return k !== 'Win'; }).map(function (k) { return '<span class="chip">' + esc(statfeedLabel(k)) + (sf[k] > 1 ? ' ×' + sf[k] : '') + '</span>'; }).join('');

    return '<div class="detail-grid"><div><h4>' + tr('match.scoreboard') + '</h4>' + sb + '</div>' +
      '<div><h4>' + tr('match.timeline') + '</h4>' + tl + gl + '</div></div>' +
      '<div class="detail-facts">' + facts.join('') + '</div>' +
      (feed ? '<h4>' + tr('match.feed') + '</h4><div class="chips">' + feed + '</div>' : '') +
      '<div class="detail-actions"><a class="btn btn-ghost" href="' + href('match/' + m.id) + '">' + tr('match.open') + ' <span aria-hidden="true">→</span></a></div>' +
      (readOnly() ? '' : '<div class="detail-actions only-mobile"><label class="small muted">' + tr('tbl.type') + '&nbsp;<select class="select tag-select" data-tag-for="' + m.id + '" aria-label="' + esc(tr('tbl.matchType')) + '">' + tagOptions(m.tag) + '</select></label></div>');
  }

  /* ---------- routing ---------- */
  function syncNav() {
    var r = state.route;
    var cur = r.view === 'players' || r.player ? 'players' : r.view === 'dash' ? 'dash' : 'history';
    $$('[data-nav]').forEach(function (a) {
      if (a.getAttribute('data-nav') === cur) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    var who = r.player ? ((viewedPlayer() || {}).name || r.player) + ' · ' : '';
    if (r.view === 'dash') document.title = who + 'Rocket Tracker';
    else if (r.view === 'history') document.title = who + tr('nav.history') + ' · Rocket Tracker';
    else if (r.view === 'players') document.title = tr('nav.players') + ' · Rocket Tracker';
    renderPlayerBanner();
  }
  /** Your own pages, when a route names you as "another player". */
  function selfRoute(r) {
    if (!r.player || !state.me || r.player !== state.me.handle) return null;
    return routeHash(null, r.view === 'history' ? 'history' : r.view === 'match' && r.id != null ? 'match/' + r.id : '');
  }
  function onRoute() {
    var r = parseRoute(location.hash);
    var self = selfRoute(r);
    if (self) { location.replace(self); return; }
    var prev = state.route;
    if (r.view === prev.view && r.id === prev.id && r.player === prev.player) return;
    if (prev.view === 'history') state.histScroll = window.scrollY || window.pageYOffset || 0;
    state.route = r;
    if (r.view !== 'players' && (r.player || '') !== state.dataFor) loadAll();
    if (r.view === 'players') loadPlayers();
    renderAll();
    window.scrollTo(0, r.view === 'history' && prev.view === 'match' && r.player === prev.player ? state.histScroll : 0);
    var h = r.view === 'history' ? $('#h-history') : r.view === 'match' ? $('#mp-title') : r.view === 'players' ? $('#h-players') : null;
    if (h) h.focus({ preventScroll: true });
  }

  /* ---------- shared match bits ---------- */
  function partialTip() { return tr('badge.partialTip'); }
  function resBadge(m) {
    var r = m.result === 'abandoned' ? [tr('res.abandon.badge'), tr('res.abandoned')] : resText(m.result);
    if (!m.result) r = ['?', tr('res.unknown')];
    return '<span class="res ' + esc(m.result || '') + '" aria-hidden="true">' + r[0] + '</span><span class="sr-only">' + r[1] + '</span>';
  }
  function longDay(t) {
    var s = fmtDay(t, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
    return s.charAt(0).toUpperCase() + s.slice(1);
  }
  function fmtTime(t) { return new Date(t).toLocaleTimeString(L(), { hour: '2-digit', minute: '2-digit' }); }
  function modeLabel(m) { return (m.mode || '—') + (m.variant && m.variant !== 'Soccar' ? ' ' + m.variant : ''); }
  function signCls(x) { return isNum(x) && x > 0 ? 'pos' : isNum(x) && x < 0 ? 'neg' : ''; }
  function matchBadges(m, long) {
    var b = [];
    if (m.mvp) b.push('<span class="chip mvp" title="' + esc(tr('badge.mvpTip')) + '">MVP</span>');
    if (m.overtime) b.push('<span class="chip" title="' + esc(tr('goals.overtime') + (isNum(m.overtime_s) && m.overtime_s > 0 ? ' : ' + fmtClock(m.overtime_s) : '')) + '">' + (long ? tr('goals.overtime') : tr('goals.ot')) + '</span>');
    if (m.forfeit) {
      b.push(m.result === 'win' ? '<span class="chip" title="' + esc(tr('badge.oppForfeitTip')) + '">' + tr('badge.oppForfeit') + '</span>'
        : '<span class="chip" title="' + esc(tr('badge.ownForfeitTip')) + '">' + tr('badge.forfeit') + '</span>');
    }
    if (m.partial) b.push('<span class="chip warn" title="' + esc(partialTip()) + '">' + tr('badge.partial') + '</span>');
    if (m.online === false) b.push('<span class="chip" title="' + esc(tr('badge.offlineTip')) + '">' + tr('badge.offline') + '</span>');
    return b.join('');
  }
  function tagFilterOptions() {
    var tags = TAGS.slice();
    state.all.forEach(function (m) { if (m.tag && tags.indexOf(m.tag) < 0) tags.push(m.tag); });
    return '<option value="all">' + tr('common.all') + '</option>' + tags.map(function (t) { return '<option value="' + esc(t) + '">' + esc(tagLabel(t)) + '</option>'; }).join('');
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
    return '<li><a class="hrow" href="' + href('match/' + m.id) + '">' +
      '<span class="h-time num">' + esc(fmtTime(m._t)) + '</span>' +
      '<span class="h-mode">' + esc(modeLabel(m)) + '</span>' +
      '<span class="h-res">' + resBadge(m) + '</span>' +
      '<span class="h-score"><span class="score">' + fmtNum(m.team_score) + '<span class="sep">–</span>' + fmtNum(m.opp_score) + '</span>' +
        (diff != null ? ' <span class="h-diff ' + signCls(diff) + '">' + fmtSigned(diff) + '</span>' : '') + '</span>' +
      '<span class="h-arena"><span class="h-arena-name">' + esc(prettyArena(m.arena)) + '</span>' + (badges ? '<span class="chips">' + badges + '</span>' : '') + '</span>' +
      '<span class="h-stats num" title="' + esc(tr('hist.statsTip')) + '">' + fmtNum(mm.goals) + ' / ' + fmtNum(mm.assists) + ' / ' + fmtNum(mm.saves) + ' / ' + fmtNum(mm.shots) + '</span>' +
      '<span class="h-pts num">' + fmtNum(mm.score) + '<small> pts</small></span>' +
      '<span class="h-dur num" title="' + esc(tr('match.duration')) + '">' + fmtClock(m.duration_s) + '</span>' +
      '<span class="h-go" aria-hidden="true">›</span></a></li>';
  }
  function manualRow(e) {
    return '<li class="hist-manual"><span class="hm-icon" aria-hidden="true">✎</span><span>' + tr('hist.manual') + DOT_SEP + plural(e.games, 'game') + ' ' + esc(e.mode) + DOT_SEP +
      wlShort(e.wins, e.losses) + '</span></li>';
  }
  function histDay(g) {
    var head = longDay(g.t);
    var tot = [plural(g.games, 'match'), wlShort(g.wins, g.losses)];
    if (g.abandoned) tot.push(plural(g.abandoned, 'abandon'));
    if (g.decided) tot.push(tr('hist.diff', { v: '<b class="' + signCls(g.diff) + '">' + fmtSigned(g.diff) + '</b>' }));
    var note = g.manualGames ? ' <span class="chip manual" title="' + esc(tr('hist.manualTip', { v: plural(g.manualGames, 'game'), n: g.manualGames })) + '">✎ ' + tr('hist.manualChip', { n: g.manualGames }) + '</span>' : '';
    var rows = g.shownMatches.map(histRow).join('') + (g.truncated ? '<li class="hist-trunc">' + tr('hist.trunc') + '</li>' : g.manual.map(manualRow).join(''));
    return '<section class="card hist-day" aria-label="' + esc(head) + '"><header class="hist-day-head"><h2>' + esc(head) + '</h2>' +
      '<p class="hist-day-tot">' + tot.join(DOT_SEP) + note + '</p></header><ul class="hist-rows">' + rows + '</ul></section>';
  }
  function renderHistory() {
    syncHistUi();
    var f = state.hist, box = $('#hist-list'), more = $('#hist-more'), sum = $('#hist-summary');
    if (!state.loaded) { sum.textContent = ''; more.hidden = true; box.innerHTML = '<div class="hist-empty muted">' + tr('common.loading') + '</div>'; return; }
    var ms = filterHistory(state.all, f);
    var man = filterManualHistory(state.manual, f);
    var groups = groupHistory(ms, man);
    var page = paginateGroups(groups, state.histShown);
    var s = summarizeHistory(ms, man);
    var parts = [];
    if (s.n) {
      parts.push('<b>' + fmtNum(s.n) + '</b> ' + tr('unit.match', { n: s.n }));
      parts.push(tr('wl.short', { w: '<b>' + s.wins + '</b>', l: '<b>' + s.losses + '</b>' }) + (s.abandoned ? ' <span class="muted">(+ ' + plural(s.abandoned, 'abandon') + ')</span>' : ''));
      parts.push(tr('hist.wr', { v: '<b>' + fmtPct(s.wr) + '</b>' }));
      parts.push(tr('hist.diffAvg', { v: '<b class="' + signCls(s.diffAvg) + '">' + fmtSigned(s.diffAvg, 2) + '</b>' }));
    } else parts.push(tr('hist.none'));
    if (s.manualGames) parts.push('<span class="muted">' + tr('hist.plusManual', { v: plural(s.manualGames, 'game'), n: s.manualGames }) + '</span>');
    sum.innerHTML = parts.join(DOT_SEP);
    if (!groups.length) {
      more.hidden = true;
      box.innerHTML = !state.all.length && !state.manual.length
        ? '<div class="no-results"><p><strong>' + tr('hist.emptyTitle') + '</strong></p><p class="muted">' + tr('hist.emptyText') + '</p></div>'
        : '<div class="no-results"><p><strong>' + tr('hist.noMatchTitle') + '</strong></p><p class="muted">' + tr('hist.noMatchText') + '</p>' +
          '<p><button type="button" class="btn" data-hist-reset>' + tr('hist.reset') + '</button></p></div>';
      return;
    }
    box.innerHTML = '<div class="hist-cols" aria-hidden="true"><span>' + tr('hist.time') + '</span><span>' + tr('tbl.mode') + '</span><span>' + tr('tbl.res') + '</span><span>' + tr('perf.score') + '</span><span>' + tr('tbl.arena') + '</span>' +
      '<span class="r">' + tr('hist.statsCol') + '</span><span class="r">' + tr('tbl.points') + '</span><span class="r">' + tr('match.duration') + '</span><span></span></div>' + page.groups.map(histDay).join('');
    more.hidden = page.remaining <= 0;
    more.textContent = tr('hist.more', { v: plural(page.remaining, 'match'), n: page.remaining });
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
    var tip = fmtDate(t._t, true) + ' · ' + modeLabel(t) + ' · ' + num(t.team_score) + '–' + num(t.opp_score) + ' (' + tr('mp.key', { v: key }) + ')';
    return '<a class="btn btn-ghost" href="' + href('match/' + t.id) + '" rel="' + rel + '" title="' + esc(tip) + '">' + label + '</a>';
  }
  function mpNav(nb) {
    return '<nav class="mp-nav" aria-label="' + esc(tr('mp.navAria')) + '">' +
      '<a class="btn btn-ghost" href="' + href('history') + '"><span aria-hidden="true">←</span> ' + tr('nav.history') + '</a>' +
      '<div class="mp-pn">' + navLink(nb.prev, '<span aria-hidden="true">‹</span> ' + tr('mp.prev'), 'prev', '←') +
      '<span class="mp-pos num" title="' + esc(tr('mp.posTip')) + '">' + fmtNum(nb.index + 1) + ' / ' + fmtNum(nb.total) + '</span>' +
      navLink(nb.next, tr('mp.next') + ' <span aria-hidden="true">›</span>', 'next', '→') + '</div></nav>';
  }
  function mpHero(m, ctx) {
    var cls = m.result === 'win' || m.result === 'loss' || m.result === 'abandoned' ? m.result : '';
    var resTxt = m.result === 'win' ? tr(m.forfeit ? 'mp.winForfeit' : 'res.win') : m.result === 'loss' ? tr(m.forfeit ? 'mp.lossForfeit' : 'res.loss')
      : m.result === 'abandoned' ? tr('mp.abandoned') : tr('res.unknown');
    var dur = tr('match.duration') + ' <b>' + fmtClock(m.duration_s) + '</b>' + (m.overtime ? ' ' + tr('mp.otPart', { v: '<b>' + fmtClock(m.overtime_s) + '</b>' }) : '');
    var facts = [esc(m.mode || '—'), esc(m.variant || 'Soccar'), esc(prettyArena(m.arena)), m.online === false ? tr('badge.offline') : tr('mp.online'), dur];
    var ts = fmtNum(m.team_score), os = fmtNum(m.opp_score);
    var badges = matchBadges(m, true);
    var ctxParts = [];
    if (ctx) {
      ctxParts.push(tr('mp.sessIdx', { i: '<b>' + tr('mental.bestNo', { v: ctx.sessIdx }) + '</b>', v: plural(ctx.sessN, 'match') }));
      var p = ctx.prevInSession;
      if (p) {
        var pr = resText(p.result)[1];
        ctxParts.push(tr('mp.prevMatch', { v: '<a href="' + href('match/' + p.id) + '">' + esc(pr) + ' ' + fmtNum(p.team_score) + '–' + fmtNum(p.opp_score) + '</a> <span class="muted">(' + esc(modeLabel(p)) + ')</span>' }));
      } else ctxParts.push(tr('mp.firstOfSession'));
      if (ctx.streak) {
        var w = ctx.streak.type === 'win', n = ctx.streak.len;
        ctxParts.push(tr(m.result === 'win' || m.result === 'loss' ? 'mp.streakAfter' : 'mp.streakAt', {
          v: '<b class="' + (w ? 'pos' : 'neg') + '">' + n + ' ' + tr(w ? 'kpi.streakWins' : 'kpi.streakLosses', { n: n }) + '</b>' }));
      }
    }
    return '<section class="card mp-hero" aria-labelledby="mp-title">' +
      '<div class="mp-meta"><h1 id="mp-title" tabindex="-1">' + esc(longDay(m._t)) + ' · ' + esc(fmtTime(m._t)) + '</h1>' +
        '<p class="mp-facts">' + facts.join(DOT_SEP) + '</p></div>' +
      '<div class="mp-center">' +
        '<div class="mp-score" role="img" aria-label="' + esc(tr('mp.scoreAria', { us: ts, them: os })) + '">' +
          '<div class="mp-team us"><span class="mp-n">' + ts + '</span><span class="mp-tl">' + tr('match.yourTeam') + '</span></div>' +
          '<span class="mp-sep" aria-hidden="true">–</span>' +
          '<div class="mp-team them"><span class="mp-n">' + os + '</span><span class="mp-tl">' + tr('match.opponents') + '</span></div></div>' +
        '<div class="mp-res ' + cls + '">' + resTxt + '</div>' +
        (badges ? '<div class="chips">' + badges + '</div>' : '') +
      '</div>' +
      (readOnly() ? '<div class="mp-side"><span class="mp-tag"><span>' + tr('tbl.type') + '</span><b>' + esc(tagLabel(m.tag)) + '</b></span></div>'
        : '<div class="mp-side"><label class="mp-tag"><span>' + tr('tbl.type') + '</span><select class="select tag-select" data-tag-for="' + m.id + '" aria-label="' + esc(tr('tbl.matchType')) + '">' + tagOptions(m.tag) + '</select></label>' +
          '<button type="button" class="btn btn-ghost btn-danger" data-mp-del="' + m.id + '">' + TRASH + tr('mp.delete') + '</button></div>') +
      (m.partial ? '<p class="mp-note">' + esc(partialTip()) + '.</p>' : '') +
      (ctxParts.length ? '<div class="mp-context">' + ctxParts.map(function (x) { return '<span>' + x + '</span>'; }).join('') + '</div>' : '') +
      '</section>';
  }

  var SB_COLS = ['score', 'goals', 'assists', 'saves', 'shots', 'touches', 'demos'];
  function mpScoreboard(m) {
    var players = (m.players || []).filter(function (p) { return p && typeof p === 'object'; });
    if (!players.length && m.me && m.me.name) players = [Object.assign({ team: m.my_team, is_me: true }, m.me)];
    function team(us) {
      var list = players.filter(function (p) { return us ? p.team === m.my_team : p.team !== m.my_team; })
        .sort(function (a, b) { return num(b.score) - num(a.score); });
      var html = '<tbody><tr class="team-row"><th colspan="' + (SB_COLS.length + 1) + '" scope="colgroup"><span class="team-dot" style="background:var(--' + (us ? 'us' : 'them') + ')"></span>' +
        tr(us ? 'match.yourTeam' : 'match.opponents') + ' · ' + fmtNum(us ? m.team_score : m.opp_score) + '</th></tr>';
      if (!list.length) return html + '<tr><td class="muted" colspan="' + (SB_COLS.length + 1) + '">' + tr('mp.unknownPlayers') + '</td></tr></tbody>';
      html += list.map(function (p) {
        return '<tr' + (p.is_me ? ' class="me"' : '') + '><th scope="row">' + playerName(p.name, p.primary_id) + (p.is_me ? ' <span class="chip you">' + tr('common.you') + '</span>' : '') + '</th>' +
          SB_COLS.map(function (c) { return '<td>' + fmtNum(p[c]) + '</td>'; }).join('') + '</tr>';
      }).join('');
      html += '<tr class="tot"><th scope="row">' + tr('mp.total') + '</th>' + SB_COLS.map(function (c) {
        var vals = list.map(function (p) { return p[c]; }).filter(isNum);
        return '<td>' + (vals.length ? fmtNum(vals.reduce(function (a, b) { return a + b; }, 0)) : '—') + '</td>';
      }).join('') + '</tr></tbody>';
      return html;
    }
    return '<section class="card mp-card mp-span" aria-labelledby="mp-h-sb"><div class="mp-head"><h2 id="mp-h-sb">' + tr('match.scoreboard') + '</h2>' +
      '<span class="card-meta">' + tr('mp.sbHint') + '</span></div>' +
      '<div class="table-scroll"><table class="mp-sb"><thead><tr><th scope="col">' + tr('match.player') + '</th>' + SB_COLS.map(function (c) { return '<th scope="col">' + tr('perf.' + c) + '</th>'; }).join('') + '</tr></thead>' +
      team(true) + team(false) + '</table></div></section>';
  }

  function mpGoals(m) {
    var goals = sortedGoals(m);
    var complete = goalsComplete(m);
    var expected = num(m.team_score) + num(m.opp_score);
    var us = 0, them = 0;
    var you = ' <span class="chip you">' + tr('common.you') + '</span>';
    var list = goals.length ? '<ol class="goal-list mp-goal-list">' + goals.map(function (g) {
      var ours = g.team === 'us';
      if (ours) us++; else them++;
      var spd = isNum(g.speed) && g.speed > 0 ? fmtNum(goalKmh(g.speed)) + ' km/h' : '';
      return '<li><span class="t num">' + goalTime(m, g) + '</span><span class="d" style="background:var(--' + (ours ? 'us' : 'them') + ')"></span>' +
        '<span class="sr-only">' + tr(ours ? 'match.yourTeam' : 'match.opponents') + '</span>' +
        '<span class="who"><b>' + esc(g.scorer || '?') + '</b>' + (g.me_scored ? you : '') +
        (g.assister ? ' <small>· ' + tr('match.assist', { v: esc(g.assister) }) + (g.me_assist ? you : '') + '</small>' : '') + '</span>' +
        '<span class="spd num">' + spd + '</span>' +
        (complete ? '<span class="sc num" title="' + esc(tr('mp.scoreAfter')) + '">' + us + '–' + them + '</span>' : '') + '</li>';
    }).join('') + '</ol>' : '<p class="muted small">' + tr(complete ? 'match.noGoal' : 'match.noGoalRecorded') + '</p>';
    var note = complete ? '' : '<p class="mp-note">' + tr('mp.partialGoals', { v: plural(goals.length, 'goal'), n: goals.length, total: expected }) + '</p>';
    return '<section class="card mp-card" aria-labelledby="mp-h-goals"><div class="mp-head"><h2 id="mp-h-goals">' + tr('perf.goals') + '</h2>' +
      '<span class="legend"><span><i style="background:var(--us)"></i>' + tr('match.yourTeam') + '</span><span><i style="background:var(--them)"></i>' + tr('match.opponents') + '</span><span><i class="ring"></i>' + tr('common.you') + '</span></span></div>' +
      goalTimeline(m, goals) + list + note + '</section>';
  }

  function cmpUnit(s) { return s.unit === '%' ? (I18N.getLang() === 'fr' ? ' %' : '%') : s.unit ? ' ' + s.unit : ''; }
  /** One "this match vs average" row: value, average, signed delta (arrow + sign, colored when good/bad), bar + average tick. */
  function cmpRow(s, v, a) {
    var ad = s.avgDigits != null ? s.avgDigits : s.digits;
    var fv = function (x, d) { return isNum(x) ? fmtNum(x, d) + cmpUnit(s) : '—'; };
    var d = isNum(v) && isNum(a) ? v - a : null;
    var flat = d != null && Math.abs(d) < 0.5 * Math.pow(10, -ad);
    var good = d == null || flat || s.higherBetter == null ? null : (d > 0) === s.higherBetter;
    var cls = d == null ? '' : flat ? 'flat' : good == null ? 'neutral' : good ? 'up' : 'down';
    var txt = d == null ? '—' : flat ? tr('mp.eqAvg') : (d > 0 ? '↑ ' : '↓ ') + fmtSigned(d, ad) + (s.unit === '%' ? ' pt' : cmpUnit(s));
    var max = Math.max(isNum(v) ? v : 0, isNum(a) ? a : 0) * 1.15;
    var bar = '';
    if (max > 0) {
      bar = '<span class="cmp" aria-hidden="true" title="' + esc(tr('mp.cmpTip', { v: fv(v, s.digits), a: fv(a, ad) })) + '">' +
        (isNum(v) ? '<span class="cmp-fill" style="width:' + (v / max * 100).toFixed(1) + '%"></span>' : '') +
        (isNum(a) ? '<span class="cmp-avg" style="left:' + (a / max * 100).toFixed(1) + '%"></span>' : '') + '</span>';
    }
    return '<tr><th scope="row">' + esc(statLabel(s)) + '</th><td class="v">' + fv(v, s.digits) + '</td><td class="a">' + fv(a, ad) + '</td>' +
      '<td class="dl ' + cls + '">' + txt + '</td><td class="b">' + bar + '</td></tr>';
  }
  function cmpTable(rows) {
    return '<div class="table-scroll"><table class="cmp-table"><thead><tr><th scope="col"><span class="sr-only">' + tr('mp.stat') + '</span></th><th scope="col">' + tr('mp.thisMatch') + '</th><th scope="col">' + tr('mp.average') + '</th>' +
      '<th scope="col">' + tr('mp.gap') + '</th><th scope="col" class="b"><span class="legend"><span><i style="background:var(--us)"></i>' + tr('mp.thisMatchLc') + '</span><span><i class="tick"></i>' + tr('mp.averageLc') + '</span></span></th></tr></thead><tbody>' +
      rows.join('') + '</tbody></table></div>';
  }
  function mpPerf(m, base) {
    var head = '<div class="mp-head"><h2 id="mp-h-perf">' + tr('mp.perfTitle') + '</h2>';
    var open = '<section class="card mp-card" aria-labelledby="mp-h-perf">';
    if (!base || base.n < 3) {
      return open + head + '</div><p class="muted small">' + tr('mp.perfNotEnough', { mode: esc(modeLabel(m)), v: base ? base.n : 0 }) + '</p></section>';
    }
    var perf = PERF_STATS.map(function (s) {
      return cmpRow(Object.assign({ avgDigits: s.key === 'score' ? 0 : 1 }, s), statValue(m, 'me', s), base.me[s.key].avg);
    });
    return open + head + '<span class="card-meta">' + tr('mp.perfSub', { v: base.n, mode: esc(modeLabel(m)) }) + '</span></div>' +
      cmpTable(perf) + '</section>';
  }
  function splitNames() { return [tr('mech.ground_pct'), tr('mech.wall_pct'), tr('mech.air_split')]; }
  function splitRow(label, g, w, a) {
    var vals = [g, w, a], tot = vals.reduce(function (s, x) { return s + (isNum(x) ? x : 0); }, 0);
    if (!(tot > 0)) return '';
    var names = splitNames();
    return '<div class="mp-split-row"><span class="lbl">' + label + '</span><div class="split-bar">' + vals.map(function (v, i) {
      return '<span style="width:' + ((isNum(v) ? v : 0) / tot * 100).toFixed(2) + '%;background:' + T.series[i] + '" title="' + esc(tr('fmt.labelValue', { label: names[i], v: fmtPct100(v, 1) })) + '"></span>';
    }).join('') + '</div><span class="vals num">' + pct(vals.map(function (v) { return fmtNum(v, 0); }).join(' / ')) + '</span></div>';
  }
  function mpMovement(m, base) {
    var mv = m.movement, h = m.hits;
    if (!mv && !h) return '';
    var bm = base ? base.movement : {}, bh = base ? base.hits : {};
    function avgOf(o, k) { return o && o[k] && o[k].n >= 3 ? o[k].avg : null; }
    var nMv = bm && bm.avg_speed ? bm.avg_speed.n : 0, nH = bh && bh.avg_speed ? bh.avg_speed.n : 0;
    var nRef = Math.max(nMv, nH);
    var sub = nRef >= 3 ? tr('mp.mvSub', { v: nRef, mode: esc(modeLabel(m)) }) : tr('mp.mvNotEnough');
    var html = '<section class="card mp-card" aria-labelledby="mp-h-mv"><div class="mp-head"><h2 id="mp-h-mv">' + tr('mp.mvTitle') + '</h2><span class="card-meta">' + sub + '</span></div>';
    if (mv) {
      var rowThis = splitRow(tr('mp.thisMatch'), mv.ground_pct, mv.wall_pct, mv.air_pct);
      var rowAvg = avgOf(bm, 'ground_pct') != null ? splitRow(tr('mp.average'), bm.ground_pct.avg, bm.wall_pct.avg, bm.air_pct.avg) : '';
      if (rowThis) {
        html += '<div class="mp-split"><div class="mp-split-head"><span class="card-title">' + tr('mech.split') + '</span><span class="legend">' +
          splitNames().map(function (l, i) { return '<span><i style="background:' + T.series[i] + '"></i>' + l + '</span>'; }).join('') + '</span></div>' +
          rowThis + rowAvg + '</div>';
      }
    }
    var rows = [];
    if (mv) MOVE_STATS.forEach(function (s) { if (!s.split) rows.push(cmpRow(s, statValue(m, 'movement', s), avgOf(bm, s.key))); });
    if (h) {
      if (rows.length) rows.push('<tr class="sub"><th colspan="5" scope="rowgroup">' + tr('mp.ballHits') + '</th></tr>');
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
    return '<section class="card mp-card" aria-labelledby="mp-h-feed"><div class="mp-head"><h2 id="mp-h-feed">' + tr('match.feed') + '</h2><span class="card-meta">' + tr('mp.feedSub') + '</span></div>' +
      '<ul class="mp-feed">' + rows.map(function (r) {
        return '<li' + (r.label !== r.key ? ' title="' + esc(tr('feed.event', { v: r.key })) + '"' : '') + '><span>' + esc(r.label) + '</span><b class="num">×' + r.n + '</b></li>';
      }).join('') + '</ul></section>';
  }
  function renderMatchPage() {
    var box = $('#view-match');
    var id = state.route.id;
    if (!state.loaded) { box.innerHTML = '<div class="card mp-missing"><p class="muted">' + tr('mp.loading') + '</p></div>'; return; }
    var m = id != null ? findMatch(id) : null;
    if (!m) {
      document.title = tr('mp.notFound') + ' · Rocket Tracker';
      box.innerHTML = '<div class="card mp-missing"><h1 id="mp-title" tabindex="-1">' + tr('mp.notFound') + '</h1>' +
        '<p class="muted">' + tr('mp.notFoundText') + (id != null ? ' (' + tr('mental.bestNo', { v: id }) + ')' : '') + '.</p>' +
        '<p><a class="btn" href="' + href('history') + '"><span aria-hidden="true">←</span> ' + tr('mp.backHistory') + '</a></p></div>';
      return;
    }
    document.title = tr('mp.docTitle', { v: fmtDate(m._t, true) }) + ' · Rocket Tracker';
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
      if (d) deleteMatch(+d.getAttribute('data-mp-del'), function () { location.hash = '#/history'; });
    });
    document.addEventListener('keydown', function (e) {
      if (state.route.view !== 'match' || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      var t = e.target;
      if (t && t.closest && t.closest('input, select, textarea, [contenteditable], .table-scroll')) return;
      if (document.querySelector('dialog[open]')) return;
      var nb = matchNeighbors(state.all, state.route.id);
      var to = e.key === 'ArrowLeft' ? nb.prev : nb.next;
      if (to) { e.preventDefault(); location.hash = href('match/' + to.id); }
    });
  }

  /* ---------- live banner & warnings ---------- */
  function renderStatus() {
    var s = state.status;
    var pill = $('#conn-pill');
    var label = pill.querySelector('.pill-label');
    var w = [];
    pill.title = tr('st.pillTip');
    if (state.statusError) {
      pill.className = 'pill pill-warn'; label.textContent = tr('st.unreachable');
      w.push(warning('!', tr('st.downTitle'), tr(isServer() ? 'st.downServer' : 'st.downLocal')));
    } else if (s && isServer() && state.dataFor) {
      // Another player: only their live state.
      var who = (viewedPlayer() || {}).name || state.dataFor, ag0 = s.agent || {};
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = tr('st.playerPlaying', { name: who }); }
      else if (ag0.online) { pill.className = 'pill pill-ok'; label.textContent = tr('st.playerOnline', { name: who }); }
      else { pill.className = 'pill pill-muted'; label.textContent = tr('st.playerOffline', { name: who }); }
      pill.title = tr('st.playerAgentTip', { name: who });
    } else if (s && isServer()) {
      var ag = s.agent || {};
      pill.title = ag.device ? tr('st.agentTip', { v: ag.device }) + (ag.last_seen ? ' · ' + tr('st.lastSeen', { v: fmtDate(Date.parse(ag.last_seen), true) }) : '') : tr('st.noAgent');
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = tr('st.inMatch'); }
      else if (ag.online && s.connected) { pill.className = 'pill pill-ok'; label.textContent = tr('st.connected'); }
      else if (ag.online) { pill.className = 'pill pill-muted'; label.textContent = tr('st.noGame'); }
      else { pill.className = 'pill pill-muted'; label.textContent = tr('st.agentOffline'); }
      var ini2 = ag.online ? s.ini : null;
      var dev = esc(ag.device || tr('st.yourPc'));
      if (!ag.devices) {
        w.push(warning('i', tr('st.noDeviceTitle'), tr('st.noDeviceText'), true));
      } else if (!ag.last_seen) {
        w.push(warning('i', tr('st.neverSeenTitle'), tr('st.neverSeenText'), true));
      } else if (ag.online && !s.connected && ini2 && ini2.found && !ini2.ok) {
        w.push(warning('!', tr('st.agentApiOff', { dev: dev }), tr('st.agentApiOffText', { v: esc(ini2.packet_send_rate) })));
      } else if (ag.online && !s.connected && ini2 && !ini2.found) {
        w.push(warning('!', tr('st.agentApiMissing', { dev: dev }), tr('st.agentApiMissingText')));
      }
      if (ag.devices && versionBelow(ag.version, s.version)) {
        w.push(warning('i', tr('st.agentOldTitle', { dev: ag.device || tr('st.yourPc'), a: stripV(ag.version), s: stripV(s.version) }), tr('st.agentOldText', { url: RELEASES_URL }), true));
      }
    } else if (s) {
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = tr('st.inMatch'); }
      else if (s.connected) { pill.className = 'pill pill-ok'; label.textContent = tr('st.connected'); pill.title = tr('st.transport', { v: s.transport === 'tcp' ? 'TCP' : s.transport === 'ws' ? 'WebSocket' : '—' }); }
      else { pill.className = 'pill pill-muted'; label.textContent = tr('st.noGame'); }
      var ini = s.ini || null;
      // Connected = the Stats API evidently works (the ini may sit in an undetected install dir): no setup warning.
      if (s.connected) { /* ok */ }
      else if (ini && !ini.found) {
        w.push(warning('!', tr('st.apiMissing'), tr('st.apiMissingText', { path: ini.path ? ' (<code>' + esc(ini.path) + '</code>)' : '' })));
      } else if (ini && !ini.ok) {
        w.push(warning('!', tr('st.apiOff'), tr('st.apiOffText', { v: esc(ini.packet_send_rate), path: esc(ini.path || 'DefaultStatsAPI.ini') })));
      } else if (!s.connected) {
        w.push(warning('i', tr('st.waitingTitle'), tr('st.waitingText'), true));
      }
      if (s.update && s.update.version) {
        w.push(warning('i', tr('st.updateTitle', { v: s.update.version }), tr('st.updateText', { cur: esc(stripV(s.version)), url: esc(s.update.url || RELEASES_URL) }) +
          '</p><p class="w-actions"><button type="button" class="btn" data-install-update' + (state.installing ? ' disabled' : '') + '>' +
          tr(state.installing ? 'st.updateInstalling' : 'st.updateInstall') + '</button>', true));
      }
    }
    $('#warnings').innerHTML = w.join('');
    var ver = s && s.version ? 'Rocket Tracker v' + s.version : 'Rocket Tracker';
    $('#footer-version').textContent = ver + (MOCK ? ' · ' + tr('st.demo') : '');

    var live = $('#live');
    if (s && s.in_match && s.live) {
      var Lv = s.live, me = Lv.me || {};
      live.hidden = false;
      live.innerHTML =
        '<div><div class="live-tag"><span class="dot"></span>' + tr('st.live') + (state.dataFor ? ' · ' + esc((viewedPlayer() || {}).name || state.dataFor) : '') + '</div><div class="live-meta">' + esc(Lv.mode || '') + ' · ' + esc(prettyArena(Lv.arena)) +
          (Lv.me ? ' · ' + tr(Lv.my_team === 1 ? 'st.teamOrange' : 'st.teamBlue') : '') + '</div></div>' +
        '<div class="live-score" aria-label="' + esc(tr('perf.score')) + '"><span class="us" title="' + esc(tr('match.yourTeam')) + '">' + fmtNum(Lv.team_score) + '</span>' +
        '<span class="clock' + (Lv.overtime ? ' ot' : '') + '">' + (Lv.overtime ? '+' : '') + fmtClock(Lv.time_seconds) + '</span>' +
        '<span class="them" title="' + esc(tr('match.opponents')) + '">' + fmtNum(Lv.opp_score) + '</span></div>' +
        '<div class="live-me">' + ['score', 'goals', 'assists', 'saves', 'shots']
          .map(function (k) { return '<span><b>' + fmtNum(me[k]) + '</b>' + tr('perf.' + k) + '</span>'; }).join('') + '</div>';
    } else {
      live.hidden = true;
      live.innerHTML = '';
    }
  }
  var RELEASES_URL = 'https://github.com/KcivazB/rocket-tracker/releases/latest';
  function stripV(v) { return String(v || '').replace(/^v/, ''); }
  // versionBelow: a and b are x.y.z (optional "v"); false when either is not.
  function versionBelow(a, b) {
    var pa = stripV(a).split('.'), pb = stripV(b).split('.');
    if (pa.length !== 3 || pb.length !== 3 || pa.concat(pb).some(function (x) { return !/^\d+$/.test(x); })) return false;
    for (var i = 0; i < 3; i++) {
      if (+pa[i] !== +pb[i]) return +pa[i] < +pb[i];
    }
    return false;
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
    tagSel.innerHTML = tagFilterOptions();
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
    $('#view-players').hidden = view !== 'players';
    $('#export-csv').href = apiBase() + '/export.csv';
    $('#import-btn').hidden = readOnly();
    $('#recent-all').href = href('history');
    $$('[data-goal-edit]').forEach(function (el) { el.hidden = readOnly(); });
    if (view !== 'dash') {
      $('#onboarding').hidden = true;
      $('#dashboard').hidden = true;
      try {
        if (view === 'history') renderHistory(); else if (view === 'players') renderPlayers(); else renderMatchPage();
      } catch (e) { console.error('render failed: ' + view, e); }
      return;
    }
    // Data of another player still loading: show nothing stale.
    if ((state.route.player || '') !== state.dataFor) { $('#onboarding').hidden = true; $('#dashboard').hidden = true; return; }
    renderOnboarding();
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
    $('#filter-count').textContent = tr('filters.count', { v: plural(ms.length, 'match'), total: fmtNum(total) });
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
  // Each loader drops its answer when the viewed player changed meanwhile (state.gen).
  function loadMatches() {
    var gen = state.gen;
    return apiFetch(apiBase() + '/matches').then(function (data) {
      if (gen !== state.gen) return;
      state.all = annotate(Array.isArray(data) ? data : []);
      state.loaded = true;
      renderAll();
    }).catch(function (e) {
      if (gen !== state.gen) return;
      console.warn('matches', e);
      state.loaded = true;
      if (!state.all.length) renderAll();
      toast(tr('toast.loadFailed', { v: e.message }), true);
    });
  }
  function pollStatus() {
    var gen = state.gen;
    return apiFetch(apiBase() + '/status').then(function (s) {
      if (gen !== state.gen) return;
      // A new version started (update installed): load its dashboard.
      if (s && s.version && state.version && s.version !== state.version) { location.reload(); return; }
      if (s && s.version) state.version = s.version;
      state.status = s; state.statusError = false;
      renderStatus();
      if (s && isNum(s.match_count)) {
        if (state.lastCount != null && s.match_count !== state.lastCount) loadMatches();
        state.lastCount = s.match_count;
      }
    }).catch(function () {
      if (gen !== state.gen) return;
      state.statusError = true;
      renderStatus();
    });
  }
  function loadConfig() {
    var gen = state.gen;
    return apiFetch(apiBase() + '/config').then(function (c) { if (gen === state.gen) state.config = c || null; })
      .catch(function (e) { console.warn('config', e); });
  }
  /** Import button: uploads rltracker.db and/or agent outbox .json files copied from the gaming PC. */
  function bindImport() {
    var btn = $('#import-btn'), input = $('#import-file');
    btn.addEventListener('click', function () {
      if (MOCK) { toast(tr('toast.noImportDemo')); return; }
      input.value = '';
      input.click();
    });
    input.addEventListener('change', function () {
      if (!input.files || !input.files.length) return;
      var fd = new FormData();
      Array.prototype.forEach.call(input.files, function (f) { fd.append('file', f, f.name); });
      btn.disabled = true;
      toast(tr('toast.importing'));
      fetch('/api/import', { method: 'POST', body: fd, cache: 'no-store' }).then(function (r) {
        return r.text().then(function (txt) {
          var data = null;
          try { data = JSON.parse(txt); } catch (e) { /* not JSON */ }
          if (!r.ok) throw new Error('HTTP ' + r.status + (data && data.error ? ' : ' + data.error : ''));
          return data || {};
        });
      }).then(function (res) {
        var skipped = (res.skipped || []).length;
        if (skipped) console.warn('import skipped', res.skipped);
        toast(tr('toast.imported', { n: res.matches || 0, m: res.manual || 0 }) + (skipped ? ' · ' + tr('toast.importSkipped', { v: skipped }) : ''), skipped > 0 && !res.matches);
        loadAll();
      }).catch(function (e) {
        toast(tr('toast.importFailed', { v: e.message }), true);
      }).then(function () { btn.disabled = false; });
    });
  }

  /** (Re)loads everything for the player of the current route. */
  function loadAll() {
    state.dataFor = state.route.player || '';
    state.gen++;
    Object.assign(state, { all: [], filtered: [], manual: [], config: null, status: null, lastCount: null, loaded: false, open: {}, shown: PAGE_SIZE, histShown: HIST_PAGE });
    renderStatus();
    var gen = state.gen;
    return Promise.all([pollStatus(), Promise.all([loadConfig(), loadManual()]).then(loadMatches)]).then(function () {
      if (gen === state.gen && state.status && isNum(state.status.match_count)) state.lastCount = state.status.match_count;
    });
  }

  /* ---------- table interactions ---------- */
  function bindTable() {
    var tbl = $('#t-matches');
    tbl.addEventListener('click', function (e) {
      var del = e.target.closest('[data-del]');
      if (del) { e.stopPropagation(); deleteMatch(+del.dataset.del); return; }
      if (e.target.closest('select, button, a')) return;
      var row = e.target.closest('tr.row');
      if (!row) return;
      toggleRow(+row.dataset.id);
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
      toast(tr('toast.tagUpdated', { v: tagLabel(tag) }));
      renderAll();
    }).catch(function (e) {
      sel.value = prev; sel.disabled = false;
      toast(tr('toast.updateFailed', { v: e.message }), true);
    });
  }
  function deleteMatch(id, after) {
    var m = findMatch(id);
    if (!m) return;
    var desc = fmtDate(m._t, true) + ' · ' + (m.mode || '') + ' · ' + num(m.team_score) + '–' + num(m.opp_score);
    if (!window.confirm(tr('confirm.deleteMatch') + '\n\n' + desc)) return;
    apiFetch('/api/matches/' + id, { method: 'DELETE' }).then(function () {
      state.all = annotate(state.all.filter(function (x) { return x.id !== id; }));
      delete state.open[id];
      if (state.lastCount != null) state.lastCount = Math.max(0, state.lastCount - 1);
      toast(tr('toast.deleted'));
      if (after) after(); else renderAll();
    }).catch(function (e) { toast(tr('toast.deleteFailed', { v: e.message }), true); });
  }

  /* ---------- settings ---------- */
  var currentConfig = null;
  function openSettings() {
    var dlg = $('#settings');
    var form = $('#settings-form');
    var msg = $('#settings-msg');
    msg.textContent = tr('common.loading'); msg.className = 'form-msg';
    form.elements.default_tag.innerHTML = tagOptions('ranked');
    form.elements.lang.value = I18N.getLang();
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
    }).catch(function (e) { msg.textContent = tr('settings.readFailed', { v: e.message }); msg.className = 'form-msg err'; });
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
      msg.textContent = tr('common.saving'); msg.className = 'form-msg';
      var lang = form.elements.lang.value;
      apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(cfg) }).then(function (c) {
        currentConfig = c || cfg;
        if (!state.dataFor) state.config = currentConfig;
        dlg.close ? dlg.close() : dlg.removeAttribute('open');
        if (lang !== I18N.getLang()) { I18N.save(lang); location.reload(); return; }
        toast(tr('toast.settingsSaved'));
        renderAll();
        pollStatus();
      }).catch(function (err) { msg.textContent = tr('toast.saveFailed', { v: err.message }); msg.className = 'form-msg err'; });
    });
  }

  /* ---------- manual games (day dialog) ---------- */
  var MANUAL_MODES = ['1v1', '2v2', '3v3', '4v4'];
  var dayDialogKey = null;
  function loadManual() {
    var gen = state.gen;
    return apiFetch(apiBase() + '/manual').then(function (d) { if (gen === state.gen) state.manual = Array.isArray(d) ? d : []; })
      .catch(function (e) { console.warn('manual', e); });
  }
  function manualFor(day, mode) {
    return state.manual.find(function (e) { return e.day === day && e.mode === mode; }) || { games: 0, wins: 0 };
  }
  function openDay(day) {
    dayDialogKey = day;
    var dlg = $('#day-dialog');
    var t = parseDay(day);
    var title = fmtDay(t, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
    $('#day-title').textContent = title.charAt(0).toUpperCase() + title.slice(1);
    var tracked = trackedByMode(state.all, day);
    $('#day-rows').innerHTML = MANUAL_MODES.map(function (mode) {
      var tk = tracked[mode] || { games: 0, wins: 0 }, mn = manualFor(day, mode);
      return '<tr data-mode="' + mode + '"><th scope="row">' + mode + '</th>' +
        '<td class="r muted">' + (tk.games ? tk.games + ' <small>(' + tk.wins + ' ' + tr('res.win.short') + ')</small>' : '—') + '</td>' +
        '<td><input type="number" min="0" max="500" step="1" inputmode="numeric" name="g-' + mode + '" value="' + num(mn.games) + '" aria-label="' + esc(tr('day.gamesAria', { mode: mode })) + '"></td>' +
        '<td><input type="number" min="0" max="500" step="1" inputmode="numeric" name="w-' + mode + '" value="' + num(mn.wins) + '" aria-label="' + esc(tr('day.winsAria', { mode: mode })) + '"></td></tr>';
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
        if (!isNum(g) || !isNum(w) || g < 0 || w < 0 || g > 500) { msg.textContent = tr('day.invalid', { mode: mode }); msg.className = 'form-msg err'; return; }
        if (w > g) { msg.textContent = tr('day.tooManyWins', { mode: mode }); msg.className = 'form-msg err'; return; }
        var cur = manualFor(day, mode);
        if (g !== num(cur.games) || (g > 0 && w !== num(cur.wins))) changes.push({ mode: mode, games: g, wins: g ? w : 0 });
      }
      if (!changes.length) { dlg.close ? dlg.close() : dlg.removeAttribute('open'); return; }
      msg.textContent = tr('common.saving'); msg.className = 'form-msg';
      Promise.all(changes.map(function (c) {
        return apiFetch('/api/manual/' + day + '/' + c.mode, { method: 'PUT', body: JSON.stringify({ games: c.games, wins: c.wins }) });
      })).then(loadManual).then(function () {
        dlg.close ? dlg.close() : dlg.removeAttribute('open');
        toast(tr('toast.daySaved', { v: fmtDay(parseDay(day), { day: 'numeric', month: 'long' }) }));
        renderAll();
      }).catch(function (err) {
        msg.textContent = tr('toast.saveFailed', { v: err.message }); msg.className = 'form-msg err';
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

  /* ---------- server mode: accounts, players, devices ---------- */
  var ONBOARDING_LOCAL = null;
  function applyMode() {
    var srv = isServer();
    $('#nav-players').hidden = !srv;
    $('#user-menu').hidden = !srv;
    $$('.local-only').forEach(function (el) { el.hidden = srv; });
    if (srv && state.me) {
      $('#me-name').textContent = state.me.name || state.me.handle;
      $('#me-avatar').textContent = initial(state.me.name || state.me.handle);
      $('#me-avatar').style.background = avatarColor(state.me.handle);
    }
    $('#footer-note').textContent = tr(srv ? 'footer.server' : 'footer.local');
  }
  function initial(s) { return String(s || '?').trim().charAt(0).toUpperCase() || '?'; }
  function avatarColor(seed) {
    var h = 0, s = String(seed || '');
    for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    return 'hsl(' + (h % 360) + ' 55% 42%)';
  }
  function avatar(p, cls) {
    return '<span class="avatar' + (cls ? ' ' + cls : '') + '" style="background:' + avatarColor(p.handle) + '" aria-hidden="true">' + esc(initial(p.name || p.handle)) + '</span>';
  }

  function renderOnboarding() {
    var card = $('#onboarding .onboarding-card');
    if (ONBOARDING_LOCAL == null) ONBOARDING_LOCAL = card.innerHTML;
    var art = card.querySelector('.onboarding-art');
    var artHtml = art ? art.outerHTML : '';
    if (state.dataFor) {
      var p = viewedPlayer();
      card.innerHTML = artHtml + '<h1>' + tr('onb.playerEmpty', { name: esc(p.name) }) + '</h1><p class="lead">' + tr('onb.playerEmptyText') + '</p>' +
        '<p><a class="btn" href="#/players"><span aria-hidden="true">←</span> ' + tr('onb.allPlayers') + '</a></p>';
    } else if (isServer()) {
      card.innerHTML = artHtml + '<h1>' + (state.me ? tr('onb.welcomeName', { name: esc(state.me.name) }) : tr('onb.welcome')) + '</h1>' +
        '<p class="lead">' + tr('onb.serverLead') + '</p>' +
        '<ol class="steps">' +
        '<li><span class="step-n">1</span><div>' + tr('onb.server1') + '</div></li>' +
        '<li><span class="step-n">2</span><div>' + tr('onb.server2') + '</div></li>' +
        '<li><span class="step-n">3</span><div>' + tr('onb.server3') + '</div></li>' +
        '</ol>';
    } else {
      card.innerHTML = ONBOARDING_LOCAL;
    }
  }

  function renderPlayerBanner() {
    var box = $('#player-banner');
    var p = viewedPlayer();
    if (!p || state.route.view === 'players') { box.hidden = true; box.innerHTML = ''; return; }
    var v = state.route.view;
    var tab = function (path, label, on) {
      return '<a href="' + routeHash(p.handle, path) + '"' + (on ? ' aria-current="page"' : '') + '>' + label + '</a>';
    };
    var names = (p.game_names || []).filter(function (n) { return normText(n) !== normText(p.name); });
    box.hidden = false;
    box.innerHTML = avatar(p, 'lg') +
      '<div class="pb-who"><span class="pb-label">' + tr('pb.statsOf') + '</span><strong>' + esc(p.name) + '</strong>' +
        '<span class="pb-meta">@' + esc(p.handle) + (names.length ? ' · ' + tr('pb.inGame', { v: names.map(esc).join(', ') }) : '') + '</span></div>' +
      '<nav class="pb-tabs" aria-label="' + esc(tr('pb.pagesOf', { name: p.name })) + '">' + tab('', tr('nav.dash'), v === 'dash') + tab('history', tr('nav.history'), v !== 'dash') + '</nav>' +
      '<a class="btn btn-ghost pb-back" href="#/players"><span aria-hidden="true">←</span> ' + tr('nav.players') + '</a>';
  }

  function loadPlayers() {
    if (!isServer()) return Promise.resolve();
    var lb = state.lb;
    var q = '?mode=' + encodeURIComponent(lb.mode) + '&tag=' + encodeURIComponent(lb.tag) + '&days=' + encodeURIComponent(lb.period);
    return Promise.all([apiFetch('/api/players'), apiFetch('/api/leaderboard' + q)]).then(function (res) {
      state.players = Array.isArray(res[0]) ? res[0] : [];
      state.leaders = Array.isArray(res[1]) ? res[1] : [];
      if (state.route.view === 'players') renderPlayers(); else syncNav();
    }).catch(function (e) { console.warn('players', e); });
  }

  function ago(iso) {
    var t = Date.parse(iso);
    if (!isNum(t)) return 'jamais';
    var d = Math.floor((startOfDay(Date.now()) - startOfDay(t)) / DAY_MS);
    return d <= 0 ? tr('common.today') : d === 1 ? tr('common.yesterday') : d < 30 ? tr('common.daysAgo', { v: d }) : tr('common.onDate', { date: fmtDay(t, { day: 'numeric', month: 'short', year: 'numeric' }) });
  }
  function playerCard(p) {
    var st = p.in_match && p.live
      ? '<span class="ps live"><span class="dot"></span>' + tr('pl.inMatch') + ' · ' + esc(p.live.mode || '') + ' · ' + fmtNum(p.live.team_score) + '–' + fmtNum(p.live.opp_score) + '</span>'
      : p.online ? '<span class="ps on"><span class="dot"></span>' + tr('pl.agentOnline') + '</span>' : '<span class="ps"><span class="dot"></span>' + tr('badge.offline') + '</span>';
    var names = (p.game_names || []).filter(function (n) { return normText(n) !== normText(p.name); });
    return '<a class="card player-card' + (p.is_me ? ' me' : '') + '" href="' + (p.is_me ? '#/' : routeHash(p.handle, '')) + '">' + avatar(p) +
      '<span class="pc-main"><span class="pc-name">' + esc(p.name) + (p.is_me ? ' <span class="chip">' + tr('common.you') + '</span>' : '') + '</span>' +
        '<span class="pc-meta">@' + esc(p.handle) + (names.length ? ' · ' + names.map(esc).join(', ') : '') + '</span>' + st + '</span>' +
      '<span class="pc-stats"><b class="num">' + fmtNum(p.matches) + '</b><span>' + tr('unit.match', { n: p.matches }) + '</span>' +
        '<span class="pc-last">' + (p.last_played ? tr('pl.last', { v: ago(p.last_played) }) : tr('pl.noMatch')) + '</span></span></a>';
  }

  var LB_COLS = [
    ['rank', '#', '', null], ['name', 'match.player', '', 1], ['games', 'kpi.matches', 'r', -1], ['winrate', 'kpi.winrate', 'r', -1],
    ['goal_diff_avg', 'tbl.diff', 'r', -1], ['score_avg', 'perf.score', 'r hide-mobile', -1], ['goals_avg', 'perf.goals', 'r hide-mobile', -1],
    ['assists_avg', 'perf.assists', 'r hide-mobile', -1], ['saves_avg', 'perf.saves', 'r hide-mobile', -1], ['shots_avg', 'perf.shots', 'r hide-mobile', -1],
    ['mvp_rate', 'feed.MVP', 'r hide-mobile', -1]
  ];
  function renderLeaders() {
    var lb = state.lb;
    var all = rankLeaders(state.leaders || [], lb.sort, lb.dir, lb.min);
    var shown = filterPlayers(all, state.playersQ);
    var head = '<thead><tr>' + LB_COLS.map(function (c) {
      if (!c[3]) return '<th class="c" style="width:44px">' + c[1] + '</th>';
      var on = lb.sort === c[0];
      return '<th class="' + c[2] + '" aria-sort="' + (on ? (lb.dir === 1 ? 'ascending' : 'descending') : 'none') + '"><button type="button" class="th-sort' + (on ? ' on' : '') +
        '" data-sort="' + c[0] + '" data-dir="' + c[3] + '">' + tr(c[1]) + (on ? '<span aria-hidden="true">' + (lb.dir === 1 ? ' ▲' : ' ▼') + '</span>' : '') + '</button></th>';
    }).join('') + '</tr></thead>';
    var hidden = (state.leaders || []).length - all.length;
    var body;
    if (!state.leaders) body = '<tr class="empty-row"><td colspan="11">' + tr('common.loading') + '</td></tr>';
    else if (!shown.length) body = '<tr class="empty-row"><td colspan="11">' + tr(all.length ? 'lb.noSearch' : 'lb.noPlayer') + '</td></tr>';
    else body = shown.map(function (r) {
      var wr = isNum(r.winrate) ? r.winrate : null;
      return '<tr class="' + (r.is_me ? 'me' : '') + '"><td class="c"><span class="lb-rank r' + Math.min(r.rank, 4) + '">' + r.rank + '</span></td>' +
        '<td><a class="lb-player" href="' + (r.is_me ? '#/' : routeHash(r.handle, '')) + '">' + avatar(r, 'sm') + '<span>' + esc(r.name) + '</span>' + (r.is_me ? ' <span class="chip">vous</span>' : '') + '</a></td>' +
        '<td class="r num">' + fmtNum(r.games) + '<span class="muted small hide-mobile"> (' + r.wins + '–' + r.losses + ')</span></td>' +
        '<td class="r num"><span class="lb-wr"><span class="lb-bar"><i style="width:' + (wr == null ? 0 : Math.round(wr * 100)) + '%"></i></span><b>' + fmtPct(wr) + '</b></span></td>' +
        '<td class="r num ' + signCls(r.goal_diff_avg) + '">' + fmtSigned(r.goal_diff_avg, 2) + '</td>' +
        '<td class="r num hide-mobile">' + fmtNum(r.score_avg) + '</td>' +
        '<td class="r num hide-mobile">' + fmtNum(r.goals_avg, 2) + '</td>' +
        '<td class="r num hide-mobile">' + fmtNum(r.assists_avg, 2) + '</td>' +
        '<td class="r num hide-mobile">' + fmtNum(r.saves_avg, 2) + '</td>' +
        '<td class="r num hide-mobile">' + fmtNum(r.shots_avg, 2) + '</td>' +
        '<td class="r num hide-mobile">' + fmtPct(r.mvp_rate) + '</td></tr>';
    }).join('');
    $('#t-leaders').innerHTML = head + '<tbody>' + body + '</tbody>';
    $('#lb-note').textContent = hidden > 0 ? tr('lb.unranked', { n: hidden, v: fmtNum(hidden), min: lb.min }) : '';
  }
  function syncLbUi() {
    var lb = state.lb;
    [['#lb-mode', 'mode'], ['#lb-period', 'period']].forEach(function (x) {
      $$(x[0] + ' button').forEach(function (b) { b.setAttribute('role', 'radio'); b.setAttribute('aria-checked', String(b.dataset.v === String(lb[x[1]]))); });
    });
    var tagSel = $('#lb-tag');
    if (!tagSel.options.length) tagSel.innerHTML = '<option value="all">' + tr('common.all') + '</option>' + TAGS.map(function (t) { return '<option value="' + t + '">' + esc(tagLabel(t)) + '</option>'; }).join('');
    tagSel.value = lb.tag;
    $('#lb-min').value = String(lb.min);
  }
  function renderPlayers() {
    syncLbUi();
    var q = $('#p-q');
    if (document.activeElement !== q && q.value !== state.playersQ) q.value = state.playersQ;
    var box = $('#players-list');
    if (!state.players) box.innerHTML = '<div class="hist-empty muted">' + tr('common.loading') + '</div>';
    else {
      var ps = filterPlayers(state.players, state.playersQ).slice().sort(function (a, b) {
        return (b.in_match - a.in_match) || (b.online - a.online) || String(b.last_played).localeCompare(String(a.last_played)) || a.name.localeCompare(b.name);
      });
      $('#players-count').textContent = state.playersQ ? tr('pl.countOf', { v: ps.length, total: plural(state.players.length, 'player') })
        : tr('pl.count', { n: state.players.length, v: fmtNum(state.players.length) });
      box.innerHTML = ps.length ? ps.map(playerCard).join('') : '<div class="no-results"><p><strong>' + tr('pl.noMatchSearch', { q: esc(state.playersQ) }) + '</strong></p><p class="muted">' + tr('pl.searchHint') + '</p></div>';
    }
    renderLeaders();
  }
  function bindPlayers() {
    $('#p-q').addEventListener('input', function (e) { state.playersQ = e.target.value; renderPlayers(); });
    function seg(id, key) {
      $(id).addEventListener('click', function (e) {
        var b = e.target.closest('button[data-v]');
        if (!b) return;
        state.lb[key] = b.dataset.v;
        saveLbFilters(); state.leaders = null; renderLeaders(); loadPlayers();
      });
    }
    seg('#lb-mode', 'mode');
    seg('#lb-period', 'period');
    $('#lb-tag').addEventListener('change', function (e) { state.lb.tag = e.target.value; saveLbFilters(); state.leaders = null; renderLeaders(); loadPlayers(); });
    $('#lb-min').addEventListener('change', function (e) { state.lb.min = parseInt(e.target.value, 10) || 0; saveLbFilters(); renderLeaders(); });
    $('#t-leaders').addEventListener('click', function (e) {
      var b = e.target.closest('[data-sort]');
      if (!b) return;
      var k = b.getAttribute('data-sort');
      if (state.lb.sort === k) state.lb.dir = -state.lb.dir; else { state.lb.sort = k; state.lb.dir = +b.getAttribute('data-dir'); }
      saveLbFilters(); renderLeaders();
    });
  }

  /* devices (agent tokens) */
  function openDevices() {
    var dlg = $('#devices');
    $('#device-new').hidden = true;
    $('#devices-msg').textContent = '';
    $('#devices-form').elements.name.value = '';
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
    loadDevices();
  }
  function loadDevices() {
    var box = $('#devices-list');
    box.innerHTML = '<p class="muted small">' + tr('common.loading') + '</p>';
    return apiFetch('/api/devices').then(function (ds) {
      ds = Array.isArray(ds) ? ds : [];
      box.innerHTML = ds.length ? '<ul class="dev-list">' + ds.map(function (d) {
        return '<li><span class="ps' + (d.online ? ' on' : '') + '" title="' + esc(tr(d.online ? 'pl.agentOnline' : 'st.agentOffline')) + '"><span class="dot"></span></span>' +
          '<span class="dev-main"><b>' + esc(d.name) + '</b><span class="muted small">' + tr('dev.created', { v: ago(d.created_at) }) + ' · ' +
          (d.last_seen_at ? tr('dev.lastSeen', { v: d.online ? tr('dev.now') : ago(d.last_seen_at) }) : tr('dev.never')) + '</span></span>' +
          '<button type="button" class="btn btn-ghost btn-danger" data-revoke="' + d.id + '" data-name="' + esc(d.name) + '">' + tr('dev.revoke') + '</button></li>';
      }).join('') + '</ul>' : '<p class="muted small">' + tr('dev.none') + '</p>';
    }).catch(function (e) { box.innerHTML = '<p class="form-msg err">' + tr('dev.listFailed', { v: esc(e.message) }) + '</p>'; });
  }
  /** Local mode: installs the newer release; the app restarts and pollStatus reloads the page. */
  function installUpdate() {
    if (state.installing || MOCK) return;
    state.installing = true;
    renderStatus();
    apiFetch('/api/update/install', { method: 'POST' }).then(function () {
      toast(tr('toast.restarting'));
    }).catch(function (e) {
      state.installing = false;
      renderStatus();
      toast(tr('toast.installFailed', { v: e.message }), true);
    });
  }
  function bindDevices() {
    var dlg = $('#devices'), form = $('#devices-form');
    document.addEventListener('click', function (e) {
      if (e.target.closest('[data-open-devices]')) { e.preventDefault(); openDevices(); }
      if (e.target.closest('[data-install-update]')) { e.preventDefault(); installUpdate(); }
    });
    $$('[data-close]', dlg).forEach(function (b) { b.addEventListener('click', function () { dlg.close ? dlg.close() : dlg.removeAttribute('open'); }); });
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var msg = $('#devices-msg');
      msg.textContent = tr('dev.creating'); msg.className = 'form-msg';
      apiFetch('/api/devices', { method: 'POST', body: JSON.stringify({ name: form.elements.name.value.trim() }) }).then(function (r) {
        msg.textContent = '';
        form.elements.name.value = '';
        $('#device-cmd').textContent = 'rltracker agent setup --server ' + r.server + ' --token ' + r.token;
        $('#device-new-text').innerHTML = tr('dev.newText', { name: esc(r.device.name) });
        $('#device-new').hidden = false;
        loadDevices();
        pollStatus();
      }).catch(function (err) { msg.textContent = tr('dev.createFailed', { v: err.message }); msg.className = 'form-msg err'; });
    });
    $('#device-copy').addEventListener('click', function () {
      var txt = $('#device-cmd').textContent;
      var done = function () { toast(tr('dev.copied')); };
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(txt).then(done, function () { selectText($('#device-cmd')); });
      else selectText($('#device-cmd'));
    });
    $('#devices-list').addEventListener('click', function (e) {
      var b = e.target.closest('[data-revoke]');
      if (!b) return;
      if (!confirm(tr('dev.confirmRevoke', { name: b.getAttribute('data-name') }))) return;
      apiFetch('/api/devices/' + b.getAttribute('data-revoke'), { method: 'DELETE' }).then(function () {
        toast(tr('dev.revoked')); loadDevices(); pollStatus();
      }).catch(function (err) { toast(tr('dev.revokeFailed', { v: err.message }), true); });
    });
  }
  function selectText(el) {
    var r = document.createRange(); r.selectNodeContents(el);
    var s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
  }

  /* ---------- boot ---------- */
  function boot() {
    I18N.apply(document);
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
      $('#export-csv').addEventListener('click', function (e) { e.preventDefault(); toast(tr('toast.noCsvDemo')); });
    }
    bindImport();
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: light)');
      var onTheme = function () { renderAll(); renderStatus(); };
      if (mq.addEventListener) mq.addEventListener('change', onTheme); else if (mq.addListener) mq.addListener(onTheme);
    }
    readTheme();
    applyChartDefaults();
    bindPlayers();
    bindDevices();
    apiFetch('/api/session').catch(function () { return { mode: 'local' }; }).then(function (s) {
      state.mode = s && s.mode === 'server' ? 'server' : 'local';
      state.me = (s && s.user) || null;
      applyMode();
      var self = selfRoute(state.route);
      if (self) { location.replace(self); state.route = parseRoute(self); }
      if (!isServer() && (state.route.player || state.route.view === 'players')) { location.replace('#/'); state.route = parseRoute('#/'); }
      if (isServer()) loadPlayers();
      loadAll();
      renderAll();
    });
    setInterval(pollStatus, 3000);
    setInterval(function () { if (isServer() && state.route.view === 'players') loadPlayers(); }, 10000);
    // Re-render the activity window when the day changes (cheap, once per 10 min).
    setInterval(function () { if (state.all.length) renderAll(); }, 10 * 60 * 1000);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot); else boot();
})(typeof globalThis !== 'undefined' ? globalThis : this);
