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
  var DEFAULT_REG_S = 300; // regulation length of a standard match (s)
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
  // Historical records used a uu/s convention without verified provenance. Keep their cohort
  // separate from new unknown observations and declared units; never infer or convert by magnitude.
  function speedUnit(m) {
    if (m && (m.speed_unit == null || m.speed_unit === '')) return 'legacy';
    return m && (m.speed_unit === 'uu/s' || m.speed_unit === 'km/h') ? m.speed_unit : 'raw';
  }
  function speedUnitLabel(unit) { return unit === 'legacy' ? tr('unit.legacySpeed') : unit === 'raw' || !unit ? tr('unit.rawSpeed') : unit; }
  function formatRawSpeed(v, unit) {
    var label = unit === 'legacy' || unit === 'uu/s' || unit === 'km/h' ? unit : 'raw';
    return isNum(v) ? fmtNum(v, 1) + ' ' + speedUnitLabel(label) : '—';
  }
  /** No arithmetic across incompatible units or historical/unknown cohorts. */
  function commonSpeedUnit(ms) {
    var units = [];
    ms.forEach(function (m) {
      var hasSpeed = m.movement && isNum(m.movement.avg_speed) || m.hits &&
        (isNum(m.hits.avg_speed) || isNum(m.hits.max_speed) || hitPoints(m).some(function (p) { return p && (isNum(p.pre) || isNum(p.post)); }));
      if (hasSpeed && units.indexOf(speedUnit(m)) < 0) units.push(speedUnit(m));
    });
    return units.length > 1 ? null : units[0] || 'raw';
  }
  /** Regulation length (s) of a match: duration minus overtime when in OT (handles a regulation inferred ≠ 300 s). */
  function regulationS(m) {
    if (m && m.regulation_s > 0) return m.regulation_s;
    var r = m && m.overtime ? num(m.duration_s) - num(m.overtime_s) : 300;
    return r > 0 ? r : 300;
  }
  /** Honor Duel: the 3-minute matches (known from v0.22, regulation seen from the start). */
  var DUEL_REG_S = 180;
  function isDuel(m) { return !!m && m.regulation_s === DUEL_REG_S; }
  /** Duel filter: 'exclude' (default), 'include' or 'only'. */
  function duelOk(m, f) { return f === 'only' ? isDuel(m) : f === 'include' || !isDuel(m); }
  /** Matches whose stats can be compared with each other: same mode (or variant), duels apart. */
  function modeGroup(m) { return JSON.stringify([m.variant || 'Soccar', m.mode || '', isDuel(m)]); }
  /**
   * fn(m) minus its mean over the matches of m's mode group in ms (null when missing): a 1v1 and a 3v3 value
   * are not on the same scale, so trends and comparisons across modes use these deviations.
   */
  function modeDeviations(ms, fn) {
    var vals = ms.map(fn), sums = {};
    ms.forEach(function (m, i) {
      if (!isNum(vals[i])) return;
      var g = sums[modeGroup(m)] || (sums[modeGroup(m)] = { s: 0, n: 0 });
      g.s += vals[i]; g.n++;
    });
    return ms.map(function (m, i) { var g = sums[modeGroup(m)]; return isNum(vals[i]) ? vals[i] - g.s / g.n : null; });
  }
  /**
   * Before v0.20.1 the wall time was counted as ground time and wall_pct was always 0: on those matches the
   * ground/wall split is unknown (air is right). A real match with no wall at all over a minute is not plausible.
   */
  var WALL_KNOWN_MIN_S = 60;
  function wallKnown(mv) { return !!mv && !(mv.wall_pct === 0 && num(mv.sample_s) >= WALL_KNOWN_MIN_S); }
  /** Movement value of m (null when missing, ground/wall null when not measured apart). */
  function mvValue(m, key) {
    var o = m && m.movement;
    if (!o || !isNum(o[key])) return null;
    if ((key === 'wall_pct' || key === 'ground_pct') && !wallKnown(o)) return null;
    return o[key];
  }
  /** True when goal g of match m was scored in overtime. */
  function isOtGoal(m, g) { return !!g.overtime || num(g.t) > regulationS(m); }
  /** One eligibility rule for every inferred historical score, first goal and kickoff. */
  function goalsComplete(m) { return liveTimelineComplete(m); }
  /** A goal within this many game seconds of a kickoff is a kickoff goal: the kickoff was lost by the team conceding it. */
  var KICKOFF_GOAL_S = 3;
  /** Goals of m in time order, those scored straight off a kickoff (match start, previous goal or overtime start) flagged `kickoff`. */
  function kickoffGoals(m) {
    var reg = regulationS(m), prev = 0;
    return (m.goals || []).filter(function (g) { return g && typeof g === 'object'; })
      .sort(function (a, b) { return num(a.t) - num(b.t); })
      .map(function (g) {
        var t = num(g.t), from = isOtGoal(m, g) ? Math.max(prev, reg) : prev;
        prev = t;
        return { goal: g, kickoff: t - from <= KICKOFF_GOAL_S };
      });
  }

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

  var DEFAULT_FILTERS = { mode: 'all', period: '30', tag: 'all', onlineOnly: true, excludeAbandoned: true, duel: 'exclude' };

  function filterMatches(ms, f, now) {
    f = f || DEFAULT_FILTERS;
    now = isNum(now) ? now : Date.now();
    var minT = f.period && f.period !== 'all' ? now - (+f.period) * DAY_MS : -Infinity;
    return ms.filter(function (m) {
      if (f.mode && f.mode !== 'all' && modeKey(m) !== f.mode) return false;
      if (f.tag && f.tag !== 'all' && (m.tag || 'other') !== f.tag) return false;
      if (f.onlineOnly && m.online === false) return false;
      if (f.excludeAbandoned && m.result === 'abandoned') return false;
      if (!duelOk(m, f.duel)) return false;
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
    var first = dec.filter(goalsComplete);
    var firstUs = wl(first.filter(function (m) { var g = sortedGoalsOf(m)[0]; return g && g.team === 'us'; }));
    var firstThem = wl(first.filter(function (m) { var g = sortedGoalsOf(m)[0]; return g && g.team === 'them'; }));
    var close = wl(dec.filter(function (m) { return isNum(m.goal_diff) && Math.abs(m.goal_diff) <= 1; }));
    var big = wl(dec.filter(function (m) { return isNum(m.goal_diff) && Math.abs(m.goal_diff) >= 3; }));
    var otMs = dec.filter(function (m) { return m.overtime; });
    var ot = wl(otMs);
    // Kickoffs: only matches whose goal list is complete (a missed goal would shift the next kickoff).
    var ko = { kickoffs: 0, won: 0, lost: 0, conceded: 0, matches: 0 };
    var koLostMs = [];
    ms.forEach(function (m) {
      if (!goalsComplete(m)) return;
      var lost = 0;
      ko.matches++;
      ko.kickoffs += 1 + (m.overtime ? 1 : 0) + kickoffGoals(m).filter(function (k) {
        if (k.goal.team === 'them') ko.conceded++;
        if (k.kickoff && k.goal.team === 'us') ko.won++;
        if (k.kickoff && k.goal.team === 'them') { ko.lost++; lost++; }
        // A terminal goal has no restart; the overtime-start kickoff is counted separately.
        return !isOtGoal(m, k.goal) && num(k.goal.t) < Math.min(regulationS(m), num(m.duration_s) || regulationS(m));
      }).length;
      if (lost) koLostMs.push(m);
    });
    ko.wonShare = ratio(ko.won, ko.kickoffs);
    ko.lostShare = ratio(ko.lost, ko.kickoffs);
    ko.concededShare = ratio(ko.lost, ko.conceded);
    ko.afterLost = wl(koLostMs.filter(isDecided));
    return {
      kickoff: ko,
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

  /** Goals by real game minute, followed by overtime, ours vs theirs. */
  function computeGoalsPerMinute(ms) {
    var qualified = ms.filter(goalsComplete);
    var minutes = qualified.reduce(function (n, m) { return Math.max(n, Math.ceil(regulationS(m) / 60)); }, 0) || 5;
    var labels = [], us = [], them = [];
    for (var i = 0; i < minutes; i++) { labels.push(i + '–' + (i + 1) + '′'); us.push(0); them.push(0); }
    labels.push(tr('goals.ot')); us.push(0); them.push(0);
    var nGoals = 0, nMatches = 0;
    ms.forEach(function (m) {
      if (!goalsComplete(m)) return;
      nMatches++;
      var reg = regulationS(m);
      (m.goals || []).forEach(function (g) {
        if (!g) return;
        var t = num(g.t);
        // Terminal regulation goals belong to the last real minute; OT has its own bucket.
        var b = isOtGoal(m, g) ? minutes : Math.min(Math.ceil(reg / 60) - 1, Math.floor(t / 60));
        if (g.team === 'us') us[b]++; else if (g.team === 'them') them[b]++; else return;
        nGoals++;
      });
    });
    return { labels: labels, us: us, them: them, matches: nMatches, goals: nGoals };
  }

  var MECH_METRICS = [
    { key: 'avg_speed', unit: 'raw', digits: 0, higherBetter: true },
    { key: 'supersonic_pct', unit: '%', digits: 1, higherBetter: true },
    { key: 'air_pct', unit: '%', digits: 1, higherBetter: null },
    { key: 'avg_boost', unit: '', digits: 0, higherBetter: null },
    { key: 'zero_boost_pct', unit: '%', digits: 1, higherBetter: false },
    { key: 'hits_avg', unit: 'raw', digits: 0, higherBetter: true, src: 'hits' }
  ];

  function mechValue(m, metric) {
    var raw;
    if (metric.src === 'hits') raw = m.hits ? m.hits.avg_speed : null;
    else raw = m.movement ? m.movement[metric.key] : null;
    if (!isNum(raw)) return null;
    return metric.conv ? metric.conv(raw) : raw;
  }

  function computeMechanics(ms) {
    var withMove = ms.filter(function (m) { return m.movement || m.hits; }), unit = commonSpeedUnit(ms);
    var metrics = MECH_METRICS.map(function (metric) {
      var value = function (m) { return metric.unit === 'raw' && !unit ? null : mechValue(m, metric); };
      var vals = withMove.map(value);
      var valid = vals.filter(isNum);
      // Trend on the deviations from each mode's mean: switching from 3v3 to 1v1 is not progress.
      var devs = modeDeviations(withMove, value).filter(isNum);
      var lastN = devs.slice(-ROLL_WINDOW), prevN = devs.slice(-2 * ROLL_WINDOW, -ROLL_WINDOW);
      var trend = (lastN.length >= ROLL_MIN && prevN.length >= ROLL_MIN) ? mean(lastN) - mean(prevN) : null;
      return {
        key: metric.key, label: tr('mech.' + metric.key), unit: metric.unit === 'raw' ? unit || 'raw' : metric.unit, digits: metric.digits, higherBetter: metric.higherBetter,
        avg: mean(valid), n: valid.length, trend: trend,
        series: rolling(withMove, value)
      };
    });
    var mv = ms.filter(function (m) { return m.movement; }).map(function (m) { return m.movement; });
    // All three shares must be available from the same matches, so the split adds up.
    var split = mv.filter(function (x) { return wallKnown(x) && [x.ground_pct, x.wall_pct, x.air_pct].every(isNum); });
    var maxHit = ms.map(function (m) { return m.hits ? m.hits.max_speed : null; }).filter(isNum);
    return {
      n: withMove.length,
      metrics: metrics,
      split: {
        ground: mean(split.map(function (x) { return x.ground_pct; })),
        wall: mean(split.map(function (x) { return x.wall_pct; })),
        air: mean(split.map(function (x) { return x.air_pct; })),
        n: split.length
      },
      boostingPct: mean(mv.map(function (x) { return x.boosting_pct; })),
      fullBoostPct: mean(mv.map(function (x) { return x.full_boost_pct; })),
      powerslidePct: mean(mv.map(function (x) { return x.powerslide_pct; })),
      demolishedS: mean(mv.map(function (x) { return x.demolished_s; })),
      maxHitRaw: unit && maxHit.length ? Math.max.apply(null, maxHit) : null, speedUnit: unit
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
  /** Hash route: '#/' dashboard, '#/history' history, '#/match/<id>' match page (id null when invalid), '#/live' match in progress,
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
    if (h === 'training' || h === 'entrainement') return { view: 'training', id: null, player: player };
    if (h === 'live' || h === 'en-partie') return { view: 'live', id: null, player: player };
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

  var DEFAULT_HIST_FILTERS = { mode: 'all', result: 'all', tag: 'all', period: 'all', q: '', includeOffline: true, duel: 'include' };
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
      if (!duelOk(m, f.duel)) return false;
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
    { key: 'avg_speed', lk: 'mech.avg_speed', unit: 'raw', digits: 0, higherBetter: true },
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
    { key: 'avg_speed', lk: 'hits.avg_speed', unit: 'raw', digits: 0, higherBetter: true },
    { key: 'max_speed', lk: 'hits.max_speed', unit: 'raw', digits: 0, higherBetter: true }
  ];
  function statLabel(s) { return tr(s.lk); }
  /** Value of stat s for match m from source 'me' | 'movement' | 'hits' (converted, null when missing). */
  function statValue(m, src, s) {
    var o = m ? m[src] : null;
    if (src === 'movement' && s.split && !wallKnown(o)) return null;
    var raw = o ? o[s.key] : null;
    if (!isNum(raw)) return null;
    return s.conv ? s.conv(raw) : raw;
  }
  /** Averages of my previous (up to maxN) decided matches of the same mode & variant (and duel or not), strictly
   *  before match `id` in chronological order (all = annotate() output). Each average carries its own sample size n. */
  function matchBaseline(all, id, maxN) {
    maxN = maxN || 50;
    var i = indexOfId(all, id);
    if (i < 0) return null;
    var m = all[i];
    return baselineOf(all.slice(0, i), m.mode, m.variant, maxN, isDuel(m));
  }
  /** Averages of the last decided matches in this mode/variant/duel cohort. Optional paceRegulation normalizes counts only. */
  function baselineOf(ms, mode, variant, maxN, duel, paceRegulation) {
    var prev = [];
    variant = variant || 'Soccar';
    for (var j = ms.length - 1; j >= 0 && prev.length < maxN; j--) {
      var x = ms[j];
      if (x && isDecided(x) && x.mode === mode && (x.variant || 'Soccar') === variant && isDuel(x) === !!duel) prev.push(x);
    }
    function avgs(src, list) {
      var o = {};
      list.forEach(function (s) {
        var unit = s.unit === 'raw' ? commonSpeedUnit(prev) : s.unit;
        var vals = s.unit === 'raw' && !unit ? [] : prev.map(function (x) {
          var v = statValue(x, src, s);
          return src === 'me' && paceRegulation ? livePace(v, x.duration_s, false, paceRegulation) : v;
        }).filter(isNum);
        o[s.key] = { avg: mean(vals), n: vals.length, unit: unit };
      });
      return o;
    }
    return {
      n: prev.length, mode: mode, duel: !!duel,
      me: avgs('me', PERF_STATS), movement: avgs('movement', MOVE_STATS), hits: avgs('hits', HIT_STATS)
    };
  }

  /* ---------- live match (pure) ---------- */
  var LIVE_DIFF_CAP = 3; // goal differences of 3 or more are grouped
  /** Live-score comparisons require known teams/times and exactly the recorded final score.
   *  Other result statistics can still use matches whose timeline is unqualified. */
  function liveTimelineComplete(m) {
    if (m.partial || !Array.isArray(m.goals) || !isNum(m.duration_s) || m.duration_s <= 0 ||
        !Number.isInteger(m.team_score) || m.team_score < 0 || !Number.isInteger(m.opp_score) || m.opp_score < 0 ||
        m.goals.length !== m.team_score + m.opp_score) return false;
    var us = 0, them = 0;
    var valid = m.goals.every(function (g) {
      if (!g || !isNum(g.t) || g.t < 0 || g.t > m.duration_s) return false;
      if (g.team === 'us') us++; else if (g.team === 'them') them++; else return false;
      return true;
    });
    return valid && us === m.team_score && them === m.opp_score;
  }
  var ODDS_Z = 1.645;    // 90 % Wilson interval around a live win rate
  /** Wilson score interval of w wins out of n: {lo, hi} (null without a match). */
  function wilson(w, n, z) {
    if (!(n > 0)) return null;
    z = z || ODDS_Z;
    var p = w / n, z2 = z * z, d = 1 + z2 / n;
    var c = (p + z2 / (2 * n)) / d, h = z * Math.sqrt(p * (1 - p) / n + z2 / (4 * n * n)) / d;
    return { lo: Math.max(0, c - h), hi: Math.min(1, c + h) };
  }
  /**
   * How my online matches of `mode` went from the same situation as the live one: the same goal difference
   * (capped) after `elapsed` game seconds, or the overtimes. Only matches with their complete goal list that
   * lasted that long, or were forfeited before: a forfeit freezes the score until the end, so leaving them out
   * would drop the losses (or wins) of the teams that gave up. Honor Duels (duel) are only compared with duels.
   * {wins, losses, n, wr, ci, overtime}.
   */
  function liveOdds(all, mode, elapsed, diff, overtime, duel) {
    var ms = (all || []).filter(function (m) {
      return m.online !== false && isDecided(m) && m.mode === mode && (m.variant || 'Soccar') === 'Soccar' && isDuel(m) === !!duel && liveTimelineComplete(m);
    });
    var withCi = function (r, ot) { return Object.assign(r, { ci: wilson(r.wins, r.n), overtime: ot }); };
    if (overtime) return withCi(wl(ms.filter(function (m) { return m.overtime; })), true);
    var cap = function (d) { return Math.max(-LIVE_DIFF_CAP, Math.min(LIVE_DIFF_CAP, d)); };
    var same = ms.filter(function (m) {
      if (!goalsComplete(m) || (num(m.duration_s) < elapsed && !m.forfeit)) return false;
      var d = 0;
      sortedGoalsOf(m).forEach(function (g) {
        if (num(g.t) <= elapsed && !isOtGoal(m, g)) d += g.team === 'us' ? 1 : g.team === 'them' ? -1 : 0;
      });
      return cap(d) === cap(diff);
    });
    return withCi(wl(same), false);
  }
  /**
   * The session going on at `at` (its last match ended at most SESSION_GAP_MS before): matches, record, streak,
   * record in `mode`, and how I do from the next match number on in my sessions (fatigue). null without one.
   */
  function currentSession(all, at, mode) {
    all = all || [];
    var last = all[all.length - 1];
    if (!last || at - endMs(last) > SESSION_GAP_MS || at < startMs(last)) return null;
    var ms = all.filter(function (m) { return m._sess === last._sess; });
    var next = ms.length + 1;
    var later = wl(all.filter(function (m) { return m.online !== false && (m._sessIdx || 1) >= next; }));
    return {
      start: ms[0]._t != null ? ms[0]._t : startMs(ms[0]), matches: ms, n: ms.length, record: wl(ms),
      streak: streaks(ms).current, mode: wl(ms.filter(function (m) { return m.mode === mode; })),
      next: next, later: later, overall: wl(all.filter(function (m) { return m.online !== false; }))
    };
  }
  /** Live snapshot (/api/status live) as a match object for the match-page widgets: goals and players from my side. */
  function liveAsMatch(Lv) {
    var me = Lv.me || null, myTeam = Lv.my_team;
    var reg = regulationS(Lv), el = num(Lv.elapsed_s);
    var players = (Lv.players || []).map(function (p) { return Object.assign({}, p); });
    if (me) players.unshift(Object.assign({ team: myTeam, is_me: true }, me));
    var goals = (Lv.goals || []).map(function (g) {
      var mine = !!me && g.scorer === me.name && g.team === myTeam;
      return {
        t: num(g.t), team: g.team === myTeam ? 'us' : g.team >= 0 ? 'them' : '', scorer: g.scorer, assister: g.assister, speed: g.speed,
        overtime: !!g.overtime, me_scored: mine, me_assist: !!me && !mine && g.assister === me.name && g.team === myTeam
      };
    });
    return {
      mode: Lv.mode, arena: Lv.arena, my_team: myTeam, regulation_s: reg, team_score: num(Lv.team_score), opp_score: num(Lv.opp_score),
      overtime: !!Lv.overtime, overtime_s: Lv.overtime ? Math.max(0, el - reg) : 0, duration_s: el, partial: !!Lv.partial,
      speed_unit: Lv.speed_unit || 'unknown',
      online: true, me: me, players: players, goals: goals, movement: me && me.movement ? me.movement : null,
      // null from an agent that does not send the timed shots and saves yet (unknown, not none).
      events: Array.isArray(Lv.events) ? Lv.events.map(function (e) {
        return { t: num(e.t), kind: e.kind, team: e.team === myTeam ? 'us' : e.team >= 0 ? 'them' : '', me: !!e.me };
      }) : null
    };
  }
  /* ---------- live momentum: the last minutes against the rest of the match ---------- */
  var MOMENTUM_WIN_S = 90;   // the recent window (game seconds)
  var MOMENTUM_MIN_EXP = 2;  // shots expected in the window at the earlier pace before a change is stated
  /**
   * Shots, saves and goals of each team in the last `win` game seconds against the earlier part of the match, with
   * per-minute rates, conversion (goals per shot), shots neither scored nor saved, and the shots of the last minute.
   * signals: changes clear enough to state (the earlier pace must predict MOMENTUM_MIN_EXP shots in the window).
   * null without timed events (older agent) or before the first minute.
   */
  function computeMomentum(m, elapsed, win) {
    win = win || MOMENTUM_WIN_S;
    if (!m || !Array.isArray(m.events) || !(elapsed >= 60)) return null;
    var cut = Math.max(0, elapsed - win);
    function side(team, from, to) {
      var inRange = function (t) { return (from === 0 ? t >= 0 : t > from) && t <= to; };
      var o = { shots: 0, saves: 0, goals: 0, oppSaves: 0, minutes: (to - from) / 60 };
      m.events.forEach(function (e) {
        if (!inRange(e.t)) return;
        if (e.team === team) { if (e.kind === 'shot') o.shots++; else if (e.kind === 'save') o.saves++; }
        else if (e.team && e.kind === 'save') o.oppSaves++;
      });
      (m.goals || []).forEach(function (g) { if (g && g.team === team && inRange(num(g.t))) o.goals++; });
      o.shotsPerMin = o.minutes > 0 ? o.shots / o.minutes : null;
      o.conversion = ratio(o.goals, o.shots);
      o.unconverted = Math.max(0, o.shots - o.goals - o.oppSaves); // on target per the game, but neither in nor saved
      return o;
    }
    function team(t) {
      var recent = side(t, cut, elapsed), before = cut > 0 ? side(t, 0, cut) : null;
      var lastMin = side(t, Math.max(0, elapsed - 60), elapsed).shots;
      var expected = before && before.minutes > 0 ? before.shots / before.minutes * recent.minutes : null;
      return { recent: recent, before: before, lastMin: lastMin, expected: expected };
    }
    var us = team('us'), them = team('them');
    var signals = [];
    // Only once the earlier part is as long as the window: a pace needs time to be one.
    if (cut >= win) {
      [['us', us], ['them', them]].forEach(function (x) {
        var k = x[0], s = x[1], e = s.expected;
        if (e == null) return;
        if (e >= MOMENTUM_MIN_EXP && s.recent.shots <= e / 2) signals.push({ key: k + 'Fading', shots: s.recent.shots, expected: e });
        else if (s.recent.shots >= Math.max(3, 2 * e)) signals.push({ key: k + 'Rising', shots: s.recent.shots, expected: e });
        // As many shots as before but no goal from them, while they used to score.
        if (s.recent.shots >= Math.max(MOMENTUM_MIN_EXP, 0.8 * e) && s.recent.goals === 0 && s.before.goals > 0) {
          signals.push({ key: k + 'Blunted', shots: s.recent.shots, saves: s.recent.oppSaves });
        }
        // Scoring more of their shots than before (at least 2 goals in the window).
        if (s.recent.goals >= 2 && isNum(s.before.conversion) && s.recent.conversion > s.before.conversion) {
          signals.push({ key: k + 'Sharper', conv: s.recent.conversion, before: s.before.conversion });
        }
      });
    }
    return { win: win, elapsed: elapsed, us: us, them: them, signals: signals };
  }
  /** A count after `elapsed` game seconds projected to a regulation match (null in overtime or before a minute). */
  function livePace(v, elapsed, overtime, regulation) {
    return isNum(v) && !overtime && elapsed >= 60 ? v * (regulation || DEFAULT_REG_S) / elapsed : null;
  }

  /* ---------- on the field: touches, goal placement, demolitions (matches recorded since v0.16) ---------- */
  // Positions are from my team's side: my goal at y = -5120, the opponents' at +5120.
  var FIELD = { x: 4096, y: 5120, z: 2044, goalX: 893, goalZ: 642 };
  var THIRD_Y = FIELD.y / 3;
  var AERIAL_Z = 300;       // the ball rests at z = 93: above 300 at the touch, it is an aerial touch
  // Preserve the historical raw threshold for analytics consumers, but do not display it as physical power.
  var POWER_RAW_THRESHOLD = 2777.777777777778;
  var MAP_COLS = 6, MAP_ROWS = 8;
  function hitPoints(m) { return m && m.hits && Array.isArray(m.hits.points) ? m.hits.points : []; }
  function share(n, d) { return d > 0 ? n / d : null; }

  function computeTouches(ms) {
    var cells = [], i;
    for (i = 0; i < MAP_COLS * MAP_ROWS; i++) cells.push(0);
    var out = { matches: 0, n: 0, def: 0, mid: 0, off: 0, aerial: 0, powerful: 0, gain: [], post: [], cells: cells, max: 0,
      offWin: { n: 0, off: 0 }, offLoss: { n: 0, off: 0 } };
    ms.forEach(function (m) {
      var pts = hitPoints(m);
      if (!pts.length) return;
      out.matches++;
      var bucket = m.result === 'win' ? out.offWin : m.result === 'loss' ? out.offLoss : null;
      pts.forEach(function (p) {
        if (!p || !isNum(p.y)) return;
        out.n++;
        var y = p.y, x = num(p.x);
        if (y < -THIRD_Y) out.def++; else if (y > THIRD_Y) out.off++; else out.mid++;
        if (bucket) { bucket.n++; if (y > THIRD_Y) bucket.off++; }
        if (num(p.z) > AERIAL_Z) out.aerial++;
        if (isNum(p.post)) {
          out.post.push(p.post);
          if (p.post >= POWER_RAW_THRESHOLD) out.powerful++;
          if (isNum(p.pre)) out.gain.push(p.post - p.pre);
        }
        var c = Math.max(0, Math.min(MAP_COLS - 1, Math.floor((x + FIELD.x) / (2 * FIELD.x) * MAP_COLS)));
        var r = Math.max(0, Math.min(MAP_ROWS - 1, Math.floor((y + FIELD.y) / (2 * FIELD.y) * MAP_ROWS)));
        cells[r * MAP_COLS + c]++;
      });
    });
    out.max = Math.max.apply(null, cells);
    out.defShare = share(out.def, out.n); out.midShare = share(out.mid, out.n); out.offShare = share(out.off, out.n);
    out.aerialShare = share(out.aerial, out.n);
    out.powerShare = share(out.powerful, out.post.length);
    out.speedUnit = commonSpeedUnit(ms);
    out.avgGain = out.speedUnit ? mean(out.gain) : null;
    out.avgPost = out.speedUnit ? mean(out.post) : null;
    out.offWin.share = share(out.offWin.off, out.offWin.n);
    out.offLoss.share = share(out.offLoss.off, out.offLoss.n);
    out.perMatch = share(out.n, out.matches);
    return out;
  }

  /** Where the goals crossed the line: 3 columns (sides, center) x 2 rows (low, high), scored and conceded. */
  function goalZone(p) {
    var col = p.x < -FIELD.goalX / 3 ? 0 : p.x > FIELD.goalX / 3 ? 2 : 1;
    var row = num(p.z) > FIELD.goalZ / 2 ? 0 : 1; // 0 = high
    return row * 3 + col;
  }
  function computeGoalPlacement(ms) {
    function side() { return { n: 0, zones: [0, 0, 0, 0, 0, 0], points: [], high: 0, corners: 0 }; }
    var out = { scored: side(), conceded: side() };
    ms.forEach(function (m) {
      (m.goals || []).forEach(function (g) {
        if (!g || !g.impact || !isNum(g.impact.x)) return;
        var s = g.team === 'us' ? out.scored : g.team === 'them' ? out.conceded : null;
        if (!s) return;
        var z = goalZone(g.impact);
        s.n++; s.zones[z]++;
        if (z < 3) s.high++;
        if (z % 3 !== 1) s.corners++;
        s.points.push(g.impact);
      });
    });
    [out.scored, out.conceded].forEach(function (s) {
      s.highShare = share(s.high, s.n); s.cornerShare = share(s.corners, s.n);
      s.max = Math.max.apply(null, s.zones);
      s.points = s.points.slice(-150);
    });
    return out;
  }

  function computeDemos(ms) {
    var detailed = ms.filter(function (m) { return Array.isArray(m.demolitions); });
    var by = {}, on = {}, made = 0, taken = 0;
    var often = [], rarely = []; // matches with 2+ demolitions taken, and the others
    detailed.forEach(function (m) {
      var t = 0;
      (m.demolitions || []).forEach(function (d) {
        if (!d || !d.player) return;
        if (d.mine) { made++; on[d.player] = (on[d.player] || 0) + 1; } else { taken++; t++; by[d.player] = (by[d.player] || 0) + 1; }
      });
      if (isDecided(m)) (t >= 2 ? often : rarely).push(m);
    });
    function top(o) {
      var best = null;
      Object.keys(o).forEach(function (k) { if (o[k] >= 2 && (!best || o[k] > best.n)) best = { name: k, n: o[k] }; });
      return best;
    }
    return {
      matches: detailed.length,
      madePer: share(made, detailed.length), takenPer: share(taken, detailed.length),
      made: made, taken: taken,
      nemesis: top(by), victim: top(on),
      often: wl(often), rarely: wl(rarely)
    };
  }

  /** Score at half-time (regulation) and how leads end, from matches with every goal recorded. */
  function computeScoreStates(ms) {
    var ahead = [], level = [], behind = [], led = 0, ledLost = 0, trailed = 0, trailedWon = 0;
    var lateFor = 0, lateAgainst = 0, n = 0;
    ms.forEach(function (m) {
      if (!isDecided(m) || !goalsComplete(m)) return;
      n++;
      var reg = regulationS(m), half = reg / 2, d = 0, dHalf = null, maxD = 0, minD = 0;
      sortedGoalsOf(m).forEach(function (g) {
        if (dHalf == null && num(g.t) > half) dHalf = d;
        if (!isOtGoal(m, g) && num(g.t) >= reg - 60) { if (g.team === 'us') lateFor++; else if (g.team === 'them') lateAgainst++; }
        d += g.team === 'us' ? 1 : g.team === 'them' ? -1 : 0;
        maxD = Math.max(maxD, d); minD = Math.min(minD, d);
      });
      if (dHalf == null) dHalf = d;
      (dHalf > 0 ? ahead : dHalf < 0 ? behind : level).push(m);
      if (maxD > 0) { led++; if (!isWin(m)) ledLost++; }
      if (minD < 0) { trailed++; if (isWin(m)) trailedWon++; }
    });
    return {
      n: n, ahead: wl(ahead), level: wl(level), behind: wl(behind),
      led: led, ledLostShare: share(ledLost, led), trailed: trailed, comebackShare: share(trailedWon, trailed),
      lateFor: lateFor, lateAgainst: lateAgainst
    };
  }
  function sortedGoalsOf(m) {
    return (m.goals || []).filter(Boolean).slice().sort(function (a, b) { return num(a.t) - num(b.t); });
  }

  /* ---------- what wins your matches ---------- */
  // Per-match values compared between wins and losses. Counts are per 5 minutes of play (overtime evens out).
  function per5(m, v) { var d = num(m.duration_s); return isNum(v) && d >= 60 ? v * 300 / d : null; }
  function mv(key) { return function (m) { return mvValue(m, key); }; }
  function touchShare(test) {
    return function (m) {
      var pts = hitPoints(m);
      if (pts.length < 5) return null;
      return 100 * pts.filter(test).length / pts.length;
    };
  }
  function speedRaw(m) { return mv('avg_speed')(m); }
  function hitPowerRaw(m) { return m.hits && isNum(m.hits.avg_speed) ? m.hits.avg_speed : null; }
  // focus: the training that works on it (see FOCUSES). Left out: what follows the score rather than drives it
  // (saves, defensive touches, demolitions taken, time demolished: a team under pressure has more of them).
  var FACTORS = [
    { key: 'zero_boost', lk: 'mech.zero_boost_pct', unit: '%', digits: 1, value: mv('zero_boost_pct'), focus: 'boost' },
    { key: 'avg_boost', lk: 'mech.avg_boost', unit: '', digits: 0, value: mv('avg_boost'), focus: 'boost' },
    { key: 'full_boost', lk: 'mech.full_boost_pct', unit: '%', digits: 1, value: mv('full_boost_pct'), focus: 'boost' },
    { key: 'speed', lk: 'mech.avg_speed', unit: 'raw', digits: 0, value: speedRaw, focus: 'recovery' },
    { key: 'supersonic', lk: 'mech.supersonic_pct', unit: '%', digits: 1, value: mv('supersonic_pct'), focus: 'recovery' },
    { key: 'air', lk: 'mech.air_pct', unit: '%', digits: 1, value: mv('air_pct'), focus: 'aerials' },
    { key: 'wall', lk: 'mech.wall_pct', unit: '%', digits: 1, value: mv('wall_pct'), focus: 'recovery' },
    { key: 'powerslide', lk: 'mech.powerslide_pct', unit: '%', digits: 1, value: mv('powerslide_pct'), focus: 'recovery' },
    { key: 'hit_power', lk: 'mech.hits_avg', unit: 'raw', digits: 0, value: hitPowerRaw, focus: 'power' },
    { key: 'touches', lk: 'wf.touches5', unit: '', digits: 1, value: function (m) { return per5(m, me(m).touches); }, focus: 'dribble' },
    { key: 'shots', lk: 'wf.shots5', unit: '', digits: 1, value: function (m) { return per5(m, me(m).shots); }, focus: 'shooting' },
    { key: 'demos', lk: 'wf.demos5', unit: '', digits: 1, value: function (m) { return per5(m, me(m).demos); }, focus: 'rotation' },
    { key: 'off_touches', lk: 'wf.offTouches', unit: '%', digits: 0, value: touchShare(function (p) { return p.y > THIRD_Y; }), focus: 'rotation' },
    { key: 'aerial_touches', lk: 'wf.aerialTouches', unit: '%', digits: 0, value: touchShare(function (p) { return num(p.z) > AERIAL_Z; }), focus: 'aerials' }
  ];
  var FACTOR_MIN = 10;       // matches needed on each side of the median
  var FACTOR_GROUP_MIN = 6;  // values a mode needs to take part (it is split at its own median)
  var FACTOR_Q = 0.1;        // false discovery rate over the factors tested (Benjamini-Hochberg)
  /** Standard normal CDF (Abramowitz-Stegun 7.1.26, error < 1.5e-7). */
  function normCdf(z) {
    var x = Math.abs(z) / Math.SQRT2, t = 1 / (1 + 0.3275911 * x);
    var erf = 1 - (((((1.061405429 * t - 1.453152027) * t) + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-x * x);
    return z >= 0 ? (1 + erf) / 2 : (1 - erf) / 2;
  }
  /** Indexes of the p-values kept by Benjamini-Hochberg at rate q. */
  function bhKeep(ps, q) {
    var order = ps.map(function (p, i) { return i; }).sort(function (a, b) { return ps[a] - ps[b]; });
    var k = -1;
    order.forEach(function (idx, r) { if (ps[idx] <= (r + 1) / ps.length * q) k = r; });
    return order.slice(0, k + 1);
  }

  /**
   * For each factor: win rate above vs below the player's median, and whether the gap is clear. Each mode (duels
   * apart) is split at its own median and the gap is tested within modes (Cochran-Mantel-Haenszel), so a mode
   * won more often with higher values (touches in 1v1) does not pass for a factor. A gap is clear when it holds
   * among all the factors tested (Benjamini-Hochberg, FACTOR_Q). threshold: the median when a single mode is
   * compared, else null (one median per mode).
   */
  function computeWinFactors(ms) {
    var dec = ms.filter(isDecided);
    var rows = FACTORS.map(function (f) {
      var unit = f.unit === 'raw' ? commonSpeedUnit(dec) : f.unit;
      var groups = {}, order = [];
      dec.forEach(function (m) {
        var v = f.unit === 'raw' && !unit ? null : f.value(m);
        if (!isNum(v)) return;
        var k = modeGroup(m), x = { v: v, w: isWin(m) ? 1 : 0 };
        (groups[k] || (groups[k] = [])).push(x);
        order.push(x);
      });
      var out = { key: f.key, lk: f.lk, unit: f.unit === 'raw' ? unit || 'raw' : f.unit, digits: f.digits, focus: f.focus, n: order.length, enough: false };
      var hi = [], lo = [], used = [], a = 0, e = 0, varSum = 0;
      Object.keys(groups).forEach(function (k) {
        var g = groups[k];
        if (g.length < FACTOR_GROUP_MIN) return;
        var sorted = g.map(function (x) { return x.v; }).sort(function (p, q) { return p - q; });
        var med = sorted[Math.floor(sorted.length / 2)], incl = false;
        var nHi = g.filter(function (x) { return x.v > med; }).length;
        if (nHi * 3 < g.length) { incl = true; nHi = g.filter(function (x) { return x.v >= med; }).length; }
        if (!nHi || nHi === g.length) return;
        g.forEach(function (x) { x.hi = incl ? x.v >= med : x.v > med; (x.hi ? hi : lo).push(x); });
        used.push({ med: med, incl: incl });
        // Cochran-Mantel-Haenszel: wins above the median against what the mode's win rate predicts.
        var n = g.length, n1 = nHi, wins = sum(g.map(function (x) { return x.w; }));
        a += sum(g.filter(function (x) { return x.hi; }).map(function (x) { return x.w; }));
        e += n1 * wins / n;
        if (n > 1) varSum += n1 * (n - n1) * wins * (n - wins) / (n * n * (n - 1));
      });
      if (hi.length < FACTOR_MIN || lo.length < FACTOR_MIN) return out;
      var pHi = sum(hi.map(function (x) { return x.w; })) / hi.length, pLo = sum(lo.map(function (x) { return x.w; })) / lo.length;
      out.enough = true;
      out.modes = used.length;
      out.threshold = used.length === 1 ? used[0].med : null;
      out.inclusive = used.length === 1 && used[0].incl; // "from X" rather than "above X"
      out.hi = { n: hi.length, wr: pHi };
      out.lo = { n: lo.length, wr: pLo };
      out.delta = pHi - pLo;
      out.z = varSum > 0 ? (a - e) / Math.sqrt(varSum) : 0;
      out.p = 2 * (1 - normCdf(Math.abs(out.z)));
      out.better = out.delta > 0 ? 'more' : 'less';
      // Recent form: the last 20 matches with a value, each against its mode's median.
      var recent = order.filter(function (x) { return x.hi != null; }).slice(-ROLL_WINDOW);
      out.recent = used.length === 1 ? mean(recent.map(function (x) { return x.v; })) : null;
      out.recentBadShare = share(recent.filter(function (x) { return out.delta > 0 ? !x.hi : x.hi; }).length, recent.length);
      return out;
    });
    var tested = rows.filter(function (r) { return r.enough; });
    var keep = bhKeep(tested.map(function (r) { return r.p; }), FACTOR_Q);
    tested.forEach(function (r, i) { r.clear = keep.indexOf(i) >= 0 && r.z * r.delta > 0; });
    var clear = tested.filter(function (r) { return r.clear; })
      .sort(function (a, b) { return Math.abs(b.delta) - Math.abs(a.delta); });
    return { decided: dec.length, rows: rows, clear: clear, tested: tested.length };
  }

  /* ---------- what to train ---------- */
  var FOCUSES = ['kickoff', 'shooting', 'saves', 'aerials', 'power', 'boost', 'dribble', 'recovery', 'rotation', 'freeplay'];
  /**
   * Mechanics worth training, strongest first: clear win factors where the recent matches are on the bad side,
   * lost kickoffs, a low shot conversion, conceded goals going in the same zone. Always ends with freeplay.
   */
  function computeFocusSuggestions(ms) {
    var out = [], seen = {};
    function add(focus, score, reason, params) {
      if (seen[focus]) { if (score > seen[focus].score) { seen[focus].score = score; seen[focus].reason = reason; seen[focus].params = params; } return; }
      seen[focus] = { focus: focus, score: score, reason: reason, params: params || {} };
      out.push(seen[focus]);
    }
    var wf = computeWinFactors(ms);
    wf.clear.forEach(function (r) {
      if (r.recentBadShare == null || r.recentBadShare < 0.4) return;
      add(r.focus, Math.abs(r.delta) * r.recentBadShare * 100, 'factor', { factor: r });
    });
    var ko = computeSituations(ms).kickoff;
    if (ko.kickoffs >= 30 && ko.lost > ko.won && isNum(ko.lostShare) && ko.lostShare >= 0.06) {
      add('kickoff', (ko.lostShare - (ko.wonShare || 0)) * 200, 'kickoff', { lost: ko.lostShare, won: ko.wonShare });
    }
    var k = computeKpis(ms);
    if (k.shots >= 30 && isNum(k.conversion) && k.conversion < 0.3) add('shooting', (0.3 - k.conversion) * 100, 'conversion', { v: k.conversion });
    var gp = computeGoalPlacement(ms).conceded;
    if (gp.n >= 15 && isNum(gp.highShare) && gp.highShare >= 0.45) add('saves', gp.highShare * 10, 'high', { v: gp.highShare });
    out.sort(function (a, b) { return b.score - a.score; });
    out = out.slice(0, 3);
    if (!seen.freeplay || out.indexOf(seen.freeplay) < 0) out.push({ focus: 'freeplay', score: 0, reason: 'base', params: {} });
    return out;
  }

  /* ---------- stats through a session ---------- */
  var SESSION_BUCKETS = [[1, 2], [3, 4], [5, 6], [7, Infinity]];
  var FATIGUE_STATS = [
    { key: 'winrate', lk: 'kpi.winrate', unit: '%', digits: 0, higherBetter: true, value: function (m) { return isDecided(m) ? (isWin(m) ? 100 : 0) : null; } },
    { key: 'touches', lk: 'wf.touches5', unit: '', digits: 1, higherBetter: true, value: function (m) { return per5(m, me(m).touches); } },
    { key: 'speed', lk: 'mech.avg_speed', unit: 'raw', digits: 0, higherBetter: true, value: speedRaw },
    { key: 'zero_boost', lk: 'mech.zero_boost_pct', unit: '%', digits: 1, higherBetter: false, value: mv('zero_boost_pct') },
    { key: 'hit_power', lk: 'mech.hits_avg', unit: 'raw', digits: 0, higherBetter: true, value: hitPowerRaw },
    { key: 'goal_diff', lk: 'prog.goalDiff', unit: '', digits: 2, higherBetter: true, value: function (m) { return isDecided(m) && isNum(m.goal_diff) ? m.goal_diff : null; } }
  ];
  function computeSessionFatigue(ms) {
    var rows = FATIGUE_STATS.map(function (s) {
      var unit = s.unit === 'raw' ? commonSpeedUnit(ms) : s.unit;
      var value = function (m) { return s.unit === 'raw' && !unit ? null : s.value(m); };
      // Gaps on the deviations from each mode's mean: late in a session is often another mode.
      var devs = modeDeviations(ms, value);
      var cells = SESSION_BUCKETS.map(function (b) {
        var inB = function (m) { var i = m._sessIdx || 1; return i >= b[0] && i <= b[1]; };
        var vals = ms.filter(inB).map(value).filter(isNum);
        return { avg: mean(vals), n: vals.length, dev: mean(devs.filter(function (d, i) { return inB(ms[i]); })) };
      });
      var base = cells[0];
      cells.forEach(function (c, i) { c.delta = i && isNum(c.dev) && isNum(base.dev) && c.n >= 5 && base.n >= 5 ? c.dev - base.dev : null; });
      return { key: s.key, lk: s.lk, unit: s.unit === 'raw' ? unit || 'raw' : s.unit, digits: s.digits, higherBetter: s.higherBetter, cells: cells };
    });
    return { buckets: SESSION_BUCKETS, rows: rows };
  }

  /* ---------- training (pure) ---------- */
  var WARM_GAP_MS = 60 * 60 * 1000; // a training ending at most this long before a session's first match warms it up
  var WARM_MIN_S = 5 * 60;          // shorter trainings do not count as a warm-up
  var WARM_MIN_SESSIONS = 5;        // sessions needed on each side to compare
  var WARM_MIN_MATCHES = 10;        // decided first-three matches needed on each side
  var TRAINING_MAX_MS = 12 * 60 * 60 * 1000, TRAINING_MAX_INTERVALS = 4096;
  function utcSecond(ms) { return Math.floor(ms / 1000) * 1000; }
  function trainingStart(t) { var v = Date.parse(t.started_at); return isNum(v) ? utcSecond(v) : 0; }
  function trainingEnd(t) { var v = Date.parse(t.ended_at); return isNum(v) ? utcSecond(v) : trainingStart(t); }
  /** Match Go's UTC second endpoints before union, display or threshold decisions. */
  function canonicalTraining(t) {
    var start = Date.parse(t.started_at), end = Date.parse(t.ended_at);
    if (!isNum(start) || !isNum(end) || end < start) return null;
    var out = Object.assign({}, t, { started_at: new Date(utcSecond(start)).toISOString(), ended_at: new Date(utcSecond(end)).toISOString() });
    if (t.active_intervals == null) return out;
    if (!Array.isArray(t.active_intervals)) return null;
    var intervals = [], invalid = false;
    t.active_intervals.forEach(function (v) {
      var a = v && Date.parse(v.started_at), b = v && Date.parse(v.ended_at);
      if (!isNum(a) || !isNum(b) || a < start || b > end || b < a) { invalid = true; return; }
      a = utcSecond(a); b = utcSecond(b);
      if (b > a) intervals.push([a, b]);
    });
    if (invalid) return null;
    intervals.sort(function (a, b) { return a[0] - b[0]; });
    var union = [];
    intervals.forEach(function (v) {
      var last = union[union.length - 1];
      if (last && v[0] <= last[1]) last[1] = Math.max(last[1], v[1]);
      else union.push(v.slice());
    });
    out.active_intervals = union.map(function (v) { return { started_at: new Date(v[0]).toISOString(), ended_at: new Date(v[1]).toISOString() }; });
    return out;
  }
  function trainingSeconds(t) {
    var c = canonicalTraining(t);
    if (!c) return 0;
    if (c.active_intervals == null) return Math.max(0, (trainingEnd(c) - trainingStart(c)) / 1000);
    return sum(c.active_intervals.map(function (v) { return (Date.parse(v.ended_at) - Date.parse(v.started_at)) / 1000; }));
  }
  function trainingLimit(t) {
    if (!t || !isNum(Date.parse(t.started_at)) || !isNum(Date.parse(t.ended_at)) || Date.parse(t.ended_at) < Date.parse(t.started_at)) return 'trn.invalidTimer';
    if (Date.parse(t.ended_at) - Date.parse(t.started_at) > TRAINING_MAX_MS) return 'trn.limitTime';
    if (Array.isArray(t.active_intervals) && t.active_intervals.length > TRAINING_MAX_INTERVALS) return 'trn.limitIntervals';
    if (!canonicalTraining(t)) return 'trn.invalidTimer';
    return '';
  }
  function planMinutes(blocks) { return sum((blocks || []).map(function (b) { return b.minutes; })); }

  /**
   * A warm-up of `minutes` built from the suggested mechanics: car control first (about a fifth), then the
   * first suggestion (60 % of the rest) and the second one.
   */
  function buildWarmupPlan(suggestions, minutes) {
    minutes = Math.max(5, Math.min(60, Math.round(num(minutes) || 15)));
    var foci = (suggestions || []).map(function (s) { return s.focus; }).filter(function (f) { return f !== 'freeplay'; }).slice(0, 2);
    if (!foci.length) foci = ['shooting', 'aerials'];
    var fp = Math.max(2, Math.round(minutes * 0.2)), rest = minutes - fp;
    var blocks = [{ focus: 'freeplay', minutes: fp }];
    if (foci.length === 1) blocks.push({ focus: foci[0], minutes: rest });
    else {
      var a = Math.max(1, Math.round(rest * 0.6));
      blocks.push({ focus: foci[0], minutes: a }, { focus: foci[1], minutes: Math.max(1, rest - a) });
    }
    return blocks;
  }

  /** Sessions (annotate()d matches) with their first match time, matches and whether a training warmed them up. */
  function sessionsWithWarmup(ms, trainings) {
    var ts = (trainings || []).filter(function (t) { return trainingSeconds(t) >= WARM_MIN_S; });
    var by = {}, list = [];
    ms.forEach(function (m) {
      var s = by[m._sess];
      if (!s) { s = by[m._sess] = { start: m._t, matches: [] }; list.push(s); }
      s.matches.push(m);
      s.start = Math.min(s.start, m._t);
    });
    list.forEach(function (s) {
      s.warm = ts.find(function (t) { var e = trainingEnd(t); return e <= s.start + 2 * 60000 && e >= s.start - WARM_GAP_MS; }) || null;
    });
    return list;
  }

  /** Win rate and form of sessions started after a warm-up vs without one. */
  function computeWarmupEffect(ms, trainings) {
    function side() { return { sessions: 0, first: [], all: [], touches: [] }; }
    var warm = side(), cold = side();
    sessionsWithWarmup(ms, trainings).forEach(function (s) {
      var dec = s.matches.filter(isDecided);
      if (!dec.length) return;
      var o = s.warm ? warm : cold;
      o.sessions++;
      dec.forEach(function (m) {
        o.all.push(m);
        if ((m._sessIdx || 1) <= 3) {
          o.first.push(m);
          var v = per5(m, me(m).touches);
          if (isNum(v)) o.touches.push(v);
        }
      });
    });
    function done(o) { return { sessions: o.sessions, first: wl(o.first), all: wl(o.all), touches: mean(o.touches) }; }
    var w = done(warm), c = done(cold);
    var enough = w.sessions >= WARM_MIN_SESSIONS && c.sessions >= WARM_MIN_SESSIONS && w.first.n >= WARM_MIN_MATCHES && c.first.n >= WARM_MIN_MATCHES;
    return { warm: w, cold: c, enough: enough, delta: enough ? w.first.wr - c.first.wr : null };
  }

  function warmupEffectMessage(e) {
    if (!e.enough) return (!e.warm.sessions ? tr('trn.effectNone') + ' ' : '') + tr('trn.effectMore', {
      sessions: WARM_MIN_SESSIONS, matches: WARM_MIN_MATCHES,
      warmSessions: e.warm.sessions, coldSessions: e.cold.sessions,
      warmMatches: e.warm.first.n, coldMatches: e.cold.first.n
    });
    var d = e.delta * 100;
    return d >= 5 ? tr('trn.effectGood', { v: fmtNum(d) }) : d <= -5 ? tr('trn.effectBad', { v: fmtNum(-d) }) : tr('trn.effectFlat');
  }

  /** Training time over the last 7 and 30 days, against the time played, and per mechanic. */
  function computeTrainingSummary(trainings, ms, now) {
    now = isNum(now) ? now : Date.now();
    var d7 = now - 7 * DAY_MS, d30 = now - 30 * DAY_MS, byFocus = {}, days = {};
    var s7 = 0, s30 = 0, n30 = 0;
    (trainings || []).forEach(function (t) {
      var st = trainingStart(t), sec = trainingSeconds(t);
      if (st >= d7) s7 += sec;
      if (st >= d30) {
        s30 += sec; n30++;
        var f = t.focus || '';
        byFocus[f] = (byFocus[f] || 0) + sec;
        days[dayKey(st)] = 1;
      }
    });
    var played = sum(ms.filter(function (m) { return (m._t || startMs(m)) >= d30; }).map(function (m) { return num(m.duration_s) * 1.15; }));
    var foci = Object.keys(byFocus).map(function (f) { return { focus: f, seconds: byFocus[f] }; }).sort(function (a, b) { return b.seconds - a.seconds; });
    return { week: s7, month: s30, count: n30, days: Object.keys(days).length, played: played, ratio: share(s30, s30 + played), foci: foci };
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

  /** Queue counts describe local intent, never acknowledgement by a remote destination. */
  function deliveryMessage(delivery) {
    var keys = ['pending', 'held', 'rejected', 'legacy_held'];
    if (!delivery || !keys.every(function (k) { return Number.isSafeInteger(delivery[k]) && delivery[k] >= 0; })) return null;
    var parts = [tr('st.deliveryLocal'), tr('st.deliveryCounts', delivery)];
    if (delivery.pending) parts.push(tr('st.deliveryPending'));
    if (delivery.held || delivery.legacy_held) parts.push(tr('st.deliveryHeld'));
    if (delivery.rejected) parts.push(tr('st.deliveryRejected'));
    return { text: parts.join(' '), attention: !!(delivery.held || delivery.legacy_held || delivery.rejected) };
  }

  /**
   * What a /status reply says about the stored data: 'rev:<data_rev>' when the server sends a
   * revision (changes on any write), else the count under countKey (older server or agent), else null.
   */
  function dataMarker(s, countKey) {
    if (!s) return null;
    if (s.data_rev != null && s.data_rev !== '') return 'rev:' + s.data_rev;
    return isNum(s[countKey]) ? s[countKey] : null;
  }
  /** True when the data observed (dataMarker) is not the one last loaded successfully (null: never). */
  function needsReload(observed, loaded) { return observed != null && observed !== loaded; }
  /** 'v0.19.0' and '0.19.0' -> '0.19.0'. */
  function stripV(v) { return String(v || '').replace(/^v/, ''); }

  var api = {
    deliveryMessage: deliveryMessage, dataMarker: dataMarker, needsReload: needsReload, stripV: stripV,
    ROLL_WINDOW: ROLL_WINDOW, SESSION_GAP_MS: SESSION_GAP_MS, TAGS: TAGS, tagLabel: tagLabel, plural: plural,
    DEFAULT_FILTERS: DEFAULT_FILTERS, MECH_METRICS: MECH_METRICS,
    isNum: isNum, ratio: ratio, regulationS: regulationS, isDuel: isDuel, wallKnown: wallKnown, modeGroup: modeGroup, modeDeviations: modeDeviations, mean: mean, wl: wl, formatRawSpeed: formatRawSpeed, speedUnit: speedUnit, annotate: annotate, modeKey: modeKey, filterMatches: filterMatches,
    streaks: streaks, computeKpis: computeKpis, rolling: rolling, computeProgression: computeProgression,
    computeActivity: computeActivity, computeGoalDiffDist: computeGoalDiffDist, computeMental: computeMental,
    computeSituations: computeSituations, kickoffGoals: kickoffGoals, computeGoalsPerMinute: computeGoalsPerMinute, computeMechanics: computeMechanics,
    computeStatfeed: computeStatfeed, statfeedLabel: statfeedLabel, prettyArena: prettyArena, computeArenas: computeArenas,
    computeTeammates: computeTeammates, computeGoal: computeGoal, computeTimeInsights: computeTimeInsights, trackedByMode: trackedByMode, trackerUrl: trackerUrl, fmtNum: fmtNum, fmtPct: fmtPct, fmtPct100: fmtPct100, fmtSigned: fmtSigned,
    fmtClock: fmtClock, fmtHours: fmtHours, dayKey: dayKey,
    parseRoute: parseRoute, routeHash: routeHash, filterPlayers: filterPlayers, rankLeaders: rankLeaders, DEFAULT_HIST_FILTERS: DEFAULT_HIST_FILTERS, filterHistory: filterHistory, filterManualHistory: filterManualHistory,
    groupHistory: groupHistory, paginateGroups: paginateGroups, summarizeHistory: summarizeHistory, matchNeighbors: matchNeighbors,
    matchContext: matchContext, matchBaseline: matchBaseline, baselineOf: baselineOf, goalsComplete: goalsComplete, statValue: statValue, PERF_STATS: PERF_STATS, MOVE_STATS: MOVE_STATS, HIT_STATS: HIT_STATS,
    computeTouches: computeTouches, computeGoalPlacement: computeGoalPlacement, computeDemos: computeDemos, computeScoreStates: computeScoreStates,
    computeWinFactors: computeWinFactors, computeFocusSuggestions: computeFocusSuggestions, computeSessionFatigue: computeSessionFatigue,
    FACTORS: FACTORS, FOCUSES: FOCUSES, FATIGUE_STATS: FATIGUE_STATS,
    liveOdds: liveOdds, wilson: wilson, computeMomentum: computeMomentum, currentSession: currentSession, liveAsMatch: liveAsMatch, livePace: livePace,
    canonicalTraining: canonicalTraining, trainingLimit: trainingLimit, trainingSeconds: trainingSeconds, buildWarmupPlan: buildWarmupPlan, sessionsWithWarmup: sessionsWithWarmup, computeWarmupEffect: computeWarmupEffect, warmupEffectMessage: warmupEffectMessage, computeTrainingSummary: computeTrainingSummary
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
    shared: {},       // server mode: match id -> other accounts that recorded it
    lastShared: null,
    sharedBusy: 0,
    sharedFailed: false,
    ranks: [],        // rank / MMR entries of the viewed player, oldest first
    lastRanks: null,
    ranksBusy: 0,
    ranksFailed: false,
    allRaw: [],       // every match of the viewed player, all accounts (state.all = the shown profile's, annotated)
    profiles: [],     // the viewed player's Rocket League accounts, most played first
    profile: 'all',   // the account shown: a profileKey or 'all'
    rankPl: null,     // playlist shown in the MMR chart
    sharedCopies: {}, // apiBase/id -> their records of that match (match page)
    filters: loadFilters(),
    status: null,
    statusError: false,
    wasLive: false, // a match of mine was going on at the last status (opens the in-game view at the next start)
    lastCount: null,    // status marker of the matches loaded (see dataMarker), null: none / last load failed
    matchesBusy: 0,     // match loads running
    matchesFailed: false, // the last match load failed (its toast is shown once)
    shown: PAGE_SIZE,
    open: {},
    loaded: false,
    config: null,
    manual: [],
    lastManual: null,
    manualBusy: 0,
    manualFailed: false,
    trainings: [],      // trainings of the viewed player, oldest first
    lastTrainings: null,
    trainingsBusy: 0,
    timer: null,        // the warm-up running (see startTimer), kept in localStorage
    planDraft: null,    // warm-up plan being edited, not saved yet
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
            var error = new Error('HTTP ' + r.status + (detail ? ' : ' + detail : ''));
            error.status = r.status;
            throw error;
          });
        }
        if (r.status === 204) return null;
        var ct = r.headers.get('content-type') || '';
        return ct.indexOf('json') >= 0 ? r.json() : r.text();
      });
  }

  /** Demo trainings: a warm-up before about half of the sessions (more often before the good ones). */
  function mockTrainings(d) {
    var out = [], prevEnd = 0, foci = ['shooting', 'aerials', 'kickoff', 'boost', 'saves', 'freeplay'];
    var ms = annotate(JSON.parse(JSON.stringify(d)));
    ms.forEach(function (m, i) {
      if (m._sessIdx !== 1) return;
      var good = ms.slice(i, i + 3).filter(isWin).length >= 2;
      if (((i * 7919) % 100) / 100 > (good ? 0.7 : 0.35)) return;
      var len = (8 + (i % 4) * 4) * 60000, end = m._t - 3 * 60000;
      if (end - len < prevEnd) return;
      out.push({ id: i + 1, started_at: new Date(end - len).toISOString(), ended_at: new Date(end).toISOString(), source: i % 3 ? 'game' : 'timer',
        focus: foci[i % foci.length], arena: i % 3 ? 'Park_P' : '', touches: i % 3 ? 60 + (i % 50) : 0, avg_hit_speed: 1900, max_hit_speed: 3900 });
      prevEnd = m._t;
    });
    return out;
  }
  /** Demo live match: the most frequent teammate and opponent of the mock history, plus a stranger. */
  function mockLivePlayers() {
    var best = { mate: null, opp: null }, n = {};
    mock.matches.forEach(function (m) {
      (m.players || []).forEach(function (p) {
        if (p.is_me || !p.primary_id) return;
        var role = p.team === m.my_team ? 'mate' : 'opp', k = role + p.primary_id;
        n[k] = (n[k] || 0) + 1;
        if (!best[role] || n[k] > n[role + best[role].primary_id]) best[role] = p;
      });
    });
    var out = [{ name: 'Inconnu', primary_id: 'Steam|76561190000000000|0', team: 1 }];
    if (best.mate) out.unshift({ name: best.mate.name, primary_id: best.mate.primary_id, team: 0 });
    if (best.opp) out.push({ name: best.opp.name, primary_id: best.opp.primary_id, team: 1 });
    return out;
  }
  /** Demo live match, replayed every 5 minutes: goals and stats grow with the clock. */
  function mockLive() {
    var el = Math.round(((Date.now() - mock.t0) / 1000 + 117) % 300);
    var players = mockLivePlayers(), mate = players.filter(function (p) { return p.team === 0; })[0], opp = players.filter(function (p) { return p.team === 1; })[0];
    var goals = [
      { t: 38, team: 0, scorer: 'Virgile', assister: mate ? mate.name : '', speed: 104, overtime: false },
      { t: 121, team: 1, scorer: opp ? opp.name : 'Inconnu', assister: '', speed: 87, overtime: false },
      { t: 205, team: 0, scorer: mate ? mate.name : 'Virgile', assister: 'Virgile', speed: 112, overtime: false },
      { t: 262, team: 1, scorer: 'Inconnu', assister: opp ? opp.name : '', speed: 71, overtime: false }
    ].filter(function (g) { return g.t <= el; });
    var k = el / 300, sc = [0, 0];
    goals.forEach(function (g) { sc[g.team]++; });
    var stat = function (s, base) {
      var r = function (x) { return Math.round(x * k); };
      return Object.assign(s, { score: r(base * 2.4), shots: r(base / 70), saves: r(base / 110), touches: r(base / 7), demos: r(base / 160) });
    };
    players.forEach(function (p, i) {
      stat(p, 260 - i * 30);
      p.goals = goals.filter(function (g) { return g.scorer === p.name; }).length;
      p.assists = goals.filter(function (g) { return g.assister === p.name; }).length;
    });
    var me = stat({ name: 'Virgile' }, 300);
    me.goals = goals.filter(function (g) { return g.scorer === 'Virgile'; }).length;
    me.assists = goals.filter(function (g) { return g.assister === 'Virgile'; }).length;
    me.movement = el > 5 ? { sample_s: el, avg_speed: 1450, supersonic_pct: 17.5, ground_pct: 63, wall_pct: 9, air_pct: 28, avg_boost: 41, zero_boost_pct: 11.2, full_boost_pct: 6.4, boosting_pct: 14, powerslide_pct: 3.1, demolished_s: 3 } : null;
    return {
      guid: 'mock-live', mode: '2v2', arena: 'EuroStadium_Night_P', time_seconds: 300 - el, overtime: false, elapsed_s: el,
      team_score: sc[0], opp_score: sc[1], my_team: 0, me: me, players: players, goals: goals,
      // Opponents shooting a lot early then fading; my team pushing late.
      events: [[20, 'shot', 1], [36, 'shot', 0], [38, 'shot', 0, true], [55, 'shot', 1], [56, 'save', 0, true], [80, 'shot', 1], [95, 'shot', 1],
        [96, 'save', 0], [110, 'shot', 1], [121, 'shot', 1], [150, 'shot', 0], [151, 'save', 1], [170, 'shot', 1], [190, 'shot', 0, true],
        [205, 'shot', 0], [222, 'shot', 0], [223, 'save', 1], [240, 'shot', 0, true], [255, 'shot', 1], [262, 'shot', 1], [280, 'shot', 0]]
        .filter(function (e) { return e[0] <= el; }).map(function (e) { return { t: e[0], kind: e[1], team: e[2], me: !!e[3] }; }),
      started_at: new Date(Date.now() - el * 1000 - 20000).toISOString()
    };
  }
  function mockFetch(path, opts) {
    var method = (opts.method || 'GET').toUpperCase();
    var ready = mockReady || (mockReady = (function () {
      mock = { matches: [], config: { player_names: ['Virgile'], player_ids: ['Epic|8f3a21c9|0'], default_tag: 'ranked', rl_install_dir: '', dashboard_port: 8765, rl_port: 49123, goal: { mode: '1v1', daily: 10, season_start: '', season_end: '' }, break_every: 0, break_minutes: 5, warmup: { minutes: 15, blocks: [] } }, trainings: [], t0: Date.now() };
      if (MOCK === 'empty') return Promise.resolve();
      var src = params.get('mockdata') || '../devtools/mock/mock-matches.json';
      return fetch(src, { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) {
        mock.matches = d;
        mock.trainings = mockTrainings(d);
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
      if (path === '/api/matches' && method === 'GET') {
        var copy = JSON.parse(JSON.stringify(mock.matches));
        // Demo of several accounts (?mockalt=1): every 4th match on a second account.
        if (params.get('mockalt')) copy.forEach(function (x, i) { if (i % 4 === 3 && x.me) { x.me.name = 'VirgSmurf'; x.me.primary_id = 'Steam|76561190000999999|0'; } });
        return copy;
      }
      if ((m = path.match(/^\/api\/matches\/(\d+)$/))) {
        var id = +m[1];
        var idx = mock.matches.findIndex(function (x) { return x.id === id; });
        if (idx < 0) throw new Error('HTTP 404');
        if (method === 'DELETE') { mock.matches.splice(idx, 1); return null; }
        if (method === 'PATCH') { Object.assign(mock.matches[idx], JSON.parse(opts.body || '{}')); return mock.matches[idx]; }
      }
      if (/^\/api\/profiles\//.test(path) && method === 'DELETE') return { deleted: 0 };
      if (path === '/api/trainings') {
        if (method === 'POST') { var tb = JSON.parse(opts.body || '{}'); tb.id = Date.now(); tb.source = 'timer'; mock.trainings.push(tb); return tb; }
        return JSON.parse(JSON.stringify(mock.trainings));
      }
      if ((m = path.match(/^\/api\/trainings\/(\d+)$/))) {
        var ti = mock.trainings.findIndex(function (x) { return x.id === +m[1]; });
        if (ti < 0) throw new Error('HTTP 404');
        if (method === 'DELETE') { mock.trainings.splice(ti, 1); return null; }
        Object.assign(mock.trainings[ti], JSON.parse(opts.body || '{}')); return mock.trainings[ti];
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
        var live = ms === 'live' ? mockLive() : null;
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

  /* ---------- profiles: the player's Rocket League accounts ---------- */
  /** Same as store.ProfileKey: the account a match was played on. */
  function profileKey(m) {
    var me = m.me || {};
    var id = String(me.primary_id || '').trim().toLowerCase();
    if (id) return id;
    var n = String(me.name || '').trim().toLowerCase();
    return n ? 'name:' + n : '';
  }
  function platformLabel(key) {
    var p = key.indexOf('name:') === 0 ? '' : key.split('|')[0];
    var names = { epic: 'Epic', steam: 'Steam', ps4: 'PlayStation', ps5: 'PlayStation', psn: 'PlayStation', xboxone: 'Xbox', xbox: 'Xbox', xbl: 'Xbox', switch: 'Switch' };
    return names[p] || '';
  }
  function computeProfiles(ms) {
    var by = {};
    ms.forEach(function (m) {
      var k = profileKey(m);
      var p = by[k] || (by[k] = { key: k, name: '', n: 0, last: 0 });
      p.n++;
      var t = startMs(m);
      if (t >= p.last) { p.last = t; p.name = (m.me && m.me.name) || p.name; }
    });
    return Object.keys(by).map(function (k) { return by[k]; }).sort(function (a, b) { return b.n - a.n || b.last - a.last; });
  }
  /** The main account: the first platform id (or name) of the settings that has matches, else the most played. */
  function mainProfile() {
    var c = !state.dataFor ? state.config : null, keys = state.profiles.map(function (p) { return p.key; });
    var wanted = c ? (c.player_ids || []).map(function (x) { return String(x).trim().toLowerCase(); })
      .concat((c.player_names || []).map(function (x) { return 'name:' + String(x).trim().toLowerCase(); })) : [];
    for (var i = 0; i < wanted.length; i++) if (keys.indexOf(wanted[i]) >= 0) return wanted[i];
    return keys[0] || '';
  }
  function profileStoreKey() { return 'rt.profile.' + (state.dataFor || 'me'); }
  /** Rebuilds the account list after the matches changed and shows the chosen account. */
  function setupProfiles() {
    state.profiles = computeProfiles(state.allRaw);
    var keys = state.profiles.map(function (p) { return p.key; });
    var saved = null;
    try { saved = localStorage.getItem(profileStoreKey()); } catch (e) { /* storage unavailable */ }
    var cur = state.profileFor === (state.dataFor || '') ? state.profile : saved;
    if (cur !== 'all' && keys.indexOf(cur) < 0) cur = state.profiles.length > 1 ? mainProfile() : 'all';
    state.profileFor = state.dataFor || '';
    applyProfile(state.profiles.length > 1 ? cur : 'all');
  }
  function applyProfile(key) {
    state.profile = key;
    state.all = annotate(key === 'all' ? state.allRaw : state.allRaw.filter(function (m) { return profileKey(m) === key; }));
    renderProfileSwitch();
  }
  function selectProfile(key) {
    try { localStorage.setItem(profileStoreKey(), key); } catch (e) { /* storage unavailable */ }
    state.shown = PAGE_SIZE;
    applyProfile(key);
    renderAll();
  }
  function profileName(key) {
    var p = state.profiles.filter(function (x) { return x.key === key; })[0];
    return p ? p.name || key : key;
  }
  function renderProfileSwitch() {
    var box = $('#profile-switch'), sel = $('#profile-sel');
    box.hidden = state.profiles.length < 2;
    // Removing an account that is not one's own (e.g. recorded while spectating); never the main one.
    $('#profile-del').hidden = box.hidden || !!state.dataFor || state.profile === 'all' || state.profile === mainProfile();
    if (box.hidden) return;
    sel.innerHTML = state.profiles.map(function (p) {
      var plat = platformLabel(p.key);
      return '<option value="' + esc(p.key) + '"' + (p.key === state.profile ? ' selected' : '') + '>' + esc((p.name || p.key) + (plat ? ' · ' + plat : '')) +
        ' (' + esc(plural(p.n, 'match')) + ')</option>';
    }).join('') + '<option value="all"' + (state.profile === 'all' ? ' selected' : '') + '>' + esc(tr(state.dataFor ? 'profile.allTheirs' : 'profile.all')) + '</option>';
  }
  function deleteProfile() {
    var key = state.profile, p = state.profiles.filter(function (x) { return x.key === key; })[0];
    if (!p || !window.confirm(tr('profile.confirmDelete', { v: p.name || key, n: plural(p.n, 'match') }))) return;
    apiFetch('/api/profiles/' + encodeURIComponent(key), { method: 'DELETE' }).then(function () {
      state.allRaw = state.allRaw.filter(function (m) { return profileKey(m) !== key; });
      state.ranks = (state.ranks || []).filter(function (r) { return r.profile !== key; });
      if (state.config) {
        state.config.player_ids = (state.config.player_ids || []).filter(function (x) { return String(x).trim().toLowerCase() !== key; });
        state.config.player_names = (state.config.player_names || []).filter(function (x) { return 'name:' + String(x).trim().toLowerCase() !== key; });
      }
      state.lastCount = null;
      state.profile = 'all';
      setupProfiles();
      selectProfile(state.profiles.length > 1 ? mainProfile() : 'all');
      toast(tr('profile.deleted', { v: p.name || key }));
    }).catch(function (e) { toast(tr('toast.deleteFailed', { v: e.message }), true); });
  }
  /** Manual games have no account: they count with the main one (and in "all"). */
  function profileManual() {
    return state.profile === 'all' || state.profiles.length < 2 || state.profile === mainProfile() ? state.manual : [];
  }
  /** Platform id of one of the viewed player's own accounts? (marked in the teammates). */
  function isOwnAccount(pid) {
    var k = String(pid || '').trim().toLowerCase();
    return !!k && state.profiles.some(function (p) { return p.key === k; });
  }
  function rankProfile(r) { return String(r.profile || '').trim().toLowerCase(); }
  function ranksForProfile(rs, key) { return (rs || []).filter(function (r) { return rankProfile(r) === key; }); }
  function rankDestination() { return state.profile === 'all' ? mainProfile() : state.profile; }
  function rankAccountLabel(key) {
    if (!key) return tr('rank.unknownAccount');
    var name = profileName(key);
    return name !== key ? name + ' (' + key + ')' : key;
  }
  /** Unattributed legacy entries stay unknown, never implicitly assigned to the main account. */
  function profileRanks() {
    return state.profile === 'all' ? state.ranks || [] : ranksForProfile(state.ranks, state.profile);
  }

  /* ---------- ranks & MMR (noted by the player) ---------- */
  // tier: 0 unranked, 1-21 Bronze I .. Grand Champion III, 22 Supersonic Legend; division 1-4.
  var PLAYLISTS = ['1v1', '2v2', '3v3', 'hoops', 'rumble', 'dropshot', 'snowday', 'tournament'];
  var RANK_FAMILIES = [
    { k: 'unranked', c1: '#b4bcc6', c2: '#5d6670', shape: 'shield' },
    { k: 'bronze', c1: '#e3a571', c2: '#7a4622', shape: 'shield' },
    { k: 'silver', c1: '#f1f4f8', c2: '#848f9c', shape: 'shield' },
    { k: 'gold', c1: '#ffe28c', c2: '#ad7a12', shape: 'shield' },
    { k: 'platinum', c1: '#b9f4f7', c2: '#2f97a6', shape: 'gem' },
    { k: 'diamond', c1: '#acd0ff', c2: '#2853c4', shape: 'gem' },
    { k: 'champion', c1: '#e0c8ff', c2: '#6630c0', shape: 'hex' },
    { k: 'gc', c1: '#ffa0a0', c2: '#ad1b28', shape: 'crown' },
    { k: 'ssl', c1: '#ffffff', c2: '#9ba7ba', shape: 'star' }
  ];
  var ROMAN = ['I', 'II', 'III', 'IV'];
  function rankFamily(tier) { return tier === 0 ? 0 : tier === 22 ? 8 : Math.ceil(tier / 3); }
  function rankLevel(tier) { return tier >= 1 && tier <= 21 ? (tier - 1) % 3 + 1 : 0; }
  function rankName(tier) {
    if (!isNum(tier)) return '';
    var lv = rankLevel(tier);
    return tr('rank.f.' + RANK_FAMILIES[rankFamily(tier)].k) + (lv ? ' ' + ROMAN[lv - 1] : '');
  }
  function rankFull(r) {
    if (!isNum(r.tier)) return '';
    return rankName(r.tier) + (isNum(r.division) ? ' · ' + tr('rank.div', { v: ROMAN[r.division - 1] }) : '');
  }
  function playlistLabel(p) { return tr('pl.' + p); }
  var rankSvgSeq = 0;
  /** Our own badge for a tier (not Psyonix's emblems): family colour and shape, one chevron per level. */
  function rankIcon(tier, size) {
    size = size || 40;
    if (!isNum(tier)) {
      return '<svg class="rank-icon" width="' + size + '" height="' + size + '" viewBox="0 0 40 40" aria-hidden="true"><circle cx="20" cy="20" r="15" fill="none" stroke="currentColor" stroke-opacity=".35" stroke-width="2" stroke-dasharray="4 3"/></svg>';
    }
    var f = RANK_FAMILIES[rankFamily(tier)], id = 'rkg' + (++rankSvgSeq), lv = rankLevel(tier);
    var shapes = {
      shield: 'M20 3 L34 8 V19 C34 28 27 34 20 37 C13 34 6 28 6 19 V8 Z',
      gem: 'M20 2 L36 15 L20 38 L4 15 Z',
      hex: 'M20 3 L35 11 V29 L20 37 L5 29 V11 Z',
      crown: 'M20 6 L35 13 V29 L20 37 L5 29 V13 Z',
      star: (function () {
        var pts = [];
        for (var i = 0; i < 16; i++) {
          var a = -Math.PI / 2 + i * Math.PI / 8, r = i % 2 ? 9 : 18;
          pts.push((20 + r * Math.cos(a)).toFixed(1) + ' ' + (20 + r * Math.sin(a)).toFixed(1));
        }
        return 'M' + pts.join(' L') + ' Z';
      })()
    };
    var svg = '<svg class="rank-icon" width="' + size + '" height="' + size + '" viewBox="0 0 40 40" role="img" aria-label="' + esc(rankName(tier)) + '">' +
      '<defs><linearGradient id="' + id + '" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="' + f.c1 + '"/><stop offset="1" stop-color="' + f.c2 + '"/></linearGradient></defs>' +
      '<path d="' + shapes[f.shape] + '" fill="url(#' + id + ')" stroke="' + f.c2 + '" stroke-width="1.2" stroke-linejoin="round"/>';
    if (f.shape === 'gem') svg += '<path d="M4 15 H36 M12 15 L20 2 L28 15 M12 15 L20 38 L28 15" fill="none" stroke="#fff" stroke-opacity=".35" stroke-width="1"/>';
    if (f.shape === 'crown') svg += '<path d="M11 9 L13.5 2.5 L17 6 L20 1 L23 6 L26.5 2.5 L29 9 Z" fill="' + f.c1 + '" stroke="' + f.c2 + '" stroke-width="1" stroke-linejoin="round"/>';
    if (f.k === 'unranked') svg += '<path d="M15 21 H25" stroke="#fff" stroke-width="2.6" stroke-linecap="round"/>';
    var ys = lv === 1 ? [23] : lv === 2 ? [20, 26] : lv === 3 ? [17, 23, 29] : [];
    ys.forEach(function (y) {
      svg += '<path d="M14 ' + (y + 2) + ' L20 ' + (y - 3) + ' L26 ' + (y + 2) + '" fill="none" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>';
    });
    return svg + '</svg>';
  }
  function divisionPips(div) {
    if (!isNum(div)) return '';
    var h = '<span class="rank-pips" title="' + esc(tr('rank.div', { v: ROMAN[div - 1] })) + '">';
    for (var i = 1; i <= 4; i++) h += '<i' + (i <= div ? ' class="on"' : '') + '></i>';
    return h + '</span>';
  }
  function annotateRanks(rs) {
    return rs.map(function (r) { r._t = Date.parse(r.at); return r; }).sort(function (a, b) { return a._t - b._t || a.id - b.id; });
  }
  /** Independent history and MMR delta for each account and playlist. */
  function rankSummary(rs) {
    var by = Object.create(null);
    rs.forEach(function (r) {
      var profile = rankProfile(r), key = JSON.stringify([profile, r.playlist]);
      var s = by[key] || (by[key] = { profile: profile, playlist: r.playlist, last: null, tier: null, division: null, mmr: null, prevMmr: null, mmrAt: null, n: 0 });
      s.n++;
      s.last = r;
      if (isNum(r.tier)) { s.tier = r.tier; s.division = isNum(r.division) ? r.division : null; s.tierAt = r._t; }
      if (isNum(r.mmr)) { s.prevMmr = s.mmr; s.mmr = r.mmr; s.mmrAt = r._t; }
    });
    return Object.keys(by).map(function (k) { return by[k]; }).sort(function (a, b) {
      return PLAYLISTS.indexOf(a.playlist) - PLAYLISTS.indexOf(b.playlist) || a.profile.localeCompare(b.profile);
    });
  }
  function renderRanks() {
    var sec = $('#rank-section');
    if (!sec) return;
    var own = !readOnly();
    var rs = profileRanks();
    sec.hidden = !state.loaded || (!rs.length && !own);
    if (sec.hidden) return;
    $('#rank-add').hidden = !own;
    renderRankPrompt();
    var sum = rankSummary(rs);
    $('#rank-cards').innerHTML = sum.length ? sum.map(function (s) {
      var delta = isNum(s.mmr) && isNum(s.prevMmr) ? s.mmr - s.prevMmr : null;
      return '<div class="card rank-card">' + rankIcon(s.tier, 56) +
        '<div class="rank-card-body"><span class="kpi-label">' + esc(playlistLabel(s.playlist)) + ' · ' + esc(rankAccountLabel(s.profile)) + '</span>' +
        '<span class="rank-card-name">' + (isNum(s.tier) ? esc(rankName(s.tier)) + divisionPips(s.division) : '<span class="muted">' + tr('rank.noRank') + '</span>') + '</span>' +
        '<span class="rank-card-mmr">' + (isNum(s.mmr) ? '<b class="num">' + fmtNum(s.mmr) + '</b> MMR' +
          (delta != null ? ' <span class="' + signCls(delta) + '">' + (delta > 0 ? '▲ ' : delta < 0 ? '▼ ' : '') + fmtSigned(delta) + '</span>' : '') : '<span class="muted">' + tr('rank.noMmr') + '</span>') + '</span>' +
        '<span class="kpi-sub">' + tr('rank.notedOn', { v: esc(fmtDate(s.last._t, true)) }) + '</span></div></div>';
    }).join('') : '<div class="card rank-empty"><p>' + tr('rank.empty') + '</p></div>';
    renderMmrChart(sum);
    var log = $('#rank-log'), box = $('#rank-log-box');
    box.hidden = !rs.length;
    log.innerHTML = rs.slice().reverse().map(function (r) {
      return '<li><span class="rank-log-when">' + esc(fmtDate(r._t, true)) + '</span><span class="rank-log-pl">' + esc(playlistLabel(r.playlist)) + ' · ' + esc(rankAccountLabel(rankProfile(r))) + '</span>' +
        '<span class="rank-log-what">' + (isNum(r.tier) ? rankIcon(r.tier, 22) + esc(rankFull(r)) : '') + (isNum(r.mmr) ? (isNum(r.tier) ? ' · ' : '') + fmtNum(r.mmr) + ' MMR' : '') + '</span>' +
        (own ? '<button type="button" class="del-btn" data-rank-del="' + r.id + '" title="' + esc(tr('tbl.delete')) + '" aria-label="' + esc(tr('tbl.delete')) + '">' + TRASH + '</button>' : '') + '</li>';
    }).join('');
  }
  function renderMmrChart(sum) {
    var withMmr = sum.filter(function (s) { return isNum(s.mmr); }).map(function (s) { return s.playlist; }).filter(function (p, i, ps) { return ps.indexOf(p) === i; });
    var card = $('#rank-chart-card');
    card.hidden = !withMmr.length;
    if (!withMmr.length) { if (charts['c-mmr']) { charts['c-mmr'].destroy(); delete charts['c-mmr']; } return; }
    if (withMmr.indexOf(state.rankPl) < 0) {
      // Default: the playlist noted most recently.
      var last = profileRanks().filter(function (r) { return isNum(r.mmr); }).pop();
      state.rankPl = last ? last.playlist : withMmr[0];
    }
    $('#rank-pl').innerHTML = withMmr.map(function (p) {
      return '<button type="button" role="radio" data-rank-pl="' + p + '" aria-checked="' + (p === state.rankPl) + '">' + esc(playlistLabel(p)) + '</button>';
    }).join('');
    var pts = profileRanks().filter(function (r) { return r.playlist === state.rankPl && isNum(r.mmr); });
    var groups = sum.filter(function (s) { return s.playlist === state.rankPl && isNum(s.mmr); });
    var datasets = groups.map(function (s, i) {
      var points = pts.filter(function (r) { return rankProfile(r) === s.profile; });
      var color = T.series[i % T.series.length];
      return { label: rankAccountLabel(s.profile), data: points.map(function (r) { return { x: r._t, y: r.mmr, rank: r }; }),
        borderColor: color, backgroundColor: alpha(color, 0.10), fill: false, borderWidth: 2,
        pointRadius: points.length > 40 ? 0 : 4, pointHoverRadius: 5, pointBackgroundColor: color,
        pointBorderColor: T.surface, pointBorderWidth: 2, tension: 0.25, cubicInterpolationMode: 'monotone' };
    });
    makeChart('c-mmr', {
      type: 'line',
      data: { datasets: datasets },
      options: {
        parsing: false,
        interaction: { mode: 'nearest', axis: 'x', intersect: false },
        scales: baseScales({
          x: { type: 'linear', ticks: { color: T.muted, maxTicksLimit: 6, callback: function (v) { return fmtDay(v, { day: 'numeric', month: 'short' }); } } },
          y: { grace: '8%', ticks: { color: T.muted, padding: 8, maxTicksLimit: 6, callback: function (v) { return fmtNum(v); } } }
        }),
        plugins: {
          legend: { display: datasets.length > 1 },
          crosshair: { enabled: true },
          tooltip: { displayColors: false, callbacks: {
            title: function (it) { return fmtDate(it[0].raw.x, true); },
            label: function (it) { var r = it.raw.rank; return rankAccountLabel(rankProfile(r)) + ' · ' + fmtNum(r.mmr) + ' MMR' + (isNum(r.tier) ? ' · ' + rankFull(r) : ''); }
          } }
        }
      }
    }, pts.length >= 2 ? null : tr('rank.chartNotEnough'));
  }
  /** Playlist of a ranked-looking match ('' when it cannot be told). */
  function matchPlaylist(m) {
    if (!m.online || (m.tag !== 'ranked' && m.tag !== 'tournament')) return '';
    if (m.tag === 'tournament') return 'tournament';
    if (m.variant === 'Hoops') return 'hoops';
    if (m.variant === 'Dropshot') return 'dropshot';
    return ['1v1', '2v2', '3v3'].indexOf(m.mode) >= 0 ? m.mode : '';
  }
  /** Session boundaries use every match of one account, before selecting ranked playlists. */
  function endedSession() {
    var profile = rankDestination();
    var ms = state.allRaw.filter(function (m) {
      return profileKey(m) === profile && isNum(Date.parse(m.started_at)) && endMs(m) >= startMs(m);
    }).slice().sort(function (a, b) { return startMs(a) - startMs(b); });
    if (!ms.length) return null;
    var start = startMs(ms[0]), lastEnd = endMs(ms[0]), session = [];
    ms.forEach(function (m) {
      if (startMs(m) - lastEnd > SESSION_GAP_MS) { session = []; start = startMs(m); }
      session.push(m);
      lastEnd = session.length === 1 ? endMs(m) : Math.max(lastEnd, endMs(m));
    });
    var idle = Date.now() - lastEnd;
    if (idle < SESSION_GAP_MS || idle > 12 * 3600 * 1000) return null;
    var count = {}, noted = {};
    session.forEach(function (m) {
      var p = matchPlaylist(m);
      if (p && m.result !== 'abandoned') count[p] = (count[p] || 0) + 1;
    });
    ranksForProfile(state.ranks, profile).forEach(function (r) { if (r._t >= start) noted[r.playlist] = true; });
    var todo = PLAYLISTS.filter(function (p) { return count[p] && !noted[p]; });
    return todo.length ? { profile: profile, start: start, todo: todo, count: count } : null;
  }
  function renderRankPrompt() {
    var box = $('#rank-prompt');
    var s = readOnly() ? null : endedSession();
    var key = s ? 'rt.rankPrompt.' + s.profile + '.' + s.start : '';
    var dismissed = false;
    try { dismissed = !!(key && localStorage.getItem(key)); } catch (e) { /* storage unavailable */ }
    if (!s || dismissed) { box.innerHTML = ''; return; }
    box.innerHTML = '<div class="warning info rank-prompt"><span class="w-icon">★</span><div><strong>' + tr('rank.promptTitle') + '</strong>' +
      '<p>' + esc(tr('profile.forAccount', { v: rankAccountLabel(s.profile) })) + ' · ' + tr('rank.promptText') + '</p><p class="w-actions">' + s.todo.map(function (p) {
        return '<button type="button" class="btn" data-rank-for="' + p + '">' + esc(playlistLabel(p)) + ' <small class="muted">' + plural(s.count[p], 'match') + '</small></button>';
      }).join(' ') + ' <button type="button" class="btn btn-ghost" data-rank-dismiss="' + esc(key) + '">' + tr('rank.promptLater') + '</button></p></div></div>';
  }

  /* rank dialog */
  var rankForm = { tier: null, family: null, level: 1, division: null };
  var rankContext = null;
  function currentRankContext(ctx) {
    return ctx && ctx === rankContext && ctx.gen === state.gen && ctx.dataFor === state.dataFor && !readOnly();
  }
  function openRankDialog(playlist) {
    if (readOnly()) return;
    rankContext = { profile: rankDestination(), gen: state.gen, dataFor: state.dataFor };
    var dlg = $('#rank-dialog'), f = $('#rank-form');
    var pl = playlist || state.rankPl || '2v2';
    f.elements.playlist.innerHTML = PLAYLISTS.map(function (p) { return '<option value="' + p + '"' + (p === pl ? ' selected' : '') + '>' + esc(playlistLabel(p)) + '</option>'; }).join('');
    prefillRank(pl);
    $('#rank-account').textContent = tr('rank.fixedTarget', { v: rankAccountLabel(rankContext.profile) });
    $('#rank-msg').textContent = '';
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
  }
  /** Starts from the last entry of the playlist: usually only the division or the MMR changed. */
  function prefillRank(pl) {
    if (!rankContext) return;
    var s = rankSummary(ranksForProfile(state.ranks, rankContext.profile)).filter(function (x) { return x.playlist === pl; })[0];
    var tier = s && isNum(s.tier) ? s.tier : null;
    rankForm = { family: tier == null ? null : rankFamily(tier), level: tier == null ? 1 : rankLevel(tier) || 1, division: s && isNum(s.division) ? s.division : null };
    $('#rank-form').elements.mmr.value = s && isNum(s.mmr) ? s.mmr : '';
    renderRankPicker();
  }
  function formTier() {
    var f = rankForm.family;
    if (f == null) return null;
    return f === 0 ? 0 : f === 8 ? 22 : (f - 1) * 3 + rankForm.level;
  }
  function renderRankPicker() {
    $('#rank-families').innerHTML = RANK_FAMILIES.map(function (fam, i) {
      var sample = i === 0 ? 0 : i === 8 ? 22 : (i - 1) * 3 + (rankForm.family === i ? rankForm.level : 1);
      return '<button type="button" role="radio" aria-checked="' + (rankForm.family === i) + '" data-rank-family="' + i + '" title="' + esc(tr('rank.f.' + fam.k)) + '">' +
        rankIcon(sample, 34) + '<span>' + esc(tr('rank.f.' + fam.k)) + '</span></button>';
    }).join('');
    var hasLevels = rankForm.family != null && rankForm.family >= 1 && rankForm.family <= 7;
    $('#rank-sub').hidden = !hasLevels;
    $('#rank-level').innerHTML = [1, 2, 3].map(function (l) {
      return '<button type="button" role="radio" data-rank-level="' + l + '" aria-checked="' + (rankForm.level === l) + '">' + ROMAN[l - 1] + '</button>';
    }).join('');
    $('#rank-division').innerHTML = '<button type="button" role="radio" data-rank-div="" aria-checked="' + (rankForm.division == null) + '">' + tr('rank.divUnknown') + '</button>' +
      [1, 2, 3, 4].map(function (d) {
        return '<button type="button" role="radio" data-rank-div="' + d + '" aria-checked="' + (rankForm.division === d) + '">' + tr('rank.div', { v: ROMAN[d - 1] }) + '</button>';
      }).join('');
    var t = formTier();
    $('#rank-preview').innerHTML = t == null ? '<span class="muted">' + tr('rank.pickHint') + '</span>' :
      rankIcon(t, 28) + ' <b>' + esc(rankFull({ tier: t, division: hasLevels ? rankForm.division : null })) + '</b> <button type="button" class="linklike" data-rank-clear>' + tr('rank.clear') + '</button>';
  }
  function saveRank(e) {
    e.preventDefault();
    var f = $('#rank-form'), msg = $('#rank-msg'), ctx = rankContext;
    if (!currentRankContext(ctx)) { msg.textContent = tr('rank.contextChanged'); msg.className = 'form-msg err'; return; }
    var tier = formTier(), mmrTxt = f.elements.mmr.value.trim();
    var mmr = mmrTxt === '' ? null : parseInt(mmrTxt, 10);
    if (tier == null && mmr == null) { msg.textContent = tr('rank.needOne'); msg.className = 'form-msg err'; return; }
    var hasDiv = tier != null && tier >= 1 && tier <= 21 && rankForm.division != null;
    var body = { playlist: f.elements.playlist.value, profile: ctx.profile,
      tier: tier, division: hasDiv ? rankForm.division : null, mmr: mmr };
    msg.textContent = tr('common.saving'); msg.className = 'form-msg';
    if (MOCK) { body.id = Date.now(); body.at = new Date().toISOString(); state.ranks = annotateRanks(state.ranks.concat([body])); closeRank(); return; }
    return apiFetch('/api/ranks', { method: 'POST', body: JSON.stringify(body) }).then(function (r) {
      if (!currentRankContext(ctx)) return;
      state.ranks = annotateRanks(state.ranks.concat([r]));
      state.rankPl = isNum(r.mmr) ? r.playlist : state.rankPl;
      closeRank();
      toast(tr('rank.saved'));
    }).catch(function (err) { if (currentRankContext(ctx)) { msg.textContent = tr('toast.saveFailed', { v: err.message }); msg.className = 'form-msg err'; } });
  }
  function closeRank() {
    rankContext = null;
    var dlg = $('#rank-dialog');
    if (dlg.close) dlg.close(); else dlg.removeAttribute('open');
    renderRanks();
  }
  function deleteRank(id) {
    if (!window.confirm(tr('rank.confirmDelete'))) return;
    var done = function () { state.ranks = state.ranks.filter(function (r) { return r.id !== id; }); renderRanks(); };
    if (MOCK) { done(); return; }
    apiFetch('/api/ranks/' + id, { method: 'DELETE' }).then(done).catch(function (e) { toast(tr('toast.deleteFailed', { v: e.message }), true); });
  }
  function bindRanks() {
    $('#rank-add').addEventListener('click', function () { openRankDialog(); });
    $('#rank-section').addEventListener('click', function (e) {
      var b;
      if ((b = e.target.closest('[data-rank-pl]'))) { state.rankPl = b.getAttribute('data-rank-pl'); renderRanks(); }
      else if ((b = e.target.closest('[data-rank-for]'))) openRankDialog(b.getAttribute('data-rank-for'));
      else if ((b = e.target.closest('[data-rank-dismiss]'))) { try { localStorage.setItem(b.getAttribute('data-rank-dismiss'), '1'); } catch (x) { /* storage unavailable */ } renderRanks(); }
      else if ((b = e.target.closest('[data-rank-del]'))) deleteRank(+b.getAttribute('data-rank-del'));
    });
    var dlg = $('#rank-dialog'), form = $('#rank-form');
    $$('[data-close]', dlg).forEach(function (b) { b.addEventListener('click', function () { dlg.close ? dlg.close() : dlg.removeAttribute('open'); }); });
    form.elements.playlist.addEventListener('change', function () { prefillRank(form.elements.playlist.value); });
    form.addEventListener('click', function (e) {
      var b;
      if ((b = e.target.closest('[data-rank-family]'))) { rankForm.family = +b.getAttribute('data-rank-family'); if (rankForm.family === 0 || rankForm.family === 8) rankForm.division = null; }
      else if ((b = e.target.closest('[data-rank-level]'))) rankForm.level = +b.getAttribute('data-rank-level');
      else if ((b = e.target.closest('[data-rank-div]'))) { var v = b.getAttribute('data-rank-div'); rankForm.division = v ? +v : null; }
      else if (e.target.closest('[data-rank-clear]')) { rankForm.family = null; rankForm.division = null; }
      else return;
      renderRankPicker();
    });
    form.addEventListener('submit', saveRank);
  }
  /** Demo mode: a few weeks of 2v2 and 1v1 entries. */
  function mockRanks() {
    var out = [], now = Date.now(), mmr2 = 1010, mmr1 = 830;
    for (var i = 20; i >= 0; i--) {
      mmr2 += Math.round(Math.sin(i * 1.3) * 14 + 4);
      out.push({ id: 100 + i, at: new Date(now - i * 2.5 * 86400000).toISOString(), playlist: '2v2', tier: 14, division: Math.min(4, Math.max(1, Math.round((mmr2 - 1000) / 12) + 2)), mmr: mmr2 });
      if (i % 3 === 0) { mmr1 += Math.round(Math.cos(i) * 18); out.push({ id: 200 + i, at: new Date(now - i * 2.5 * 86400000 - 3600000).toISOString(), playlist: '1v1', tier: 12, division: 2, mmr: mmr1 }); }
    }
    out.push({ id: 300, at: new Date(now - 40 * 86400000).toISOString(), playlist: '3v3', tier: 13, division: null, mmr: null });
    return annotateRanks(out);
  }

  /* ---------- render: season goal ---------- */
  function goalModeLabel(mode) { return mode === 'all' ? tr('goal.allModes') : mode; }
  function goalKpi(label, value, sub, pct, cls) {
    return '<div class="card kpi goal-kpi' + (cls ? ' ' + cls : '') + '"><span class="kpi-label">' + label + '</span>' +
      '<span class="kpi-value">' + value + '</span>' +
      (pct != null ? '<div class="goal-meter" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(Math.min(1, pct) * 100) + '"><span style="width:' + (Math.min(1, Math.max(0, pct)) * 100).toFixed(1) + '%"></span></div>' : '') +
      '<span class="kpi-sub">' + sub + '</span></div>';
  }
  function renderGoal() {
    var g = computeGoal(state.all, state.config && state.config.goal, null, profileManual());
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
          title: function (it) { var i = it[0].dataIndex; return i === g.labels.length - 1 ? tr('goals.overtime') : tr('goals.minute', { v: g.labels[i] }); },
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
    var ko = s.kickoff;
    html += '<div class="card sit"><h3>' + tr('sit.forfeits') + '</h3><div class="sit-rows">' +
      sitRow(tr('sit.forfeitWon'), s.forfeitWon) + sitRow(tr('sit.forfeitLost'), s.forfeitLost) +
      sitRow(tr('sit.abandoned'), state.filters.excludeAbandoned ? '<span class="muted" title="' + esc(tr('sit.hiddenTip')) + '">' + tr('sit.hidden') + '</span>' : s.abandoned) + '</div>' +
      '<div class="sit-foot">' + tr('sit.forfeitFoot') + '</div></div>';
    html += '<div class="card sit sit-wide"><h3>' + tr('sit.kickoffs') + ' <span class="muted">· ' + tr('sit.kickoffsN', { n: ko.kickoffs, v: fmtNum(ko.kickoffs) }) + '</span></h3><div class="sit-rows">' +
      sitRow(tr('sit.koLost'), ko.lost + ' <small class="muted">· ' + fmtPct(ko.lostShare) + '</small>') +
      sitRow(tr('sit.koWon'), ko.won + ' <small class="muted">· ' + fmtPct(ko.wonShare) + '</small>') +
      sitRow(tr('sit.koConceded'), fmtPct(ko.concededShare)) +
      meterRow(tr('sit.koAfterLost'), ko.afterLost, 'them') + '</div>' +
      '<div class="sit-foot">' + tr('sit.kickoffFoot', { v: KICKOFF_GOAL_S }) + '</div></div>';
    var sc = computeScoreStates(ms);
    html += '<div class="card sit sit-wide"><h3>' + tr('sit.halftime') + ' <span class="muted">· ' + plural(sc.n, 'match') + '</span></h3><div class="sit-rows">' +
      meterRow(tr('sit.htAhead'), sc.ahead) + meterRow(tr('sit.htLevel'), sc.level) + meterRow(tr('sit.htBehind'), sc.behind, 'them') +
      sitRow(tr('sit.leadLost'), fmtPct(sc.ledLostShare) + ' <small class="muted">· ' + tr('sit.ofLeads', { n: sc.led, v: fmtNum(sc.led) }) + '</small>') + '</div>' +
      '<div class="sit-foot">' + tr('sit.halftimeFoot', { up: fmtPct(sc.comebackShare), f: sc.lateFor, a: sc.lateAgainst }) + '</div></div>';
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
    renderFatigue(ms);
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
      var v = m.unit === '%' ? fmtPct100(m.avg, m.digits) : fmtNum(m.avg, m.digits) + (m.unit ? '<span class="unit">' + unitLabel(m.unit) + '</span>' : '');
      var trend = '';
      if (isNum(m.trend)) {
        var good = m.higherBetter == null ? null : (m.trend > 0) === m.higherBetter;
        var small = Math.abs(m.trend) < Math.pow(10, -m.digits) * 0.5;
        trend = small ? tr('mech.stable')
          : tr('mech.vsPrev', { v: '<span class="' + (good == null ? '' : good ? 'up' : 'down') + '">' + (m.trend >= 0 ? '▲ ' : '▼ ') + fmtSigned(m.trend, m.digits) + (m.unit === '%' ? ' pt' : m.unit ? ' ' + unitLabel(m.unit) : '') + '</span>' });
      } else trend = m.n ? plural(m.n, 'match') : tr('mech.noData');
      return '<div class="card mech"><span class="kpi-label">' + esc(m.label) + '</span><span class="kpi-value">' + v + '</span>' +
        '<span class="trend">' + trend + '</span><div class="spark"><canvas id="sp-mech-' + i + '"></canvas></div></div>';
    }).join('');
    mc.metrics.forEach(function (m, i) {
      var cv = document.getElementById('sp-mech-' + i);
      if (hasData(m.series)) sparkline(cv, m.series, T.us, m.label, function (x) { return m.unit === '%' ? fmtPct100(x, m.digits) : fmtNum(x, m.digits) + (m.unit ? ' ' + unitLabel(m.unit) : ''); });
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
      '<div class="split-head"><span class="card-title">' + tr('mech.splitAverage') + '</span><span class="legend">' +
      segs.map(function (g) { return '<span><i style="background:' + g.color + '"></i>' + g.label + ' ' + fmtPct100(g.v, 0) + '</span>'; }).join('') + '</span></div>' +
      '<div class="split-bar">' + segs.map(function (g) { return '<span style="width:' + ((g.v || 0) / tot * 100).toFixed(2) + '%;background:' + g.color + '" title="' + esc(tr('fmt.labelValue', { label: g.label, v: fmtPct100(g.v, 1) })) + '"></span>'; }).join('') + '</div>' +
      '<div class="detail-facts">' + fact(tr('mech.boosting_pct'), fmtPct100(mc.boostingPct, 1)) + fact(tr('mech.full_boost_pct'), fmtPct100(mc.fullBoostPct, 1)) +
      fact(tr('mech.powerslide_pct'), fmtPct100(mc.powerslidePct, 1)) +
      '<span>' + tr('mech.demolished_s') + ' <b>' + fmtNum(mc.demolishedS, 1) + ' s</b> ' + tr('mech.perMatch') + '</span>' +
      fact(tr('mech.hardestHit'), formatRawSpeed(mc.maxHitRaw, mc.speedUnit)) + '</div>'
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

  /* ---------- render: what wins your matches ---------- */
  function unitLabel(unit) { return speedUnitLabel(unit); }
  function unitSuffix(unit) { return unit === '%' ? (I18N.getLang() === 'fr' ? ' %' : '%') : unit ? ' ' + unitLabel(unit) : ''; }
  function fmtStat(v, unit, digits) { return fmtNum(v, digits) + unitSuffix(unit); }
  function focusLabel(f) { return tr('focus.' + f); }
  function renderWinFactors(ms) {
    var wf = computeWinFactors(ms);
    var box = $('#win-factors');
    $('#wf-sub').textContent = tr('wf.sub', { v: plural(wf.decided, 'match') });
    if (wf.tested === 0) { box.innerHTML = '<p class="muted small wf-empty">' + tr('wf.needMore', { v: 2 * FACTOR_MIN }) + '</p>'; $('#wf-focus').innerHTML = ''; return; }
    if (!wf.clear.length) {
      box.innerHTML = '<p class="muted small wf-empty">' + tr('wf.noneClear', { v: wf.tested }) + '</p>';
    } else {
      box.innerHTML = wf.clear.slice(0, 4).map(function (r) {
        var thr = fmtStat(r.threshold, r.unit, r.digits), perMode = r.threshold == null;
        var good = r.delta > 0 ? r.hi : r.lo, bad = r.delta > 0 ? r.lo : r.hi;
        var goodLbl = perMode ? tr(r.delta > 0 ? 'wf.aboveMedian' : 'wf.belowMedian')
          : tr(r.delta > 0 ? (r.inclusive ? 'wf.from' : 'wf.above') : (r.inclusive ? 'wf.under' : 'wf.upTo'), { v: thr });
        var badLbl = perMode ? tr(r.delta > 0 ? 'wf.belowMedian' : 'wf.aboveMedian')
          : tr(r.delta > 0 ? (r.inclusive ? 'wf.under' : 'wf.upTo') : (r.inclusive ? 'wf.from' : 'wf.above'), { v: thr });
        var bar = function (label, s, cls) {
          return '<div class="sit-row"><span>' + label + ' <span class="muted">· ' + plural(s.n, 'match') + '</span></span><b>' + fmtPct(s.wr) + '</b>' +
            '<div class="meter ' + cls + '"><span style="width:' + (s.wr * 100).toFixed(1) + '%"></span></div></div>';
        };
        var recent = isNum(r.recent) ? tr('wf.recent', { v: fmtStat(r.recent, r.unit, r.digits) }) : '';
        return '<div class="card sit wf"><h3>' + esc(tr(r.lk)) + ' <span class="chip ' + (r.better === 'more' ? 'up' : 'down') + '">' +
          tr(r.better === 'more' ? 'wf.more' : 'wf.less') + '</span></h3><div class="sit-rows">' +
          bar(goodLbl, good, '') + bar(badLbl, bad, 'them') + '</div>' +
          '<div class="sit-foot">' + tr('wf.gap', { v: fmtNum(Math.abs(r.delta) * 100) }) + (recent ? ' · ' + recent : '') + '</div></div>';
      }).join('');
    }
    var sug = computeFocusSuggestions(ms);
    var main = sug[0];
    $('#wf-focus').innerHTML = '<span class="wf-focus-label">' + tr('wf.toTrain') + '</span>' +
      sug.map(function (s) { return '<span class="chip focus-chip" title="' + esc(focusReason(s)) + '">' + esc(focusLabel(s.focus)) + '</span>'; }).join('') +
      ' <a href="' + href('training') + '" class="wf-plan">' + tr('wf.plan') + ' <span aria-hidden="true">→</span></a>' +
      (main && main.reason !== 'base' ? '<span class="muted small wf-why">' + esc(focusReason(main)) + '</span>' : '');
  }
  /** Why a mechanic is suggested, in one sentence. */
  function focusReason(s) {
    var p = s.params || {};
    switch (s.reason) {
      case 'factor':
        var r = p.factor;
        if (r.threshold == null) return tr(r.delta > 0 ? 'wf.whyMoreModes' : 'wf.whyLessModes', { stat: tr(r.lk), pct: fmtPct(r.recentBadShare) });
        return tr(r.delta > 0 ? 'wf.whyMore' : 'wf.whyLess', { stat: tr(r.lk), v: fmtStat(r.threshold, r.unit, r.digits), pct: fmtPct(r.recentBadShare) });
      case 'kickoff': return tr('wf.whyKickoff', { lost: fmtPct(p.lost, 1), won: fmtPct(p.won, 1) });
      case 'conversion': return tr('wf.whyConversion', { v: fmtPct(p.v) });
      case 'high': return tr('wf.whyHigh', { v: fmtPct(p.v) });
      default: return tr('wf.whyBase');
    }
  }

  /* ---------- render: on the field ---------- */
  function renderField(ms) {
    var t = computeTouches(ms), gp = computeGoalPlacement(ms), d = computeDemos(ms);
    var available = t.matches || gp.scored.n || gp.conceded.n || d.matches;
    $('#field-sub').textContent = available ? tr('field.availability', {
      touches: t.matches, goals: gp.scored.n + gp.conceded.n, demos: d.matches
    }) : tr('field.none');
    $('#field-grid').hidden = !available;
    if (!available) return;
    drawTouchMap($('#touch-map'), t);
    var fact = function (label, v, tip) { return '<div class="sit-row"' + (tip ? ' title="' + esc(tip) + '"' : '') + '><span>' + label + '</span><b>' + v + '</b></div>'; };
    var third = function (label, v, cls) {
      return '<div class="sit-row"><span>' + label + '</span><b>' + fmtPct(v) + '</b><div class="meter ' + (cls || '') + '"><span style="width:' + (num(v) * 100).toFixed(1) + '%"></span></div></div>';
    };
    $('#touch-facts').innerHTML =
      third(tr('field.off'), t.offShare) + third(tr('field.mid'), t.midShare) + third(tr('field.def'), t.defShare, 'them') +
      fact(tr('field.offWinLoss'), fmtPct(t.offWin.share) + ' <small class="muted">/ ' + fmtPct(t.offLoss.share) + '</small>', tr('field.offWinLossTip')) +
      fact(tr('field.aerial'), fmtPct(t.aerialShare), tr('field.aerialTip')) +
      fact(tr('field.powerful'), '—', tr('field.powerfulTip')) +
      fact(tr('field.gain'), isNum(t.avgGain) ? fmtSigned(t.avgGain, 1) + ' ' + speedUnitLabel(t.speedUnit) : '—', tr('field.gainTip')) +
      fact(tr('field.perMatch'), fmtNum(t.perMatch, 1));

    $('#goal-scored').innerHTML = goalSvg(gp.scored, true);
    $('#goal-conceded').innerHTML = goalSvg(gp.conceded, false);
    $('#goal-facts').innerHTML =
      fact(tr('field.highScored'), fmtPct(gp.scored.highShare)) + fact(tr('field.cornerScored'), fmtPct(gp.scored.cornerShare)) +
      fact(tr('field.highConceded'), fmtPct(gp.conceded.highShare)) + fact(tr('field.cornerConceded'), fmtPct(gp.conceded.cornerShare));

    $('#demo-facts').innerHTML =
      fact(tr('field.demosMade'), fmtNum(d.madePer, 2)) + fact(tr('field.demosTaken'), fmtNum(d.takenPer, 2)) +
      fact(tr('field.demosNet'), fmtSigned(d.matches ? d.madePer - d.takenPer : null, 2)) +
      fact(tr('field.nemesis'), d.nemesis ? esc(d.nemesis.name) + ' <small class="muted">· ' + d.nemesis.n + '</small>' : '—', tr('field.nemesisTip')) +
      fact(tr('field.victim'), d.victim ? esc(d.victim.name) + ' <small class="muted">· ' + d.victim.n + '</small>' : '—') +
      meterRow(tr('field.demoedOften'), d.often, 'them') + meterRow(tr('field.demoedRarely'), d.rarely);
  }

  /** Top-down field, my goal at the bottom; cells shaded by the share of my touches. */
  function drawTouchMap(canvas, t) {
    var w = Math.min(canvas.parentNode.clientWidth || 260, 320), h = Math.round(w * FIELD.y / FIELD.x);
    var dpr = window.devicePixelRatio || 1;
    canvas.width = w * dpr; canvas.height = h * dpr;
    canvas.style.width = w + 'px'; canvas.style.height = h + 'px';
    var ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    var pad = 10, fw = w - 2 * pad, fh = h - 2 * pad, cw = fw / MAP_COLS, ch = fh / MAP_ROWS;
    ctx.fillStyle = T['surface-2']; ctx.fillRect(pad, pad, fw, fh);
    for (var r = 0; r < MAP_ROWS; r++) {
      for (var c = 0; c < MAP_COLS; c++) {
        var n = t.cells[r * MAP_COLS + c];
        if (!n) continue;
        ctx.fillStyle = alpha(T.us, 0.08 + 0.82 * Math.sqrt(n / t.max));
        // Row 0 is the most negative Y (my goal): drawn at the bottom.
        ctx.fillRect(pad + c * cw, pad + (MAP_ROWS - 1 - r) * ch, cw, ch);
      }
    }
    ctx.strokeStyle = T['border-strong']; ctx.lineWidth = 1;
    ctx.strokeRect(pad + 0.5, pad + 0.5, fw - 1, fh - 1);
    ctx.beginPath(); ctx.moveTo(pad, pad + fh / 2); ctx.lineTo(pad + fw, pad + fh / 2); ctx.stroke();
    ctx.beginPath(); ctx.arc(pad + fw / 2, pad + fh / 2, fw * 0.11, 0, Math.PI * 2); ctx.stroke();
    ctx.setLineDash([4, 4]);
    [1, 2].forEach(function (k) { var y = pad + fh * k / 3; ctx.beginPath(); ctx.moveTo(pad, y); ctx.lineTo(pad + fw, y); ctx.stroke(); });
    ctx.setLineDash([]);
    var gw = fw * FIELD.goalX / FIELD.x;
    ctx.fillStyle = T.them; ctx.fillRect(pad + (fw - gw) / 2, pad - 4, gw, 4);
    ctx.fillStyle = T.us; ctx.fillRect(pad + (fw - gw) / 2, pad + fh, gw, 4);
    canvas.setAttribute('aria-label', tr('field.mapAria', { off: fmtPct(t.offShare), mid: fmtPct(t.midShare), def: fmtPct(t.defShare) }));
  }

  /**
   * A goal mouth seen from the field, 3 x 2 zones shaded by their share of the goals, and the last goals as dots.
   * Facing the opponents' goal, +X is on the left; facing mine, on the right.
   */
  function goalSvg(s, theirs) {
    var W = 300, H = Math.round(W * FIELD.goalZ / (2 * FIELD.goalX));
    var col = theirs ? 'var(--us)' : 'var(--them)';
    var sx = function (x) { var u = (x + FIELD.goalX) / (2 * FIELD.goalX); return (theirs ? 1 - u : u) * W; };
    var sy = function (z) { return H - Math.max(0, Math.min(1, z / FIELD.goalZ)) * H; };
    var cuts = [-FIELD.goalX, -FIELD.goalX / 3, FIELD.goalX / 3, FIELD.goalX];
    var html = '<svg viewBox="-6 -6 ' + (W + 12) + ' ' + (H + 8) + '" role="img" aria-label="' + esc(tr(theirs ? 'field.scoredAria' : 'field.concededAria', { n: s.n })) + '">';
    for (var z = 0; z < 6; z++) {
      var c = z % 3, row = Math.floor(z / 3);
      var x1 = sx(cuts[c]), x2 = sx(cuts[c + 1]);
      var x = Math.min(x1, x2), w = Math.abs(x2 - x1), y = row ? H / 2 : 0;
      var sh = s.n ? s.zones[z] / s.n : 0;
      html += '<rect x="' + x.toFixed(1) + '" y="' + y + '" width="' + w.toFixed(1) + '" height="' + (H / 2) + '" fill="' + col + '" fill-opacity="' + (s.n ? (0.06 + 0.6 * (s.max ? s.zones[z] / s.max : 0)).toFixed(2) : 0.04) + '"></rect>';
      if (s.n) html += '<text x="' + (x + w / 2).toFixed(1) + '" y="' + (y + H / 4 + 5) + '" text-anchor="middle" class="goal-pct">' + esc(fmtPct(sh)) + '</text>';
    }
    s.points.forEach(function (p) { html += '<circle cx="' + sx(p.x).toFixed(1) + '" cy="' + sy(num(p.z)).toFixed(1) + '" r="2.4" class="goal-dot"></circle>'; });
    html += '<path d="M0 ' + H + ' V0 H' + W + ' V' + H + '" class="goal-frame"></path><line x1="-6" x2="' + (W + 6) + '" y1="' + H + '" y2="' + H + '" class="goal-line"></line></svg>';
    return html + '<div class="goal-cap">' + tr(theirs ? 'field.scored' : 'field.conceded') + ' <span class="muted">· ' + plural(s.n, 'goal') + '</span></div>';
  }

  /* ---------- render: through a session ---------- */
  function renderFatigue(ms) {
    var f = computeSessionFatigue(ms);
    var head = '<tr><th></th>' + f.buckets.map(function (b) {
      return '<th class="r">' + (b[1] === Infinity ? tr('fatigue.from', { v: b[0] }) : tr('fatigue.range', { a: b[0], b: b[1] })) + '</th>';
    }).join('') + '</tr>';
    var body = f.rows.map(function (r) {
      return '<tr><td class="name">' + esc(tr(r.lk)) + '</td>' + r.cells.map(function (c, i) {
        var v = isNum(c.avg) ? fmtStat(c.avg, r.unit, r.digits) : '—';
        var d = '';
        if (isNum(c.delta) && Math.abs(c.delta) >= Math.pow(10, -r.digits)) {
          var good = (c.delta > 0) === r.higherBetter;
          d = ' <small class="' + (good ? 'pos' : 'neg') + '">' + fmtSigned(c.delta, r.digits) + '</small>';
        }
        return '<td class="r"' + (c.n < 5 ? ' title="' + esc(tr('common.smallSample')) + '"' : '') + '>' + (i === 0 || c.n >= 5 ? v : '<span class="muted">' + v + '</span>') + d + '</td>';
      }).join('') + '</tr>';
    }).join('');
    $('#t-fatigue').innerHTML = '<thead>' + head + '</thead><tbody>' + body + '</tbody>';
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
        return '<tr><td class="name">' + playerName(t.name, t.pid) + (isOwnAccount(t.pid) ? ' <span class="chip" title="' + esc(tr('profile.ownTip')) + '">' + tr('profile.own') + '</span>' : '') + '</td><td class="r">' + t.n + '</td><td class="r hide-mobile">' + t.wins + '–' + t.losses + '</td><td class="r">' + wrCell(t.wr) + '</td>' +
          '<td class="r">' + (d == null ? '—' : fmtSigned(d) + ' pts') + '</td><td class="r hide-mobile">' + fmtNum(t.goalsPer, 2) + '</td></tr>';
      }).join('') : '<tr class="empty-row"><td colspan="6">' + tr('tbl.noMate') + '</td></tr>') + '</tbody>';
  }

  /** Server mode: record with and against the other server players (matches both recorded). */
  function computeDuos(ms) {
    var by = {};
    ms.forEach(function (m) {
      var w = (state.shared || {})[m.id];
      if (!w || (m.result !== 'win' && m.result !== 'loss')) return;
      var win = m.result === 'win';
      w.forEach(function (x) {
        var d = by[x.handle] || (by[x.handle] = { handle: x.handle, name: x.name, n: 0, wins: 0, my: 0, their: 0, scored: 0, vsN: 0, vsWins: 0 });
        if (!x.same_team) { d.vsN++; if (win) d.vsWins++; return; }
        d.n++;
        if (win) d.wins++;
        var pid = String(x.primary_id || '').toLowerCase();
        var p = pid && (m.players || []).find(function (pl) { return String(pl.primary_id || '').toLowerCase() === pid; });
        if (p && isNum(p.score) && m.me && isNum(m.me.score)) { d.scored++; d.my += m.me.score; d.their += p.score; }
      });
    });
    return Object.keys(by).map(function (k) { return by[k]; }).sort(function (a, b) { return (b.n + b.vsN) - (a.n + a.vsN); });
  }
  function renderDuos(ms) {
    var sec = $('#duos-section');
    var rows = isServer() ? computeDuos(ms) : [];
    sec.hidden = !rows.length;
    if (!rows.length) return;
    var overall = wl(ms).wr;
    $('#t-duos').innerHTML = '<thead><tr><th>' + tr('duos.player') + '</th><th class="r">' + tr('duos.together') + '</th><th class="r">' + tr('kpi.winrate') + '</th>' +
      '<th class="r" title="' + esc(tr('tbl.vsAvgTip')) + '">' + tr('tbl.vsAvg') + '</th><th class="r hide-mobile" title="' + esc(tr('duos.scoreTip')) + '">' + tr('duos.score') + '</th>' +
      '<th class="r">' + tr('duos.against') + '</th></tr></thead><tbody>' + rows.map(function (d) {
        var wr = d.n ? d.wins / d.n : null;
        var diff = isNum(wr) && isNum(overall) ? (wr - overall) * 100 : null;
        var me = d.scored ? d.my / d.scored : null, them = d.scored ? d.their / d.scored : null;
        return '<tr><td class="name"><a href="' + routeHash(d.handle, '') + '">' + esc(d.name) + '</a></td>' +
          '<td class="r">' + (d.n ? d.n + ' <small class="muted">(' + d.wins + '–' + (d.n - d.wins) + ')</small>' : '—') + '</td>' +
          '<td class="r">' + (d.n ? wrCell(wr) : '—') + '</td>' +
          '<td class="r">' + (diff == null ? '—' : fmtSigned(diff) + ' pts') + '</td>' +
          '<td class="r hide-mobile num">' + (me == null ? '—' : fmtNum(me) + ' / ' + fmtNum(them)) + '</td>' +
          '<td class="r">' + (d.vsN ? d.vsWins + '–' + (d.vsN - d.vsWins) : '—') + '</td></tr>';
      }).join('') + '</tbody>';
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
      var sw = sharedChip(m);
      if (sw) chips.push(sw);
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
    if (!isNum(g.t) || g.t < 0 || g.t > num(m.duration_s)) return '—';
    var t = num(g.t), reg = regulationS(m);
    return isOtGoal(m, g) ? '+' + fmtClock(Math.max(0, t - reg)) : fmtClock(t);
  }
  function sortedGoals(m) {
    return (m.goals || []).filter(function (g) { return g && typeof g === 'object'; }).slice().sort(function (a, b) { return num(a.t) - num(b.t); });
  }
  /** Goal timeline: our goals above the axis, theirs below, overtime shaded. */
  function goalTimeline(m, goals) {
    if (!goalsComplete(m)) return '';
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
    var us = 0, them = 0, complete = goalsComplete(m);
    var ko = goalsComplete(m) ? kickoffGoals(m).filter(function (k) { return k.kickoff; }).map(function (k) { return k.goal; }) : [];
    return goals.map(function (g) {
      if (g.team === 'us') us++; else if (g.team === 'them') them++;
      return '<li><span class="t">' + goalTime(m, g) + '</span><span class="d" style="background:var(--' + (g.team === 'us' ? 'us' : g.team === 'them' ? 'them' : 'neutral') + ')"></span>' +
        '<span class="who">' + esc(g.scorer || '?') + (g.me_scored ? ' <span class="chip">' + tr('common.you') + '</span>' : '') + (g.assister ? ' <small>· ' + tr('match.assist', { v: esc(g.assister) }) + '</small>' : '') +
        (isNum(g.speed) && g.speed > 0 ? ' <small>· ' + formatRawSpeed(g.speed, speedUnit(m)) + '</small>' : '') +
        (ko.indexOf(g) >= 0 ? ' <span class="chip" title="' + esc(tr('match.kickoffTip', { v: KICKOFF_GOAL_S })) + '">' + tr('match.kickoff') + '</span>' : '') + '</span>' +
        (complete ? '<span class="sc">' + us + '–' + them + '</span>' : '') + '</li>';
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
      fact('match.avgSpeed', formatRawSpeed(m.movement.avg_speed, speedUnit(m)));
      fact('mech.supersonic_pct', fmtPct100(m.movement.supersonic_pct, 1));
      fact('mech.air_split', fmtPct100(m.movement.air_pct, 1));
      fact('match.avgBoost', fmtNum(m.movement.avg_boost));
    }
    if (m.hits) facts.push('<span>' + tr('match.hits', { n: '<b>' + fmtNum(m.hits.count) + '</b>', avg: '<b>' + formatRawSpeed(m.hits.avg_speed, speedUnit(m)) + '</b>', max: '<b>' + formatRawSpeed(m.hits.max_speed, speedUnit(m)) + '</b>' }) + '</span>');
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
    var cur = r.view === 'players' || r.player ? 'players' : r.view === 'dash' ? 'dash' : r.view === 'training' || r.view === 'live' ? r.view : 'history';
    $$('[data-nav]').forEach(function (a) {
      if (a.getAttribute('data-nav') === cur) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    var who = r.player ? ((viewedPlayer() || {}).name || r.player) + ' · ' : '';
    if (r.view === 'dash') document.title = who + 'Rocket Tracker';
    else if (r.view === 'history') document.title = who + tr('nav.history') + ' · Rocket Tracker';
    else if (r.view === 'players') document.title = tr('nav.players') + ' · Rocket Tracker';
    else if (r.view === 'training') document.title = who + tr('nav.training') + ' · Rocket Tracker';
    else if (r.view === 'live') document.title = who + tr('nav.live') + ' · Rocket Tracker';
    renderPlayerBanner();
    renderTimerPill();
  }
  /** Your own pages, when a route names you as "another player". */
  function selfRoute(r) {
    if (!r.player || !state.me || r.player !== state.me.handle) return null;
    return routeHash(null, r.view === 'history' || r.view === 'training' || r.view === 'live' ? r.view : r.view === 'match' && r.id != null ? 'match/' + r.id : '');
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
    var h = r.view === 'history' ? $('#h-history') : r.view === 'match' ? $('#mp-title') : r.view === 'players' ? $('#h-players') : r.view === 'training' ? $('#h-training') : r.view === 'live' ? $('#h-live') : null;
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
    if (isDuel(m)) b.push('<span class="chip" title="' + esc(tr('badge.duelTip')) + '">' + tr('badge.duel') + '</span>');
    if (m.partial) b.push('<span class="chip warn" title="' + esc(partialTip()) + '">' + tr('badge.partial') + '</span>');
    if (m.online === false) b.push('<span class="chip" title="' + esc(tr('badge.offlineTip')) + '">' + tr('badge.offline') + '</span>');
    var sw = sharedChip(m);
    if (sw) b.push(sw);
    return b.join('');
  }
  /** Chip naming the server players who were in the match too. */
  function sharedChip(m) {
    var w = (state.shared || {})[m.id];
    if (!w || !w.length) return '';
    var mates = w.filter(function (x) { return x.same_team; }).map(function (x) { return x.name; });
    var opps = w.filter(function (x) { return !x.same_team; }).map(function (x) { return x.name; });
    var tip = [mates.length ? tr('shared.with', { v: mates.join(', ') }) : '', opps.length ? tr('shared.against', { v: opps.join(', ') }) : ''].filter(Boolean).join(' · ');
    return '<span class="chip shared" title="' + esc(tip) + '">👥 ' + esc(w.map(function (x) { return x.name; }).join(', ')) + '</span>';
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
    $('#h-duel').value = f.duel;
    if ($('#h-duel').value !== f.duel) { f.duel = DEFAULT_HIST_FILTERS.duel; $('#h-duel').value = f.duel; }
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
    var man = filterManualHistory(profileManual(), f);
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
      box.innerHTML = !state.all.length && !profileManual().length
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
    $('#h-duel').addEventListener('change', function (e) { state.hist.duel = e.target.value; onHistChanged(); });
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
  function mpScoreboard(m, idp) {
    idp = idp || 'mp';
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
    return '<section class="card mp-card mp-span" aria-labelledby="' + idp + '-h-sb"><div class="mp-head"><h2 id="' + idp + '-h-sb">' + tr('match.scoreboard') + '</h2>' +
      '<span class="card-meta">' + tr('mp.sbHint') + '</span></div>' +
      '<div class="table-scroll"><table class="mp-sb"><thead><tr><th scope="col">' + tr('match.player') + '</th>' + SB_COLS.map(function (c) { return '<th scope="col">' + tr('perf.' + c) + '</th>'; }).join('') + '</tr></thead>' +
      team(true) + team(false) + '</table></div></section>';
  }

  function mpGoals(m, idp) {
    idp = idp || 'mp';
    var goals = sortedGoals(m);
    var complete = goalsComplete(m);
    var expected = num(m.team_score) + num(m.opp_score);
    var us = 0, them = 0;
    var you = ' <span class="chip you">' + tr('common.you') + '</span>';
    var list = goals.length ? '<ol class="goal-list mp-goal-list">' + goals.map(function (g) {
      var ours = g.team === 'us';
      if (ours) us++; else if (g.team === 'them') them++;
      var spd = isNum(g.speed) && g.speed > 0 ? formatRawSpeed(g.speed, speedUnit(m)) : '';
      return '<li><span class="t num">' + goalTime(m, g) + '</span><span class="d" style="background:var(--' + (ours ? 'us' : g.team === 'them' ? 'them' : 'neutral') + ')"></span>' +
        '<span class="sr-only">' + tr(ours ? 'match.yourTeam' : g.team === 'them' ? 'match.opponents' : 'common.unknown') + '</span>' +
        '<span class="who"><b>' + esc(g.scorer || '?') + '</b>' + (g.me_scored ? you : '') +
        (g.assister ? ' <small>· ' + tr('match.assist', { v: esc(g.assister) }) + (g.me_assist ? you : '') + '</small>' : '') + '</span>' +
        '<span class="spd num">' + spd + '</span>' +
        (complete ? '<span class="sc num" title="' + esc(tr('mp.scoreAfter')) + '">' + us + '–' + them + '</span>' : '') + '</li>';
    }).join('') + '</ol>' : '<p class="muted small">' + tr(complete ? 'match.noGoal' : 'match.noGoalRecorded') + '</p>';
    var note = complete ? '' : '<p class="mp-note">' + tr('mp.partialGoals', { v: plural(goals.length, 'goal'), n: goals.length, total: expected }) + '</p>';
    return '<section class="card mp-card" aria-labelledby="' + idp + '-h-goals"><div class="mp-head"><h2 id="' + idp + '-h-goals">' + tr('perf.goals') + '</h2>' +
      '<span class="legend"><span><i style="background:var(--us)"></i>' + tr('match.yourTeam') + '</span><span><i style="background:var(--them)"></i>' + tr('match.opponents') + '</span><span><i class="ring"></i>' + tr('common.you') + '</span></span></div>' +
      goalTimeline(m, goals) + list + note + '</section>';
  }

  function cmpUnit(s) { return unitSuffix(s.unit); }
  /** One "this match vs average" row: value, average, signed delta (arrow + sign, colored when good/bad), bar + average tick. */
  function cmpRow(s, v, a, pre) {
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
    return '<tr><th scope="row">' + esc(statLabel(s)) + '</th>' + (pre || '') + '<td class="v">' + fv(v, s.digits) + '</td><td class="a">' + fv(a, ad) + '</td>' +
      '<td class="dl ' + cls + '">' + txt + '</td><td class="b">' + bar + '</td></tr>';
  }
  /** cols (optional): {pre: label of an extra column before the value, v: label of the value, vLc: its legend}. */
  function cmpTable(rows, cols) {
    cols = cols || {};
    return '<div class="table-scroll"><table class="cmp-table"><thead><tr><th scope="col"><span class="sr-only">' + tr('mp.stat') + '</span></th>' +
      (cols.pre ? '<th scope="col">' + cols.pre + '</th>' : '') + '<th scope="col">' + (cols.v || tr('mp.thisMatch')) + '</th><th scope="col">' + tr('mp.average') + '</th>' +
      '<th scope="col">' + tr('mp.gap') + '</th><th scope="col" class="b"><span class="legend"><span><i style="background:var(--us)"></i>' + (cols.vLc || tr('mp.thisMatchLc')) + '</span><span><i class="tick"></i>' + tr('mp.averageLc') + '</span></span></th></tr></thead><tbody>' +
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
  function mpMovement(m, base, idp) {
    idp = idp || 'mp';
    var mv = m.movement, h = m.hits;
    if (!mv && !h) return '';
    var bm = base ? base.movement : {}, bh = base ? base.hits : {};
    function avgOf(o, k) {
      var ref = o && o[k];
      if (!ref || ref.n < 3 || ref.unit === null) return null;
      if (['raw', 'legacy', 'uu/s', 'km/h'].indexOf(ref.unit) >= 0 && ref.unit !== speedUnit(m)) return null;
      return ref.avg;
    }
    function forMatch(s) { return s.unit === 'raw' ? Object.assign({}, s, { unit: speedUnit(m) }) : s; }
    var nMv = bm && bm.avg_speed ? bm.avg_speed.n : 0, nH = bh && bh.avg_speed ? bh.avg_speed.n : 0;
    var nRef = Math.max(nMv, nH);
    var sub = nRef >= 3 ? tr('mp.mvSub', { v: nRef, mode: esc(modeLabel(m)) }) : tr('mp.mvNotEnough');
    var html = '<section class="card mp-card" aria-labelledby="' + idp + '-h-mv"><div class="mp-head"><h2 id="' + idp + '-h-mv">' + tr('mp.mvTitle') + '</h2><span class="card-meta">' + sub + '</span></div>';
    if (mv) {
      var known = wallKnown(mv);
      var rowThis = known ? splitRow(tr('mp.thisMatch'), mv.ground_pct, mv.wall_pct, mv.air_pct) : '';
      var rowAvg = known && avgOf(bm, 'ground_pct') != null ? splitRow(tr('mp.average'), bm.ground_pct.avg, bm.wall_pct.avg, bm.air_pct.avg) : '';
      if (rowThis) {
        html += '<div class="mp-split"><div class="mp-split-head"><span class="card-title">' + tr('mech.split') + '</span><span class="legend">' +
          splitNames().map(function (l, i) { return '<span><i style="background:' + T.series[i] + '"></i>' + l + '</span>'; }).join('') + '</span></div>' +
          rowThis + rowAvg + '</div>';
      } else if (!known) html += '<p class="mp-note">' + tr('mp.wallUnknown') + '</p>';
    }
    var rows = [];
    if (mv) MOVE_STATS.forEach(function (s) { if (!s.split) rows.push(cmpRow(forMatch(s), statValue(m, 'movement', s), avgOf(bm, s.key))); });
    if (h) {
      if (rows.length) rows.push('<tr class="sub"><th colspan="5" scope="rowgroup">' + tr('mp.ballHits') + '</th></tr>');
      HIT_STATS.forEach(function (s) { rows.push(cmpRow(forMatch(s), statValue(m, 'hits', s), avgOf(bh, s.key))); });
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
  /** The same match recorded by other server players: everyone's own stats side by side. */
  var SHARED_ROWS = [
    { src: 'me', key: 'score', lk: 'perf.score', digits: 0, higherBetter: true },
    { src: 'me', key: 'goals', lk: 'perf.goals', digits: 0, higherBetter: true },
    { src: 'me', key: 'assists', lk: 'perf.assists', digits: 0, higherBetter: true },
    { src: 'me', key: 'saves', lk: 'perf.saves', digits: 0, higherBetter: true },
    { src: 'me', key: 'shots', lk: 'perf.shots', digits: 0, higherBetter: true },
    { src: 'me', key: 'touches', lk: 'perf.touches', digits: 0, higherBetter: null },
    { src: 'me', key: 'demos', lk: 'perf.demos', digits: 0, higherBetter: true }
  ].concat(MOVE_STATS.filter(function (s) { return !s.split; }).map(function (s) { return Object.assign({ src: 'movement' }, s); }));
  function mpShared(m) {
    var w = (state.shared || {})[m.id];
    if (!w || !w.length) return '';
    var key = apiBase() + '/' + m.id, cp = state.sharedCopies[key];
    if (cp === undefined) { loadSharedCopies(m.id, key); cp = null; }
    var head = '<section class="card mp-card mp-shared" aria-labelledby="mp-h-shared"><div class="mp-head"><h2 id="mp-h-shared">' + tr('shared.title') + '</h2>' +
      '<span class="card-meta">' + tr('shared.sub') + '</span></div>';
    if (cp === 'err') return head + '<p class="muted small">' + tr('shared.failed') + '</p>' +
      '<button type="button" class="btn" data-shared-retry="' + m.id + '">' + tr('shared.retry') + '</button></section>';
    if (!cp) return head + '<p class="muted small">' + tr('common.loading') + '</p></section>';
    var self = state.route.player ? (viewedPlayer() || {}).name : tr('common.you');
    var cols = [{ name: self, m: m, us: true }].concat(cp.map(function (c) {
      return { name: c.player.name, handle: c.player.handle, m: c.match, us: c.match.my_team === m.my_team };
    }));
    var th = cols.map(function (c) {
      var n = esc(c.name) + (c.m.me && c.m.me.name && c.m.me.name !== c.name ? ' <small class="muted">' + esc(c.m.me.name) + '</small>' : '');
      if (c.handle && !(state.me && c.handle === state.me.handle)) n = '<a href="' + routeHash(c.handle, 'match/' + c.m.id) + '">' + n + '</a>';
      return '<th class="r"><span class="team-dot" style="background:var(--' + (c.us ? 'us' : 'them') + ')"></span>' + n + '</th>';
    }).join('');
    var rows = SHARED_ROWS.map(function (s) {
      var vals = cols.map(function (c) { return statValue(c.m, s.src, s); });
      if (!vals.some(isNum)) return '';
      var nums = vals.filter(isNum);
      var best = s.higherBetter == null || nums.length < 2 ? null : (s.higherBetter ? Math.max : Math.min).apply(null, nums);
      return '<tr><th scope="row">' + esc(statLabel(s)) + '</th>' + vals.map(function (v) {
        return '<td class="r num' + (best != null && v === best ? ' best' : '') + '">' + (isNum(v) ? fmtNum(v, s.digits) + cmpUnit(s) : '—') + '</td>';
      }).join('') + '</tr>';
    }).join('');
    return head + '<div class="table-scroll"><table class="data-table shared-table"><thead><tr><th></th>' + th + '</tr></thead><tbody>' + rows + '</tbody></table></div></section>';
  }
  function loadSharedCopies(id, key) {
    var gen = state.gen, copies = state.sharedCopies;
    if (copies[key] !== undefined && copies[key] !== 'err') return Promise.resolve(false);
    state.sharedCopies[key] = null;
    return apiFetch(apiBase() + '/matches/' + id + '/shared').then(function (cp) {
      if (gen !== state.gen || copies !== state.sharedCopies) return;
      state.sharedCopies[key] = Array.isArray(cp) ? cp : [];
    }).catch(function (e) {
      if (gen !== state.gen || copies !== state.sharedCopies) return;
      console.warn('shared copies', e);
      state.sharedCopies[key] = 'err';
    }).then(function () {
      if (gen === state.gen && copies === state.sharedCopies && state.route.view === 'match' && state.route.id === id) renderMatchPage();
    });
  }
  function retrySharedCopies(id) {
    var key = apiBase() + '/' + id;
    if (state.route.view !== 'match' || state.route.id !== id || state.sharedCopies[key] !== 'err') return Promise.resolve(false);
    var request = loadSharedCopies(id, key);
    renderMatchPage();
    return request;
  }
  function renderMatchPage() {
    var box = $('#view-match');
    var id = state.route.id;
    if (!state.loaded) { box.innerHTML = '<div class="card mp-missing"><p class="muted">' + tr('mp.loading') + '</p></div>'; return; }
    var m = id != null ? findMatch(id) : null;
    if (!m && id != null && switchToMatchProfile(id)) m = findMatch(id);
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
    box.innerHTML = mpNav(nb) + mpHero(m, ctx) + mpScoreboard(m) + mpShared(m) + '<div class="mp-grid"><div class="mp-col">' + left + '</div><div class="mp-col">' + right + '</div></div>';
  }
  function bindMatchView() {
    var v = $('#view-match');
    v.addEventListener('change', function (e) {
      var sel = e.target.closest('[data-tag-for]');
      if (sel) updateTag(+sel.dataset.tagFor, sel.value, sel);
    });
    v.addEventListener('click', function (e) {
      var retry = e.target.closest('[data-shared-retry]');
      if (retry) return retrySharedCopies(+retry.dataset.sharedRetry);
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
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = tr(s.live.training ? 'st.inTraining' : 'st.inMatch'); }
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
      if (s.in_match && s.live) { pill.className = 'pill pill-live'; label.textContent = tr(s.live.training ? 'st.inTraining' : 'st.inMatch'); }
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
    // Local-only API field: never project a workstation's queue onto a hosted/player view.
    if (s && !state.statusError && state.mode === 'local' && !state.dataFor && !readOnly()) {
      var delivery = deliveryMessage(s.delivery);
      if (delivery) w.push(warning(delivery.attention ? '!' : 'i', tr('st.deliveryTitle'), esc(delivery.text), !delivery.attention));
    }
    $('#warnings').innerHTML = w.join('');
    var ver = s && s.version ? 'Rocket Tracker v' + stripV(s.version) : 'Rocket Tracker';
    $('#footer-version').textContent = ver + (MOCK ? ' · ' + tr('st.demo') : '');

    var lvNav = $('[data-nav="live"]');
    if (lvNav) lvNav.classList.toggle('is-live', !!liveNow());
    if (state.route.view === 'live') renderLive();
    var live = $('#live');
    if (state.route.view === 'live') {
      live.hidden = true;
      live.innerHTML = '';
    } else if (s && s.in_match && s.live && s.live.training) {
      var since = Date.parse(s.live.started_at);
      live.hidden = false;
      live.innerHTML = '<div><div class="live-tag"><span class="dot"></span>' + tr('st.training') + (state.dataFor ? ' · ' + esc((viewedPlayer() || {}).name || state.dataFor) : '') + '</div>' +
        '<div class="live-meta">' + esc(prettyArena(s.live.arena)) + (isNum(since) ? ' · ' + tr('st.trainingFor', { v: fmtHours((Date.now() - since) / 1000) }) : '') + '</div></div>' +
        (state.dataFor ? '' : '<div class="live-me"><a href="' + href('training') + '">' + tr('st.trainingPlan') + ' <span aria-hidden="true">→</span></a></div>');
    } else if (s && s.in_match && s.live) {
      var Lv = s.live, me = Lv.me || {};
      live.hidden = false;
      live.innerHTML =
        '<div><div class="live-tag"><span class="dot"></span>' + tr('st.live') + (state.dataFor ? ' · ' + esc((viewedPlayer() || {}).name || state.dataFor) : '') + '</div><div class="live-meta">' + esc(Lv.mode || '') + ' · ' + esc(prettyArena(Lv.arena)) +
          (Lv.me ? ' · ' + tr(Lv.my_team === 1 ? 'st.teamOrange' : 'st.teamBlue') : '') + '</div></div>' +
        '<div class="live-score" aria-label="' + esc(tr('perf.score')) + '"><span class="us" title="' + esc(tr('match.yourTeam')) + '">' + fmtNum(Lv.team_score) + '</span>' +
        '<span class="clock' + (Lv.overtime ? ' ot' : '') + '">' + (Lv.overtime ? '+' : '') + fmtClock(Lv.time_seconds) + '</span>' +
        '<span class="them" title="' + esc(tr('match.opponents')) + '">' + fmtNum(Lv.opp_score) + '</span></div>' +
        '<div class="live-me">' + ['score', 'goals', 'assists', 'saves', 'shots']
          .map(function (k) { return '<span><b>' + fmtNum(me[k]) + '</b>' + tr('perf.' + k) + '</span>'; }).join('') + '</div>' +
        liveMet(Lv) + '<a class="live-open" href="' + href('live') + '">' + tr('live.open') + ' <span aria-hidden="true">→</span></a>';
    } else {
      live.hidden = true;
      live.innerHTML = '';
    }
  }
  var RELEASES_URL = 'https://github.com/KcivazB/rocket-tracker/releases/latest';
  // versionBelow: a and b are x.y.z (optional "v"); false when either is not.
  function versionBelow(a, b) {
    var pa = stripV(a).split('.'), pb = stripV(b).split('.');
    if (pa.length !== 3 || pb.length !== 3 || pa.concat(pb).some(function (x) { return !/^\d+$/.test(x); })) return false;
    for (var i = 0; i < 3; i++) {
      if (+pa[i] !== +pb[i]) return +pa[i] < +pb[i];
    }
    return false;
  }
  /** Players of the live match already met in the history (by platform id: bots have none). */
  function liveMet(Lv) {
    var idx = metIndex(), rows = [];
    (Lv.players || []).forEach(function (p) {
      var rec = p.primary_id && idx[p.primary_id.toLowerCase()];
      if (!rec) return;
      var opp = p.team !== Lv.my_team, r = opp ? rec.opp : rec.mate;
      if (!r.w && !r.l) return;
      rows.push('<li class="' + (opp ? 'met-opp' : 'met-mate') + '"><b>' + esc(p.name) + '</b> ' +
        esc(tr(opp ? 'live.metOpp' : 'live.metMate', { n: r.w + r.l, w: r.w, l: r.l })) + '</li>');
    });
    return rows.length ? '<div class="live-met"><span>' + esc(tr('live.met')) + '</span><ul>' + rows.join('') + '</ul></div>' : '';
  }
  // metIndex: platform id -> wins/losses against (opp) and with (mate) that player, rebuilt when the matches change.
  var metCache = { all: null, idx: null };
  function metIndex() {
    if (metCache.all === state.all) return metCache.idx;
    var idx = {};
    state.all.forEach(function (m) {
      if (!m.online || (m.result !== 'win' && m.result !== 'loss')) return;
      (m.players || []).forEach(function (p) {
        var id = (p.primary_id || '').toLowerCase();
        var uid = id.split('|')[1];
        if (p.is_me || !uid || uid === '0') return; // bots have no platform id
        var rec = idx[id] || (idx[id] = { opp: { w: 0, l: 0 }, mate: { w: 0, l: 0 } });
        var r = p.team === m.my_team ? rec.mate : rec.opp;
        if (m.result === 'win') r.w++; else r.l++;
      });
    });
    metCache = { all: state.all, idx: idx };
    return idx;
  }
  /* ---------- in-game view (#/live) ---------- */
  /** The match being played (not a training), or null. */
  function liveNow() {
    var s = state.status;
    return s && s.in_match && s.live && !s.live.training ? s.live : null;
  }
  function autoLive() {
    try { return localStorage.getItem('rt.autoLive') !== '0'; } catch (e) { return true; }
  }
  /** Opens the in-game view when one of my matches starts (option), unless another player's pages are shown. */
  function autoOpenLive() {
    var on = !!liveNow();
    if (on && state.wasLive === false && autoLive() && !state.route.player && !state.dataFor && state.route.view !== 'live' && state.route.view !== 'players' &&
        !document.querySelector('dialog[open]')) {
      location.hash = href('live');
    }
    state.wasLive = on;
  }
  function liveClock(Lv) {
    return Lv.overtime ? '+' + fmtClock(Lv.time_seconds) : fmtClock(Lv.time_seconds);
  }
  /** "Leading by 2", "Level", "Trailing by 1". */
  function liveStateLabel(diff) {
    return diff > 0 ? tr('live.leading', { n: diff }) : diff < 0 ? tr('live.trailing', { n: -diff }) : tr('live.level');
  }
  function renderLive() {
    var box = $('#live-body');
    if (!box) return;
    $('#live-auto-wrap').hidden = readOnly();
    $('#live-auto').checked = autoLive();
    syncFullscreenBtn();
    var who = state.dataFor ? (viewedPlayer() || {}).name || state.dataFor : '';
    var Lv = liveNow();
    if (!Lv) {
      $('#live-sub').textContent = who || tr('live.sub');
      box.innerHTML = liveIdle();
      return;
    }
    var m = liveAsMatch(Lv);
    var team = Lv.me ? tr(Lv.my_team === 1 ? 'st.teamOrange' : 'st.teamBlue') : '';
    $('#live-sub').textContent = [who, modeLabel(m), prettyArena(Lv.arena), team].filter(Boolean).join(' · ');
    var base = baselineOf(state.all, Lv.mode, 'Soccar', 50, Lv.regulation_s === DUEL_REG_S, Lv.overtime ? null : regulationS(m));
    var at = Date.parse(Lv.started_at);
    var sess = currentSession(state.all, isNum(at) ? at : Date.now(), Lv.mode);
    var mv = Lv.me ? mpMovement(m, base, 'lv') : '';
    var perf = livePerf(m, base);
    box.innerHTML = liveHero(Lv, m) +
      '<div class="lv-grid">' + liveOddsCard(Lv, m) + liveSessionCard(sess, Lv.mode, true) + '</div>' + liveMomentumCard(m) +
      mpScoreboard(m, 'lv') + liveMet(Lv) +
      '<div class="mp-grid"><div class="mp-col">' + mpGoals(m, 'lv') + (mv ? perf : '') + '</div><div class="mp-col">' + (mv || perf) + '</div></div>';
  }
  function liveHero(Lv, m) {
    var mine = Lv.my_team === 1 ? 'orange' : 'blue', theirs = Lv.my_team === 1 ? 'blue' : 'orange';
    var diff = m.team_score - m.opp_score, me = Lv.me;
    var side = function (cls, label, n) {
      return '<div class="lv-team ' + cls + '"><span class="lv-tl">' + label + '</span><span class="lv-n num">' + fmtNum(n) + '</span></div>';
    };
    return '<section class="card lv-hero mine-' + mine + '" aria-label="' + esc(tr('perf.score')) + '">' +
      side(mine, tr('match.yourTeam'), m.team_score) +
      '<div class="lv-mid"><span class="lv-clock num' + (Lv.overtime ? ' ot' : '') + '">' + liveClock(Lv) + '</span>' +
        '<span class="lv-state ' + (diff > 0 ? 'up' : diff < 0 ? 'down' : '') + '">' + (Lv.overtime ? esc(tr('goals.overtime')) + ' · ' : '') + esc(liveStateLabel(diff)) + '</span></div>' +
      side(theirs, tr('match.opponents'), m.opp_score) +
      (me ? '<div class="lv-me">' + SB_COLS.map(function (k) { return '<span><b class="num">' + fmtNum(me[k]) + '</b>' + tr('perf.' + k) + '</span>'; }).join('') + '</div>' : '') +
      (Lv.partial ? '<p class="mp-note">' + tr('live.partial') + '</p>' : '') +
      '</section>';
  }
  var LIVE_MIN_N = 15; // matches needed before stating a win rate (below, the interval spans most of 0–100 %)
  /** What my matches of this mode became from the same score at the same time. */
  function liveOddsCard(Lv, m) {
    var el = num(Lv.elapsed_s), diff = m.team_score - m.opp_score;
    var o = liveOdds(state.all, Lv.mode, el, diff, Lv.overtime, Lv.regulation_s === DUEL_REG_S);
    var head = '<section class="card lv-card" aria-labelledby="lv-h-odds"><div class="mp-head"><h2 id="lv-h-odds">' + tr('live.oddsTitle') + '</h2></div>';
    var when = Lv.overtime ? tr('live.inOt') : el < 1 ? tr('live.atKickoff') : tr('live.atTime', { state: liveStateLabel(diff).toLowerCase(), t: fmtClock(Lv.time_seconds) });
    if (o.n < LIVE_MIN_N) return head + '<p class="lv-when">' + esc(when) + '</p><p class="muted small">' + tr('live.oddsFew', { mode: esc(Lv.mode), v: plural(o.n, 'match') }) + '</p></section>';
    var cls = o.wr >= 0.55 ? 'pos' : o.wr <= 0.45 ? 'neg' : '';
    return head + '<p class="lv-when">' + esc(when) + '</p>' +
      '<div class="lv-big ' + cls + ' num">' + fmtPct(o.wr) + '</div>' +
      '<div class="goal-meter lv-meter" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(o.wr * 100) + '"><span style="width:' + (o.wr * 100).toFixed(1) + '%"></span></div>' +
      (o.ci ? '<p class="small lv-range">' + tr('live.oddsRange', { lo: fmtPct(o.ci.lo), hi: fmtPct(o.ci.hi) }) + '</p>' : '') +
      '<p class="muted small">' + tr(o.overtime ? 'live.oddsOt' : 'live.oddsText', { w: o.wins, v: plural(o.n, 'match'), mode: esc(Lv.mode) }) + '</p></section>';
  }
  /** How each team is shooting and scoring in the last minutes against the rest of the match. */
  function liveMomentumCard(m) {
    if (!Array.isArray(m.events)) return '';
    var mo = computeMomentum(m, num(m.duration_s));
    var head = '<section class="card mp-card lv-momentum" aria-labelledby="lv-h-mo"><div class="mp-head"><h2 id="lv-h-mo">' + tr('live.moTitle') + '</h2>' +
      '<span class="card-meta">' + tr('live.moSub', { v: MOMENTUM_WIN_S }) + '</span></div>';
    if (!mo) return head + '<p class="muted small">' + tr('live.moSoon') + '</p></section>';
    var cell = function (x) { return '<td class="r num">' + x + '</td>'; };
    var perMin = function (s) { return s ? fmtNum(s.shotsPerMin, 1) : '—'; };
    var conv = function (s) { return s && s.shots ? fmtPct(s.conversion) : '—'; };
    var cnt = function (s, k) { return s ? fmtNum(s[k]) : '—'; };
    var rows = [
      [tr('live.moShotsMin'), perMin],
      [tr('perf.shots'), function (s) { return cnt(s, 'shots'); }],
      [tr('perf.goals'), function (s) { return cnt(s, 'goals'); }],
      [tr('live.moConv'), conv],
      [tr('perf.saves'), function (s) { return cnt(s, 'saves'); }],
      [tr('live.moUnconverted'), function (s) { return cnt(s, 'unconverted'); }]
    ].map(function (r) {
      return '<tr><th scope="row">' + esc(r[0]) + '</th>' + cell(r[1](mo.us.recent)) + cell(r[1](mo.us.before)) + cell(r[1](mo.them.recent)) + cell(r[1](mo.them.before)) + '</tr>';
    }).join('');
    var sub = function (k) { return '<th scope="col" class="r">' + tr(k, { v: MOMENTUM_WIN_S }) + '</th>'; };
    var table = '<div class="table-scroll"><table class="data-table lv-mo-table"><thead>' +
      '<tr><th></th><th scope="colgroup" colspan="2" class="r"><span class="team-dot" style="background:var(--us)"></span>' + tr('match.yourTeam') + '</th>' +
      '<th scope="colgroup" colspan="2" class="r"><span class="team-dot" style="background:var(--them)"></span>' + tr('match.opponents') + '</th></tr>' +
      '<tr><th></th>' + sub('live.moRecent') + sub('live.moBefore') + sub('live.moRecent') + sub('live.moBefore') + '</tr></thead><tbody>' + rows + '</tbody></table></div>';
    var last = '<p class="small">' + tr('live.moLastMin', { us: fmtNum(mo.us.lastMin), them: fmtNum(mo.them.lastMin) }) + '</p>';
    var sig = mo.signals.length ? '<ul class="lv-list lv-signals">' + mo.signals.map(function (s) {
      var good = /^us(Rising|Sharper)|^them(Fading|Blunted)/.test(s.key);
      return '<li class="' + (good ? 'pos' : 'neg') + '">' + esc(tr('live.mo.' + s.key, {
        n: fmtNum(s.shots), e: fmtNum(s.expected, 1), v: fmtPct(s.conv), b: fmtPct(s.before), s: fmtNum(s.saves)
      })) + '</li>';
    }).join('') + '</ul>' : '<p class="muted small">' + tr(mo.elapsed - mo.win >= mo.win ? 'live.moSteady' : 'live.moEarly', { v: 2 * MOMENTUM_WIN_S / 60 }) + '</p>';
    return head + table + last + sig + '</section>';
  }
  /** The session going on: record, streak (tilt warning), my win rate from this match number on, rank and daily goal. */
  function liveSessionCard(sess, mode, playing) {
    var rows = [];
    var html = '<section class="card lv-card" aria-labelledby="lv-h-sess"><div class="mp-head"><h2 id="lv-h-sess">' + tr('live.sessTitle') + '</h2>' +
      (sess ? '<span class="card-meta">' + tr('live.sessSince', { v: esc(fmtTime(sess.start)) }) + '</span>' : '') + '</div>';
    if (!sess) rows.push('<li>' + tr(playing ? 'live.sessFirst' : 'live.sessNone') + '</li>');
    else {
      var r = sess.record;
      rows.push('<li><span>' + tr(playing ? 'live.sessMatchNo' : 'live.sessPlayed', { n: playing ? sess.next : sess.n }) + '</span><b>' + wlShort(r.wins, r.losses) + (r.n ? ' · ' + fmtPct(r.wr) : '') + '</b></li>');
      if (sess.mode.n && sess.mode.n !== r.n) rows.push('<li><span>' + esc(mode) + '</span><b>' + wlShort(sess.mode.wins, sess.mode.losses) + '</b></li>');
      var st = sess.streak;
      if (st && st.len >= 2) {
        var configuredTilt = (state.config || {}).tilt_streak;
        var tilt = isNum(configuredTilt) ? configuredTilt : 3;
        rows.push('<li><span>' + tr('kpi.streak') + '</span><b class="' + (st.type === 'win' ? 'pos' : 'neg') + '">' + st.len + ' ' + tr(st.type === 'win' ? 'kpi.streakWins' : 'kpi.streakLosses', { n: st.len }) + '</b></li>');
        if (tilt > 0 && st.type === 'loss' && st.len >= tilt) rows.push('<li class="lv-warn">' + tr('live.tiltWarn', { n: st.len }) + '</li>');
      }
      // Fatigue: how I do from this match number on in my sessions, when clearly below my usual rate.
      var nx = playing ? sess.next : sess.n + 1;
      if (nx >= 4 && sess.later.n >= 10 && isNum(sess.overall.wr) && sess.later.wr <= sess.overall.wr - 0.05) {
        rows.push('<li class="lv-warn">' + tr('live.fatigue', { n: nx, v: fmtPct(sess.later.wr), all: fmtPct(sess.overall.wr) }) + '</li>');
      }
    }
    var rk = rankSummary(ranksForProfile(state.ranks, rankDestination())).find(function (x) { return x.playlist === mode; });
    if (rk && (isNum(rk.tier) || isNum(rk.mmr))) {
      rows.push('<li><span>' + tr('live.rank', { mode: esc(mode) }) + ' · ' + esc(rankAccountLabel(rk.profile)) + '</span><b>' + (isNum(rk.tier) ? rankIcon(rk.tier, 20) + esc(rankName(rk.tier)) : '') +
        (isNum(rk.mmr) ? (isNum(rk.tier) ? ' · ' : '') + fmtNum(rk.mmr) + ' MMR' : '') + '</b></li>');
    }
    var g = computeGoal(state.all, state.config && state.config.goal, null, profileManual());
    if (!g.before && !g.after && (g.mode === 'all' || g.mode === mode)) {
      rows.push('<li><span>' + tr('live.goalToday', { mode: esc(goalModeLabel(g.mode)) }) + '</span><b' + (g.today.count >= g.daily ? ' class="pos"' : '') + '>' + fmtNum(g.today.count) + ' / ' + fmtNum(g.daily) + '</b></li>');
    }
    return html + '<ul class="lv-list">' + rows.join('') + '</ul></section>';
  }
  /** My numbers so far, their pace over a regulation match and my average in this mode. */
  function livePerf(m, base) {
    if (!m.me) return '';
    var head = '<section class="card mp-card" aria-labelledby="lv-h-perf"><div class="mp-head"><h2 id="lv-h-perf">' + tr('mp.perfTitle') + '</h2>';
    if (!base || base.n < 3) return head + '</div><p class="muted small">' + tr('live.perfFew', { mode: esc(modeLabel(m)), v: base ? base.n : 0 }) + '</p></section>';
    var el = num(m.duration_s);
    var rows = PERF_STATS.map(function (s) {
      var now = statValue(m, 'me', s);
      var v = m.overtime ? now : livePace(now, el, false, regulationS(m));
      return cmpRow(Object.assign({}, s, { digits: m.overtime || s.key === 'score' ? 0 : 1, avgDigits: s.key === 'score' ? 0 : 1 }), v, base.me[s.key].avg, '<td class="v">' + fmtNum(now) + '</td>');
    });
    return head + '<span class="card-meta">' + tr(m.overtime ? 'live.perfRawSub' : el < 60 ? 'live.paceSoon' : 'live.perfSub', { v: base.n, mode: esc(modeLabel(m)), minutes: fmtNum(regulationS(m) / 60, 1) }) + '</span></div>' +
      cmpTable(rows, { pre: tr('live.now'), v: m.overtime ? tr('mp.thisMatch') : tr('live.pace', { minutes: fmtNum(regulationS(m) / 60) }), vLc: tr(m.overtime ? 'mp.thisMatchLc' : 'live.paceLc') }) + '</section>';
  }
  /** No match going on: the last match of the session and the session, waiting for the next one. */
  function liveIdle() {
    var s = state.status;
    var training = !!(s && s.in_match && s.live && s.live.training);
    var wait = training ? tr('live.idleTraining') : state.statusError ? tr('st.unreachable')
      : s && (s.connected || (s.agent && s.agent.online)) ? tr('live.idleWaiting') : tr('live.idleNoGame');
    var html = '<section class="card lv-idle"><span class="lv-idle-dot' + (training ? ' on' : '') + '" aria-hidden="true"></span><div><b>' + tr('live.idleTitle') + '</b>' +
      '<p class="muted small">' + esc(wait) + '</p></div></section>';
    var sess = currentSession(state.all, Date.now(), '');
    if (!sess) return html;
    var last = sess.matches[sess.matches.length - 1], me0 = me(last);
    var res = last.result ? resText(last.result) : null;
    return html + '<div class="lv-grid"><section class="card lv-card lv-last" aria-labelledby="lv-h-last"><div class="mp-head"><h2 id="lv-h-last">' + tr('live.lastTitle') + '</h2>' +
      '<span class="card-meta">' + esc(modeLabel(last)) + ' · ' + esc(prettyArena(last.arena)) + '</span></div>' +
      '<div class="lv-last-score"><span class="mp-res ' + esc(last.result || '') + '">' + (res ? res[1] : tr('res.unknown')) + '</span>' +
      '<span class="lv-last-n num">' + fmtNum(last.team_score) + ' – ' + fmtNum(last.opp_score) + '</span></div>' +
      '<div class="lv-me">' + SB_COLS.map(function (k) { return '<span><b class="num">' + fmtNum(me0[k]) + '</b>' + tr('perf.' + k) + '</span>'; }).join('') + '</div>' +
      '<p class="lv-link"><a href="' + href('match/' + last.id) + '">' + tr('live.lastOpen') + ' <span aria-hidden="true">→</span></a></p></section>' +
      liveSessionCard(sess, last.mode, false) + '</div>';
  }
  function fullscreenOn() { return !!(document.fullscreenElement || document.webkitFullscreenElement); }
  function syncFullscreenBtn() {
    var el = $('#view-live');
    $('#live-fs').hidden = !(el.requestFullscreen || el.webkitRequestFullscreen);
    $('#live-fs-label').textContent = tr(fullscreenOn() ? 'live.fsExit' : 'live.fs');
  }
  function bindLive() {
    $('#live-fs').addEventListener('click', function () {
      var el = $('#view-live');
      if (fullscreenOn()) { (document.exitFullscreen || document.webkitExitFullscreen).call(document); return; }
      var p = (el.requestFullscreen || el.webkitRequestFullscreen).call(el);
      if (p && p.catch) p.catch(function (e) { toast(tr('live.fsFailed', { v: e.message }), true); });
    });
    ['fullscreenchange', 'webkitfullscreenchange'].forEach(function (ev) { document.addEventListener(ev, syncFullscreenBtn); });
    $('#live-auto').addEventListener('change', function (e) {
      try { localStorage.setItem('rt.autoLive', e.target.checked ? '1' : '0'); } catch (err) { /* storage unavailable */ }
    });
    // F: full screen on the in-game view.
    document.addEventListener('keydown', function (e) {
      if (state.route.view !== 'live' || (e.key !== 'f' && e.key !== 'F') || e.altKey || e.ctrlKey || e.metaKey) return;
      var t = e.target;
      if (t && t.closest && t.closest('input, select, textarea, [contenteditable], dialog')) return;
      $('#live-fs').click();
    });
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
    $('#f-duel').value = f.duel;
    if ($('#f-duel').value !== f.duel) { f.duel = DEFAULT_FILTERS.duel; $('#f-duel').value = f.duel; }
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
    $('#f-duel').addEventListener('change', function (e) { state.filters.duel = e.target.value; onFiltersChanged(); });
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
    $('#view-training').hidden = view !== 'training';
    $('#view-live').hidden = view !== 'live';
    $('#export-csv').href = apiBase() + '/export.csv';
    $('#import-btn').hidden = readOnly();
    $('#recent-all').href = href('history');
    $$('[data-goal-edit]').forEach(function (el) { el.hidden = readOnly(); });
    if (view !== 'dash') {
      $('#onboarding').hidden = true;
      $('#dashboard').hidden = true;
      try {
        if (view === 'history') renderHistory(); else if (view === 'players') renderPlayers(); else if (view === 'training') renderTraining();
        else if (view === 'live') renderLive(); else renderMatchPage();
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
    renderRanks();
    $$('#dashboard > :not(#goal-section):not(#rank-section)').forEach(function (el) { if (!has) el.hidden = true; else if (el.id !== 'no-results' && el.id !== 'content') el.hidden = false; });
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
      var steps = [renderKpis, renderWinFactors, renderProgression, renderActivity, renderGoals, renderSituations, renderMental, renderTime, renderMechanics, renderField, renderFeed, renderArenas, renderMates, renderDuos];
      steps.forEach(function (fn) {
        try { fn(ms); } catch (e) { console.error('render failed:', fn.name, e); }
      });
    }
    renderMatches(ms);
  }

  /* ---------- data loading ---------- */
  // Each loader drops its answer when the viewed player changed meanwhile (state.gen).
  /** Server mode: which of the viewed player's matches other accounts recorded too (id -> with[]). */
  function loadShared() {
    var gen = state.gen, mark = state.status && state.status.shared_rev;
    if (!isServer() || MOCK) { state.shared = {}; return Promise.resolve(); }
    if (state.sharedBusy) return Promise.resolve(false);
    state.sharedBusy++;
    return apiFetch(apiBase() + '/shared').then(function (list) {
      if (gen !== state.gen) return false;
      var map = {};
      (list || []).forEach(function (s) { map[s.id] = s.with || []; });
      state.sharedBusy--;
      state.shared = map;
      state.sharedCopies = {};
      state.lastShared = mark == null ? null : mark;
      state.sharedFailed = false;
      renderAll();
      return true;
    }).catch(function (e) {
      if (gen !== state.gen) return false;
      state.sharedBusy--;
      state.sharedFailed = true;
      state.lastShared = null;
      console.warn('shared', e);
      return false;
    });
  }
  function loadRanks() {
    var gen = state.gen, mark = dataMarker(state.status, 'match_count');
    if (MOCK) { state.ranks = mockRanks(); return Promise.resolve(); }
    state.ranksBusy++;
    return apiFetch(apiBase() + '/ranks').then(function (rs) {
      if (gen !== state.gen) return false;
      state.ranksBusy--;
      state.ranks = annotateRanks(Array.isArray(rs) ? rs : []);
      state.lastRanks = mark;
      state.ranksFailed = false;
      renderRanks();
      return true;
    }).catch(function (e) {
      if (gen !== state.gen) return false;
      state.ranksBusy--;
      state.lastRanks = null;
      state.ranksFailed = true;
      console.warn('ranks', e);
      return false;
    });
  }
  /**
   * Loads the matches; resolves to true on success. state.lastCount is what was loaded (the
   * status marker seen before the request, see dataMarker): only set on success, so a failed
   * load is retried at the next poll. Only the first failure of a row shows a toast.
   */
  function loadMatches() {
    var gen = state.gen, mark = dataMarker(state.status, 'match_count');
    if (!state.sharedBusy) loadShared();
    if (!state.ranksBusy) loadRanks();
    state.matchesBusy++;
    return apiFetch(apiBase() + '/matches').then(function (data) {
      if (gen !== state.gen) return false;
      state.matchesBusy--;
      state.allRaw = Array.isArray(data) ? data.filter(function (m) { return m && typeof m === 'object'; }) : [];
      state.lastCount = mark;
      state.matchesFailed = false;
      setupProfiles();
      state.loaded = true;
      renderAll();
      return true;
    }).catch(function (e) {
      if (gen !== state.gen) return false;
      state.matchesBusy--;
      console.warn('matches', e);
      state.lastCount = null;
      state.loaded = true;
      if (!state.all.length) renderAll();
      if (!state.matchesFailed) toast(tr('toast.loadFailed', { v: e.message }), true);
      state.matchesFailed = true;
      return false;
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
      autoOpenLive();
      // New data (data_rev, else the counts), or the last load failed: reload, unless a load is
      // already running (the first one included: state.loaded is set when it ends).
      var pending = [], mark = dataMarker(s, 'match_count');
      if (state.loaded && isServer() && !state.sharedBusy && (state.sharedFailed ||
          !s || s.shared_rev == null || needsReload(s.shared_rev, state.lastShared))) pending.push(loadShared());
      if (state.loaded && !state.matchesBusy && needsReload(mark, state.lastCount)) pending.push(loadMatches());
      // Auxiliary reads have independent success markers: successful matches do not acknowledge
      // failed ranks or manual entries, and data_rev also covers edits that keep counts unchanged.
      if (state.loaded && !state.ranksBusy && (state.ranksFailed || needsReload(mark, state.lastRanks))) pending.push(loadRanks());
      if (state.loaded && !state.manualBusy && (state.manualFailed || needsReload(mark, state.lastManual))) pending.push(loadManual());
      if (!state.trainingsBusy && needsReload(dataMarker(s, 'training_count'), state.lastTrainings)) pending.push(loadTrainings());
      return Promise.all(pending);
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
    var gen = state.gen;
    Object.assign(state, { all: [], allRaw: [], profiles: [], filtered: [], manual: [], lastManual: null, manualBusy: 0, manualFailed: false,
      ranks: [], lastRanks: null, ranksBusy: 0, ranksFailed: false, shared: {}, lastShared: null, sharedBusy: 0, sharedFailed: false, sharedCopies: {}, trainings: [], lastTrainings: null, trainingsBusy: 0, planDraft: null, config: null, status: null,
      lastCount: null, matchesBusy: 0, matchesFailed: false, loaded: false, open: {}, shown: PAGE_SIZE, histShown: HIST_PAGE });
    renderStatus();
    loadTrainings();
    // lastCount / lastTrainings are set by the loads themselves, on success only.
    return Promise.all([pollStatus(), Promise.all([loadConfig(), loadManual()]).then(function () {
      if (gen === state.gen) return loadMatches();
    })]);
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
  /** A match of another of the player's accounts (link from Discord, history of "all"...): show that account. */
  function switchToMatchProfile(id) {
    var m = state.allRaw.find(function (x) { return x.id === id; });
    if (!m || state.profile === 'all') return false;
    selectProfile(profileKey(m));
    return true;
  }
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
      state.allRaw = state.allRaw.filter(function (x) { return x.id !== id; });
      setupProfiles();
      delete state.open[id];
      // Count marker (older server): one less, not to reload. A data_rev marker changes anyway.
      if (isNum(state.lastCount)) state.lastCount = Math.max(0, state.lastCount - 1);
      toast(tr('toast.deleted'));
      if (after) after(); else renderAll();
    }).catch(function (e) { toast(tr('toast.deleteFailed', { v: e.message }), true); });
  }

  /* ---------- settings ---------- */
  var currentConfig = null;
  function tiltOptions(sel) {
    var ns = [0, 2, 3, 4, 5];
    if (ns.indexOf(sel) < 0) ns.push(sel); // a value set in config.json
    return ns.map(function (n) {
      return '<option value="' + n + '"' + (n === sel ? ' selected' : '') + '>' + esc(n ? tr('set.tiltN', { n: n }) : tr('set.tiltOff')) + '</option>';
    }).join('');
  }
  function openSettings() {
    var dlg = $('#settings');
    var form = $('#settings-form');
    var msg = $('#settings-msg');
    msg.textContent = tr('common.loading'); msg.className = 'form-msg';
    form.elements.default_tag.innerHTML = tagOptions('ranked');
    form.elements.tilt_streak.innerHTML = tiltOptions(3);
    form.elements.lang.value = I18N.getLang();
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
    apiFetch('/api/config').then(function (c) {
      currentConfig = c || {};
      form.elements.player_names.value = (currentConfig.player_names || []).join(', ');
      form.elements.player_ids.value = (currentConfig.player_ids || []).join(', ');
      form.elements.default_tag.innerHTML = tagOptions(currentConfig.default_tag || 'ranked');
      form.elements.tilt_streak.innerHTML = tiltOptions(isNum(currentConfig.tilt_streak) ? currentConfig.tilt_streak : 3);
      form.elements.discord_webhook.value = currentConfig.discord_webhook || '';
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
    $('#discord-test').addEventListener('click', function () {
      var msg = $('#settings-msg'), hook = form.elements.discord_webhook.value.trim();
      if (!hook) { msg.textContent = tr('set.discordEmpty'); msg.className = 'form-msg err'; return; }
      msg.textContent = tr('common.loading'); msg.className = 'form-msg';
      apiFetch('/api/discord/test', { method: 'POST', body: JSON.stringify({ webhook: hook }) }).then(function () {
        msg.textContent = tr('set.discordSent'); msg.className = 'form-msg';
      }).catch(function (e) { msg.textContent = tr('set.discordFailed', { v: e.message }); msg.className = 'form-msg err'; });
    });
    $$('[data-open-settings]').forEach(function (b) { b.addEventListener('click', openSettings); });
    $$('[data-close]', dlg).forEach(function (b) { b.addEventListener('click', function () { dlg.close ? dlg.close() : dlg.removeAttribute('open'); }); });
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var split = function (s) { return s.split(',').map(function (x) { return x.trim(); }).filter(Boolean); };
      var cfg = Object.assign({}, currentConfig || {}, {
        player_names: split(form.elements.player_names.value),
        player_ids: split(form.elements.player_ids.value),
        default_tag: form.elements.default_tag.value,
        tilt_streak: parseInt(form.elements.tilt_streak.value, 10) || 0,
        discord_webhook: form.elements.discord_webhook.value.trim(),
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

  /* ---------- training page (#/training) ---------- */
  var BREAK_EVERY = [0, 2, 3, 4, 5, 6, 8, 10];
  var BREAK_MINUTES = [3, 5, 10, 15];
  var WARMUP_MINUTES = [10, 15, 20, 30];
  /** Loads the trainings; resolves to true on success. Like loadMatches, state.lastTrainings is only set on success. */
  function loadTrainings() {
    var gen = state.gen, mark = dataMarker(state.status, 'training_count');
    state.trainingsBusy++;
    var req = MOCK ? mockFetch('/api/trainings', {}) : apiFetch(apiBase() + '/trainings');
    return req.then(function (ts) {
      if (gen !== state.gen) return false;
      state.trainingsBusy--;
      state.trainings = Array.isArray(ts) ? ts : [];
      state.lastTrainings = mark;
      if (state.route.view === 'training') renderTraining();
      return true;
    }).catch(function (e) {
      if (gen !== state.gen) return false;
      state.trainingsBusy--;
      console.warn('trainings', e);
      state.lastTrainings = null;
      return false;
    });
  }
  /** The trainings of the account shown (those without an account count for every account). */
  function profileTrainings() {
    return (state.trainings || []).filter(function (t) { return state.profile === 'all' || !t.profile || t.profile === state.profile; });
  }
  function warmupCfg() { return Object.assign({ minutes: 15, blocks: [] }, (state.config || {}).warmup || {}); }
  /** The plan shown: the saved one, else one built from the stats. */
  function currentPlan() {
    if (state.planDraft) return state.planDraft;
    var w = warmupCfg();
    if (w.blocks && w.blocks.length) return w.blocks.map(function (b) { return { focus: b.focus, minutes: b.minutes }; });
    return buildWarmupPlan(computeFocusSuggestions(filterMatches(state.all, state.filters)), w.minutes);
  }
  function focusOptions(sel) {
    return FOCUSES.map(function (f) { return '<option value="' + f + '"' + (f === sel ? ' selected' : '') + '>' + esc(focusLabel(f)) + '</option>'; }).join('');
  }
  function numOptions(list, sel, fmt) {
    return list.map(function (v) { return '<option value="' + v + '"' + (v === sel ? ' selected' : '') + '>' + esc(fmt(v)) + '</option>'; }).join('');
  }

  function renderTraining() {
    $('#train-sub').textContent = readOnly() ? tr('trn.subOther', { name: (viewedPlayer() || {}).name || state.dataFor }) : tr('trn.sub');
    renderTimerCard();
    var ms = filterMatches(state.all, state.filters);
    var sug = computeFocusSuggestions(ms);
    $('#train-focus').innerHTML = '<h3>' + tr('trn.focusTitle') + '</h3>' +
      (ms.length ? '' : '<p class="muted small">' + tr('trn.focusNoData') + '</p>') +
      '<ol class="focus-list">' + sug.map(function (s) {
        return '<li><div class="focus-name">' + esc(focusLabel(s.focus)) + '</div><div class="muted small">' + esc(focusReason(s)) + '</div>' +
          '<div class="drill">' + tr('drill.' + s.focus) + '</div></li>';
      }).join('') + '</ol>' +
      '<div class="sit-foot">' + tr('trn.focusFoot', { v: plural(ms.length, 'match') }) + '</div>';
    renderBreaks();
    renderWarmupEffect();
    renderTrainingLog();
  }

  function renderTimerCard() {
    var box = $('#train-plan');
    var tm = state.timer;
    if (readOnly()) { box.innerHTML = '<h3>' + tr('trn.planTitle') + '</h3><p class="muted small">' + tr('trn.planOther') + '</p>'; return; }
    if (tm) {
      if (tm.ended) {
        var disabled = state.timerSaving ? ' disabled' : '';
        box.innerHTML = '<h3>' + tr('trn.doneTitle') + '</h3><p class="muted small">' + tr(tm.issue || (tm.saveRejected ? 'trn.rejectedSave' : 'trn.pendingSave')) + '</p>' +
          '<p class="muted small">' + tr('trn.keptActivity', { v: trainingSeconds(tm.payload || timerBody(tm, tm.ended)) }) + '</p>' +
          '<div class="btn-row"><button type="button" class="btn" data-timer="stop"' + disabled + '>' + tr('trn.retrySave') + '</button>' +
          '<button type="button" class="btn btn-ghost" data-timer="export">' + tr('trn.exportTimer') + '</button>' +
          '<button type="button" class="btn btn-ghost" data-timer="discard"' + disabled + '>' + tr('trn.discardTimer') + '</button></div>' +
          '<p class="muted small">' + tr('trn.timerPrecision') + '</p>';
        return;
      }
      var b = tm.blocks[tm.idx], next = tm.blocks[tm.idx + 1];
      box.innerHTML = '<h3>' + tr('trn.running') + '</h3>' +
        (tm.legacyActivityExcluded ? '<p class="muted small">' + tr('trn.legacyActivity') + '</p>' : '') +
        '<div class="timer-now"><div class="timer-clock" id="timer-clock">--:--</div><div><div class="timer-focus">' + esc(focusLabel(b.focus)) + '</div>' +
        '<div class="muted small">' + tr('trn.blockOf', { i: tm.idx + 1, n: tm.blocks.length }) + (next ? ' · ' + tr('trn.next', { v: esc(focusLabel(next.focus)) }) : '') + '</div></div></div>' +
        '<div class="meter timer-meter"><span id="timer-bar"></span></div>' +
        '<p class="muted small">' + tr('trn.timerBounds') + '</p>' +
        '<div class="drill">' + tr('drill.' + b.focus) + '</div>' +
        '<ol class="plan-steps">' + tm.blocks.map(function (x, i) {
          return '<li class="' + (i < tm.idx ? 'done' : i === tm.idx ? 'cur' : '') + '"><span>' + esc(focusLabel(x.focus)) + '</span><span>' + x.minutes + ' min</span></li>';
        }).join('') + '</ol>' +
        '<div class="btn-row"><button type="button" class="btn" data-timer="pause">' + tr(tm.paused ? 'trn.resume' : 'trn.pause') + '</button>' +
        '<button type="button" class="btn btn-ghost" data-timer="skip">' + tr('trn.skip') + '</button>' +
        '<button type="button" class="btn btn-ghost" data-timer="stop">' + tr('trn.stop') + '</button></div>';
      tickTimer();
      return;
    }
    var plan = currentPlan(), w = warmupCfg(), custom = !!(w.blocks && w.blocks.length) || !!state.planDraft;
    box.innerHTML = '<h3>' + tr('trn.planTitle') + ' <span class="muted">· ' + tr('trn.total', { v: planMinutes(plan) }) + '</span></h3>' +
      '<p class="muted small">' + tr(custom ? 'trn.planCustom' : 'trn.planAuto') + '</p>' +
      '<div class="plan-rows">' + plan.map(function (b, i) {
        return '<div class="plan-row"><span class="plan-no">' + (i + 1) + '</span><select class="select" data-plan-focus="' + i + '" aria-label="' + esc(tr('trn.focus')) + '">' + focusOptions(b.focus) + '</select>' +
          '<label class="plan-min"><input type="number" min="1" max="60" value="' + b.minutes + '" data-plan-min="' + i + '" aria-label="' + esc(tr('trn.minutes')) + '"> min</label>' +
          '<button type="button" class="del-btn" data-plan-del="' + i + '"' + (plan.length < 2 ? ' disabled' : '') + ' title="' + esc(tr('trn.removeBlock')) + '" aria-label="' + esc(tr('trn.removeBlock')) + '">' + TRASH + '</button></div>';
      }).join('') + '</div>' +
      '<div class="btn-row"><button type="button" class="btn btn-ghost" data-plan="add"' + (plan.length >= 8 ? ' disabled' : '') + '>' + tr('trn.addBlock') + '</button>' +
      (custom ? '<button type="button" class="btn btn-ghost" data-plan="save">' + tr('trn.savePlan') + '</button><button type="button" class="btn btn-ghost" data-plan="auto">' + tr('trn.autoPlan') + '</button>' :
        '<label class="plan-len">' + tr('trn.autoLen') + ' <select class="select" data-plan-len>' + numOptions(WARMUP_MINUTES, w.minutes, function (v) { return v + ' min'; }) + '</select></label>') + '</div>' +
      '<button type="button" class="btn btn-primary timer-start" data-timer="start">' + tr('trn.start') + '</button>' +
      '<div class="sit-foot">' + tr('trn.planFoot') + ' ' + tr('trn.timerPrecision') + ' ' + tr('trn.timerBounds') + '</div>';
  }

  /* timer: blocks run one after the other; a chime and a notification at each change */
  function measuredTimer(tm, now) {
    if (!Array.isArray(tm.activeIntervals)) {
      // Legacy countdowns cannot locate prior pauses. Keep calendar bounds but measure only from now.
      tm.activeIntervals = []; tm.activeSince = tm.paused || tm.ended ? null : now;
      tm.legacyActivityExcluded = true;
    }
    return tm;
  }
  function loadTimer() {
    try {
      var t = JSON.parse(localStorage.getItem('rt.timer') || 'null');
      if (!t || !Array.isArray(t.blocks) || !t.blocks.length || !isNum(t.started) ||
          !Number.isInteger(t.idx) || t.idx < 0 || t.idx >= t.blocks.length) return null;
      return measuredTimer(t, Date.now());
    } catch (e) { return null; }
  }
  function closeTimerInterval(tm, now) {
    measuredTimer(tm, now);
    if (isNum(tm.activeSince) && now > tm.activeSince) tm.activeIntervals.push({
      started_at: new Date(tm.activeSince).toISOString(), ended_at: new Date(now).toISOString()
    });
    tm.activeSince = null;
  }
  function timerBody(tm, end) {
    var intervals = tm.activeIntervals.slice();
    if (!tm.ended && isNum(tm.activeSince) && end > tm.activeSince) intervals.push({
      started_at: new Date(tm.activeSince).toISOString(), ended_at: new Date(end).toISOString()
    });
    var main = tm.blocks.find(function (b) { return b.focus !== 'freeplay'; }) || tm.blocks[0];
    var raw = { started_at: new Date(tm.started).toISOString(), ended_at: new Date(end).toISOString(), active_intervals: intervals, focus: main.focus };
    // Invalid legacy/persisted content is retained for export, never silently repaired by dropping activity.
    return canonicalTraining(raw) || raw;
  }
  function freezeTimer(tm, now) {
    if (!tm.ended) { closeTimerInterval(tm, now); tm.ended = now; tm.paused = now; }
    if (!tm.payload) tm.payload = timerBody(tm, tm.ended);
  }
  function guardTimer(tm, now, openingInterval) {
    var body = timerBody(tm, now), issue = trainingLimit(body);
    if (now - tm.started >= TRAINING_MAX_MS) issue = 'trn.limitTime';
    if (openingInterval && body.active_intervals.length >= TRAINING_MAX_INTERVALS) issue = 'trn.limitIntervals';
    if (!issue) return false;
    freezeTimer(tm, now); tm.issue = issue;
    saveTimer(); renderTimerPill();
    toast(tr(issue), true);
    if (state.route.view === 'training') renderTimerCard();
    return true;
  }
  function exportTimer() {
    var tm = state.timer;
    if (!tm || !tm.ended) return false;
    freezeTimer(tm, tm.ended);
    var blob = new Blob([JSON.stringify(tm.payload, null, 2)], { type: 'application/json' });
    var url = URL.createObjectURL(blob), link = document.createElement('a');
    link.href = url; link.download = 'rocket-tracker-training-' + new Date(tm.started).toISOString().slice(0, 10) + '.json';
    document.body.appendChild(link); link.click(); link.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 0);
    return true;
  }
  function discardTimer() {
    if (!state.timer || state.timerSaving || !window.confirm(tr('trn.confirmDiscardTimer'))) return false;
    state.timer = null; saveTimer(); renderTimerPill(); renderTimerCard();
    return true;
  }
  function saveTimer() { try { if (state.timer) localStorage.setItem('rt.timer', JSON.stringify(state.timer)); else localStorage.removeItem('rt.timer'); } catch (e) { /* ignore */ } }
  function blockLeft(tm, now) {
    var b = tm.blocks[tm.idx];
    return b.minutes * 60000 - ((tm.paused || now) - tm.blockStart - tm.pausedMs);
  }
  function startTimer() {
    if (state.timer || state.timerSaving) return;
    var plan = currentPlan().filter(function (b) { return b.minutes > 0; });
    if (!plan.length) return;
    var now = Date.now();
    state.timer = { blocks: plan, started: now, idx: 0, blockStart: now, paused: null, pausedMs: 0, activeIntervals: [], activeSince: now };
    saveTimer();
    if (window.Notification && Notification.permission === 'default') { try { Notification.requestPermission(); } catch (e) { /* ignore */ } }
    chime();
    renderTimerCard();
  }
  function nextBlock(now) {
    var tm = state.timer;
    if (!tm || tm.ended) return;
    measuredTimer(tm, now);
    if (guardTimer(tm, now, true)) return;
    if (tm.idx + 1 >= tm.blocks.length) return finishTimer();
    closeTimerInterval(tm, now); tm.activeSince = now;
    tm.idx++; tm.blockStart = now; tm.pausedMs = 0; tm.paused = null;
    var b = tm.blocks[tm.idx];
    chime();
    notify(tr('trn.nowTitle', { v: focusLabel(b.focus) }), tr('trn.nowText', { v: b.minutes }));
    saveTimer();
    if (state.route.view === 'training') renderTimerCard();
  }
  /** Freeze the canonical payload before deciding thresholds; failed saves keep that exact payload. */
  function finishTimer() {
    var tm = state.timer, now = Date.now(), gen = state.gen;
    if (!tm || state.timerSaving) return Promise.resolve(false);
    measuredTimer(tm, now); freezeTimer(tm, now);
    var body = tm.payload;
    tm.issue = trainingLimit(body) || (trainingSeconds(body) < 60 ? 'trn.tooShort' : '');
    if (tm.issue) {
      saveTimer(); renderTimerPill(); toast(tr(tm.issue), true);
      if (state.route.view === 'training') renderTimerCard();
      return Promise.resolve(false);
    }
    state.timerSaving = true;
    saveTimer(); renderTimerPill();
    if (state.route.view === 'training') renderTimerCard();
    return apiFetch('/api/trainings', { method: 'POST', body: JSON.stringify(body) }).then(function () {
      if (state.timer === tm) state.timer = null;
      saveTimer(); chime(); notify(tr('trn.doneTitle'), tr('trn.doneText'));
      toast(tr('trn.saved'));
      if (gen === state.gen) loadTrainings();
      return true;
    }).catch(function (e) {
      tm.saveRejected = e.status >= 400 && e.status < 500 && e.status !== 408 && e.status !== 429;
      saveTimer(); toast(tr('toast.saveFailed', { v: e.message }), true);
      return false;
    }).then(function (saved) {
      state.timerSaving = false; renderTimerPill();
      if (state.route.view === 'training') renderTimerCard();
      return saved;
    });
  }
  function pauseTimer(now) {
    var tm = state.timer;
    if (!tm || tm.ended) return;
    measuredTimer(tm, now);
    if (guardTimer(tm, now, !!tm.paused)) return;
    if (tm.paused) { tm.pausedMs += now - tm.paused; tm.paused = null; tm.activeSince = now; }
    else { closeTimerInterval(tm, now); tm.paused = now; }
    saveTimer(); renderTimerCard();
  }
  function tickTimer() {
    var tm = state.timer;
    renderTimerPill();
    if (!tm || tm.ended) return;
    var now = Date.now();
    if (guardTimer(tm, now, false)) return;
    var left = blockLeft(tm, now);
    if (left <= 0 && !tm.paused) { nextBlock(now); return; }
    var clock = $('#timer-clock'), bar = $('#timer-bar');
    if (clock) clock.textContent = fmtClock(Math.ceil(Math.max(0, left) / 1000));
    if (bar) bar.style.width = (100 - Math.max(0, left) / (tm.blocks[tm.idx].minutes * 600)).toFixed(1) + '%';
  }
  function renderTimerPill() {
    var pill = $('#timer-pill'), tm = state.timer;
    pill.hidden = !tm || state.route.view === 'training';
    if (!tm || pill.hidden) return;
    if (tm.ended) { pill.textContent = tr('trn.retrySave'); return; }
    pill.textContent = tr('trn.pill', { v: focusLabel(tm.blocks[tm.idx].focus), t: fmtClock(Math.ceil(Math.max(0, blockLeft(tm, Date.now())) / 1000)) });
  }
  var audioCtx = null;
  function chime() {
    try {
      var AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return;
      audioCtx = audioCtx || new AC();
      [0, 0.18].forEach(function (d, i) {
        var o = audioCtx.createOscillator(), g = audioCtx.createGain(), t = audioCtx.currentTime + d;
        o.frequency.value = i ? 880 : 660;
        g.gain.setValueAtTime(0.0001, t); g.gain.exponentialRampToValueAtTime(0.25, t + 0.02); g.gain.exponentialRampToValueAtTime(0.0001, t + 0.3);
        o.connect(g); g.connect(audioCtx.destination); o.start(t); o.stop(t + 0.32);
      });
    } catch (e) { /* no sound */ }
  }
  function notify(title, body) {
    try { if (window.Notification && Notification.permission === 'granted') new Notification(title, { body: body, tag: 'rt-warmup' }); } catch (e) { /* ignore */ }
  }

  function savePlan(blocks, minutes) {
    var w = warmupCfg();
    var cfg = Object.assign({}, state.config || {}, { warmup: { minutes: minutes || w.minutes, blocks: blocks } });
    return apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(cfg) }).then(function (c) {
      state.config = c || cfg;
      state.planDraft = null;
      toast(tr('toast.settingsSaved'));
      renderTraining();
    }).catch(function (e) { toast(tr('toast.saveFailed', { v: e.message }), true); });
  }

  function renderBreaks() {
    var c = state.config || {}, every = isNum(c.break_every) ? c.break_every : 0, mins = isNum(c.break_minutes) ? c.break_minutes : 5;
    $('#train-breaks').innerHTML = '<div class="breaks-row">' +
      '<label>' + tr('trn.breakEvery') + ' <select class="select" data-break="every"' + (readOnly() ? ' disabled' : '') + '>' +
      numOptions(BREAK_EVERY, every, function (v) { return v ? tr('trn.everyN', { n: v, v: v }) : tr('trn.never'); }) + '</select></label>' +
      '<label>' + tr('trn.breakLen') + ' <select class="select" data-break="minutes"' + (readOnly() || !every ? ' disabled' : '') + '>' +
      numOptions(BREAK_MINUTES, mins, function (v) { return v + ' min'; }) + '</select></label></div>' +
      '<p class="muted small">' + tr(every ? 'trn.breakOn' : 'trn.breakOff', { n: every, m: mins }) + '</p>';
  }

  function renderWarmupEffect() {
    var e = computeWarmupEffect(state.all, profileTrainings());
    var box = function (label, s, cls) {
      return '<div class="tilt-box"><span class="lbl">' + label + '</span><div class="val' + (cls ? ' ' + cls : '') + '">' + fmtPct(s.first.wr) + '</div>' +
        '<span class="n">' + tr('trn.firstThree', { v: plural(s.first.n, 'match') }) + ' · ' + plural(s.sessions, 'session') + '<br>' +
        tr('trn.wholeSession', { v: fmtPct(s.all.wr) }) + (isNum(s.touches) ? ' · ' + tr('trn.touches', { v: fmtNum(s.touches, 1) }) : '') + '</span></div>';
    };
    var verdict = warmupEffectMessage(e);
    $('#train-effect').innerHTML = '<div class="tilt-pair">' + box(tr('trn.withWarmup'), e.warm) + box(tr('trn.withoutWarmup'), e.cold) + '</div>' +
      '<div class="tilt-verdict">' + verdict + '</div>' +
      '<div class="sit-foot">' + tr('trn.effectFoot', { m: WARM_MIN_S / 60, h: WARM_GAP_MS / 3600000 }) + '</div>';
  }

  function renderTrainingLog() {
    var ts = profileTrainings();
    var s = computeTrainingSummary(ts, state.all);
    var sess = sessionsWithWarmup(state.all.filter(function (m) { return m._t >= Date.now() - 30 * DAY_MS; }), ts);
    var warmShare = share(sess.filter(function (x) { return x.warm; }).length, sess.length);
    var tile = function (label, v, sub) { return '<div class="card kpi"><span class="kpi-label">' + label + '</span><span class="kpi-value">' + v + '</span><span class="kpi-sub">' + sub + '</span></div>'; };
    $('#train-tiles').innerHTML =
      tile(tr('trn.week'), fmtHours(s.week), tr('trn.last7')) +
      tile(tr('trn.month'), fmtHours(s.month), tr('trn.monthSub', { n: s.count, v: fmtNum(s.count), d: s.days })) +
      tile(tr('trn.warmShare'), fmtPct(warmShare), tr('trn.warmShareSub', { v: plural(sess.length, 'session') })) +
      tile(tr('trn.ratio'), fmtPct(s.ratio), tr('trn.ratioSub', { v: fmtHours(s.played) }));
    $('#train-foci').innerHTML = s.foci.length ? s.foci.map(function (f) {
      return '<span class="chip focus-chip">' + esc(f.focus ? focusLabel(f.focus) : tr('trn.noFocus')) + ' · ' + fmtHours(f.seconds) + '</span>';
    }).join('') : '';
    var rows = ts.slice().reverse().slice(0, 20);
    var ro = readOnly();
    $('#t-trainings').innerHTML = '<thead><tr><th>' + tr('trn.when') + '</th><th class="r">' + tr('trn.length') + '</th><th>' + tr('trn.source') + '</th><th>' + tr('trn.focus') + '</th><th class="r hide-mobile">' + tr('trn.touchesCol') + '</th>' + (ro ? '' : '<th></th>') + '</tr></thead><tbody>' +
      (rows.length ? rows.map(function (t) {
        return '<tr><td>' + esc(fmtDate(trainingStart(t), true)) + '</td><td class="r">' + fmtHours(trainingSeconds(t)) + '</td>' +
          '<td>' + tr(t.source === 'timer' ? 'trn.srcTimer' : 'trn.srcGame') + (t.arena ? ' <span class="muted small hide-mobile">· ' + esc(prettyArena(t.arena)) + '</span>' : '') + '</td>' +
          '<td>' + (ro ? esc(t.focus ? focusLabel(t.focus) : '—') : '<select class="select" data-trn-focus="' + t.id + '"><option value="">' + esc(tr('trn.noFocus')) + '</option>' + focusOptions(t.focus) + '</select>') + '</td>' +
          '<td class="r hide-mobile">' + (t.touches ? fmtNum(t.touches) : '—') + '</td>' +
          (ro ? '' : '<td class="r"><button type="button" class="del-btn" data-trn-del="' + t.id + '" title="' + esc(tr('trn.delete')) + '" aria-label="' + esc(tr('trn.delete')) + '">' + TRASH + '</button></td>') + '</tr>';
      }).join('') : '<tr class="empty-row"><td colspan="6">' + tr('trn.none') + '</td></tr>') + '</tbody>';
  }

  function bindTraining() {
    var view = $('#view-training');
    view.addEventListener('click', function (e) {
      var b = e.target.closest('button');
      if (!b || b.disabled) return;
      var plan = currentPlan().map(function (x) { return Object.assign({}, x); });
      if (b.dataset.timer === 'start') startTimer();
      else if (b.dataset.timer === 'pause' && state.timer) {
        pauseTimer(Date.now());
      } else if (b.dataset.timer === 'skip' && state.timer) nextBlock(Date.now());
      else if (b.dataset.timer === 'stop') finishTimer();
      else if (b.dataset.timer === 'export') exportTimer();
      else if (b.dataset.timer === 'discard') discardTimer();
      else if (b.dataset.plan === 'add') { plan.push({ focus: 'shooting', minutes: 3 }); state.planDraft = plan; renderTimerCard(); }
      else if (b.dataset.plan === 'save') savePlan(plan);
      else if (b.dataset.plan === 'auto') { state.planDraft = null; savePlan([]); }
      else if (b.dataset.planDel != null) { plan.splice(+b.dataset.planDel, 1); state.planDraft = plan; renderTimerCard(); }
      else if (b.dataset.trnDel) {
        if (!window.confirm(tr('trn.confirmDelete'))) return;
        apiFetch('/api/trainings/' + b.dataset.trnDel, { method: 'DELETE' }).then(loadTrainings).catch(function (err) { toast(tr('toast.saveFailed', { v: err.message }), true); });
      }
    });
    view.addEventListener('change', function (e) {
      var el = e.target, plan = currentPlan().map(function (x) { return Object.assign({}, x); });
      if (el.dataset.planFocus != null) { plan[+el.dataset.planFocus].focus = el.value; state.planDraft = plan; renderTimerCard(); }
      else if (el.dataset.planMin != null) { plan[+el.dataset.planMin].minutes = Math.max(1, Math.min(60, parseInt(el.value, 10) || 1)); state.planDraft = plan; renderTimerCard(); }
      else if (el.hasAttribute('data-plan-len')) savePlan([], parseInt(el.value, 10));
      else if (el.dataset.break) {
        var c = Object.assign({}, state.config || {});
        c[el.dataset.break === 'every' ? 'break_every' : 'break_minutes'] = parseInt(el.value, 10) || 0;
        apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(c) }).then(function (saved) {
          state.config = saved || c; toast(tr('toast.settingsSaved')); renderBreaks();
        }).catch(function (err) { toast(tr('toast.saveFailed', { v: err.message }), true); });
      } else if (el.dataset.trnFocus) {
        apiFetch('/api/trainings/' + el.dataset.trnFocus, { method: 'PATCH', body: JSON.stringify({ focus: el.value }) }).then(loadTrainings)
          .catch(function (err) { toast(tr('toast.saveFailed', { v: err.message }), true); });
      }
    });
    $('#timer-pill').addEventListener('click', function () { location.hash = href('training'); });
    state.timer = loadTimer();
    saveTimer();
    setInterval(tickTimer, 500);
  }

  /* ---------- manual games (day dialog) ---------- */
  var MANUAL_MODES = ['1v1', '2v2', '3v3', '4v4'];
  var dayDialogKey = null;
  function loadManual() {
    var gen = state.gen, mark = dataMarker(state.status, 'match_count');
    state.manualBusy++;
    return apiFetch(apiBase() + '/manual').then(function (d) {
      if (gen !== state.gen) return false;
      state.manualBusy--;
      state.manual = Array.isArray(d) ? d : [];
      state.lastManual = mark;
      state.manualFailed = false;
      if (state.loaded) renderAll();
      return true;
    }).catch(function (e) {
      if (gen !== state.gen) return false;
      state.manualBusy--;
      state.lastManual = null;
      state.manualFailed = true;
      console.warn('manual', e);
      return false;
    });
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
    $$('.discord-only').forEach(function (el) { el.hidden = !(srv && state.discord); });
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

  var playersSeq = 0;
  /** Loads the players and the leaderboard; only the latest request is applied (filters changed, 10 s refresh). */
  function loadPlayers() {
    if (!isServer()) return Promise.resolve();
    var lb = state.lb, seq = ++playersSeq;
    var q = '?mode=' + encodeURIComponent(lb.mode) + '&tag=' + encodeURIComponent(lb.tag) + '&days=' + encodeURIComponent(lb.period);
    return Promise.all([apiFetch('/api/players'), apiFetch('/api/leaderboard' + q)]).then(function (res) {
      if (seq !== playersSeq) return;
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
    bindRanks();
    bindTraining();
    bindLive();
    $('#profile-sel').addEventListener('change', function (e) { selectProfile(e.target.value); });
    $('#profile-del').addEventListener('click', deleteProfile);
    apiFetch('/api/session').catch(function () { return { mode: 'local' }; }).then(function (s) {
      state.mode = s && s.mode === 'server' ? 'server' : 'local';
      state.me = (s && s.user) || null;
      state.discord = !!(s && s.discord);
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
