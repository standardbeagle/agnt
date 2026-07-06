// API efficiency audit — analyzes the recorded fetch/XHR call buffer
// (window.__devtool_api) for waterfalls, N+1 patterns, redundant fetches, and
// chatty initial loads.
//
// DATA LIMITATION (fail-honest): the api-tracker call buffer carries NO
// response-size / content-length field, so payload-size over-fetch is NOT
// measurable here. The "over-fetch" detector is implemented as CHATTY-LOAD
// (call count in the initial-load window) only, plus one info-level note that
// true payload-size over-fetch detection is unavailable until api-tracker
// captures a content-length. Inventing a size would violate project rules.

(function() {
  'use strict';

  // === THRESHOLDS (named constants) ===
  var WATERFALL_GAP_MS = 150;        // B started within this of A finishing → serial link
  var WATERFALL_MIN_CHAIN = 3;       // serial chain length to flag (or 2 if notable waste)
  var WATERFALL_NOTABLE_WASTE_MS = 200; // 2-call chain flagged only if it wastes this much
  var WATERFALL_CRITICAL_WASTE_MS = 1000; // wasted wall-time above this → critical
  var NPLUSONE_MIN = 5;              // calls sharing a template → flag batch opportunity
  var DUPLICATE_WINDOW_MS = 2000;    // identical method+url within this window → duplicate
  var DUPLICATE_MIN = 2;             // duplicate count threshold
  var CHATTY_WINDOW_MS = 3000;       // initial-load window measured from earliest call
  var CHATTY_THRESHOLD = 20;         // calls in the window above this → chatty load
  var CHATTY_SEVERE = 40;            // chatty count above this → warning instead of info

  // Shared audit helpers (audit-utils.js): stable FNV-1a finding ids, the
  // shared window.__devtool.audit.findingSelectors highlight registry, and the
  // canonical A-F grade scale.
  function computeFindingID(type, selector, message) {
    return window.__devtool_audit_utils.computeFindingID(type, selector, message);
  }

  function registerFinding(id, selector) {
    window.__devtool_audit_utils.registerFinding(id, selector);
  }

  function calculateGrade(score) {
    return window.__devtool_audit_utils.calculateGrade(score);
  }

  // Extract just the path (drop origin + query) for compact messages and
  // template grouping. Falls back to the raw url if parsing fails.
  function urlPath(url) {
    try {
      var u = new URL(url, window.location.origin);
      return u.pathname;
    } catch (e) {
      var q = url.indexOf('?');
      return q >= 0 ? url.substring(0, q) : url;
    }
  }

  // Normalize a URL to a template: replace id-like path segments with {id} and
  // strip the query string entirely. Used to group N+1 patterns.
  function templateOf(method, url) {
    var path = urlPath(url);
    var segments = path.split('/');
    for (var i = 0; i < segments.length; i++) {
      var s = segments[i];
      if (s === '') continue;
      var isNumeric = /^\d+$/.test(s);
      var isUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
      var isHex = /^[0-9a-f]{12,}$/i.test(s); // long hex token (e.g. mongo id, sha)
      if (isNumeric || isUUID || isHex) {
        segments[i] = '{id}';
      }
    }
    return method + ' ' + segments.join('/');
  }

  // Options:
  //   raw: boolean - if true, returns verbose detailed format
  //                  (default: false, AI-optimized grouped format)
  function auditAPIEfficiency(options) {
    options = options || {};
    var raw = options.raw === true;

    var api = window.__devtool_api;
    var calls = (api && typeof api.getCalls === 'function') ? api.getCalls() : [];

    var findings = [];
    var findingSelectors = {};

    function addFinding(f) {
      findingSelectors[f.id] = f.selector || '';
      registerFinding(f.id, f.selector || '');
      findings.push(f);
    }

    // === EMPTY / SHORT BUFFER: honest empty report ===
    if (!calls || calls.length === 0) {
      var emptySummary = 'No API calls recorded — reload page then re-run.';
      if (!raw) {
        return {
          audit: 'api',
          score: 100,
          grade: 'A',
          summary: emptySummary,
          checkedAt: new Date().toISOString(),
          stats: { total: 0, errors: 0, warnings: 0, info: 0, totalIssues: 0 },
          findingsByType: {}
        };
      }
      return {
        audit: 'api',
        score: 100,
        grade: 'A',
        summary: emptySummary,
        checkedAt: new Date().toISOString(),
        findings: [],
        findingSelectors: {}
      };
    }

    // Work on a timestamp-sorted copy.
    var sorted = calls.slice().sort(function(a, b) {
      return (a.timestamp || 0) - (b.timestamp || 0);
    });

    // === 1. WATERFALL / SERIAL-CHAIN ===
    // Walk the sorted calls; a link A→B exists when B starts shortly after A
    // finishes (within WATERFALL_GAP_MS) and they do not overlap — i.e. they
    // could have run in parallel but ran serially.
    function detectWaterfall() {
      var n = sorted.length;
      if (n < 2) return;
      var i = 0;
      while (i < n - 1) {
        var chain = [sorted[i]];
        var j = i;
        while (j < n - 1) {
          var a = sorted[j];
          var b = sorted[j + 1];
          var aEnd = (a.timestamp || 0) + (a.duration || 0);
          var bStart = b.timestamp || 0;
          var gap = bStart - aEnd;
          // serial link: b starts at/after a ends, within the gap window
          if (gap >= -10 && gap <= WATERFALL_GAP_MS && (a.duration != null) && (b.duration != null)) {
            chain.push(b);
            j++;
          } else {
            break;
          }
        }

        if (chain.length >= 2) {
          var serialWall = 0;
          var maxSingle = 0;
          for (var c = 0; c < chain.length; c++) {
            var d = chain[c].duration || 0;
            serialWall += d;
            if (d > maxSingle) maxSingle = d;
          }
          var wasted = serialWall - maxSingle; // wall-time saved if run in parallel
          var flag = chain.length >= WATERFALL_MIN_CHAIN ||
                     (chain.length >= 2 && wasted >= WATERFALL_NOTABLE_WASTE_MS);
          if (flag) {
            var firstUrl = chain[0].url || '';
            var sel = firstUrl;
            var msg = chain.length + ' calls run serially, ~' + Math.round(wasted) +
                      'ms wasted vs parallel';
            var sev = wasted >= WATERFALL_CRITICAL_WASTE_MS ? 'critical' : 'warning';
            var id = computeFindingID('waterfall', sel, msg);
            addFinding({
              id: id,
              type: 'waterfall',
              severity: sev,
              selector: sel,
              message: msg,
              chainLength: chain.length,
              serialWallMs: Math.round(serialWall),
              maxSingleMs: Math.round(maxSingle),
              wastedMs: Math.round(wasted),
              urls: chain.slice(0, 8).map(function(call) {
                return (call.method || 'GET') + ' ' + urlPath(call.url || '');
              })
            });
          }
        }

        // advance past the chain we just consumed (at least one step)
        i = (j > i) ? j + 1 : i + 1;
      }
    }

    // === 2. N-PLUS-ONE ===
    // Group by method+template; flag templates hit NPLUSONE_MIN+ times where
    // the calls differ only by an id segment (i.e. the template contains {id}).
    function detectNPlusOne() {
      var groups = {};
      for (var i = 0; i < sorted.length; i++) {
        var call = sorted[i];
        var tmpl = templateOf(call.method || 'GET', call.url || '');
        if (!groups[tmpl]) groups[tmpl] = { template: tmpl, count: 0, calls: [] };
        groups[tmpl].count++;
        groups[tmpl].calls.push(call);
      }
      for (var key in groups) {
        var g = groups[key];
        // Only meaningful if the template was parameterised (had id segments)
        // AND it was hit many times — otherwise it is just a hot endpoint.
        if (g.count >= NPLUSONE_MIN && key.indexOf('{id}') >= 0) {
          var sel = g.calls[0].url || '';
          var msg = g.count + '× ' + key + ' → batch';
          var id = computeFindingID('n-plus-one', sel, msg);
          addFinding({
            id: id,
            type: 'n-plus-one',
            severity: 'warning',
            selector: sel,
            message: msg,
            template: key,
            count: g.count
          });
        }
      }
    }

    // === 3. REDUNDANT / DUPLICATE ===
    // Identical method+url repeated within DUPLICATE_WINDOW_MS → duplicate
    // fetch / cache miss. (Bodies are not in the buffer, so method+url is the
    // identity.)
    function detectDuplicates() {
      var byKey = {};
      for (var i = 0; i < sorted.length; i++) {
        var call = sorted[i];
        var key = (call.method || 'GET') + ' ' + (call.url || '');
        if (!byKey[key]) byKey[key] = [];
        byKey[key].push(call);
      }
      for (var k in byKey) {
        var list = byKey[k];
        if (list.length < DUPLICATE_MIN) continue;
        // Slide a window over timestamps; find the densest cluster.
        var bestCount = 0;
        var bestSpan = 0;
        for (var a = 0; a < list.length; a++) {
          var count = 1;
          var lastInWindow = list[a].timestamp || 0;
          for (var b = a + 1; b < list.length; b++) {
            if (((list[b].timestamp || 0) - (list[a].timestamp || 0)) <= DUPLICATE_WINDOW_MS) {
              count++;
              lastInWindow = list[b].timestamp || 0;
            } else {
              break;
            }
          }
          if (count > bestCount) {
            bestCount = count;
            bestSpan = lastInWindow - (list[a].timestamp || 0);
          }
        }
        if (bestCount >= DUPLICATE_MIN) {
          var first = list[0];
          var sel = first.url || '';
          var label = (first.method || 'GET') + ' ' + urlPath(first.url || '');
          var msg = label + ' fetched ' + bestCount + '× in ' + Math.round(bestSpan) + 'ms';
          var id = computeFindingID('duplicate-call', sel, msg);
          addFinding({
            id: id,
            type: 'duplicate-call',
            severity: 'warning',
            selector: sel,
            message: msg,
            count: bestCount,
            spanMs: Math.round(bestSpan)
          });
        }
      }
    }

    // === 4. OVER-FETCH → CHATTY-LOAD (+ payload-size unavailable note) ===
    function detectChattyLoad() {
      var earliest = sorted[0].timestamp || 0;
      var windowEnd = earliest + CHATTY_WINDOW_MS;
      var count = 0;
      for (var i = 0; i < sorted.length; i++) {
        var ts = sorted[i].timestamp || 0;
        if (ts >= earliest && ts <= windowEnd) count++;
      }
      if (count > CHATTY_THRESHOLD) {
        var sel = '';
        var msg = count + ' API calls in the first ' + (CHATTY_WINDOW_MS / 1000) +
                  's of load — consider consolidating initial requests';
        var sev = count > CHATTY_SEVERE ? 'warning' : 'info';
        var id = computeFindingID('chatty-load', sel, msg);
        addFinding({
          id: id,
          type: 'chatty-load',
          severity: sev,
          selector: sel,
          message: msg,
          count: count,
          windowMs: CHATTY_WINDOW_MS
        });
      }

      // Honest note: payload-size over-fetch is not measurable from this buffer.
      var noteMsg = 'Payload-size over-fetch detection unavailable: the API call ' +
                    'buffer has no response-size/content-length field. Add a ' +
                    'content-length capture to api-tracker to enable it.';
      var noteId = computeFindingID('over-fetch-unavailable', '', noteMsg);
      addFinding({
        id: noteId,
        type: 'over-fetch-unavailable',
        severity: 'info',
        selector: '',
        message: noteMsg
      });
    }

    detectWaterfall();
    detectNPlusOne();
    detectDuplicates();
    detectChattyLoad();

    // === SCORING ===
    // Heavier weights for waterfall / n+1 / duplicate; lighter for chatty/info.
    var score = 100;
    for (var fi = 0; fi < findings.length; fi++) {
      var f = findings[fi];
      var weight;
      switch (f.type) {
        case 'waterfall':
          weight = f.severity === 'critical' ? 18 : 10;
          break;
        case 'n-plus-one':
          weight = 10;
          break;
        case 'duplicate-call':
          weight = 8;
          break;
        case 'chatty-load':
          weight = f.severity === 'warning' ? 6 : 3;
          break;
        default: // over-fetch-unavailable note and other info
          weight = 0;
          break;
      }
      score -= weight;
    }
    score = Math.max(0, Math.min(100, score));
    var grade = calculateGrade(score);

    // === STATS ===
    var errorCount = 0, warningCount = 0, infoCount = 0;
    for (var si = 0; si < findings.length; si++) {
      var sv = findings[si].severity;
      if (sv === 'error' || sv === 'critical') errorCount++;
      else if (sv === 'warning') warningCount++;
      else infoCount++;
    }

    // === SUMMARY ===
    var summaryParts = [];
    if (score >= 80) summaryParts.push('API efficiency is good');
    else if (score >= 60) summaryParts.push('API efficiency is moderate');
    else summaryParts.push('API efficiency needs attention');
    summaryParts.push('(' + calls.length + ' calls analyzed)');
    var actionable = errorCount + warningCount;
    if (actionable > 0) {
      summaryParts.push(actionable + ' issue' + (actionable === 1 ? '' : 's') + ' to address');
    }
    var summary = summaryParts.join('. ');

    // === RAW RESPONSE ===
    if (raw) {
      return {
        audit: 'api',
        score: score,
        grade: grade,
        summary: summary,
        checkedAt: new Date().toISOString(),
        findings: findings,
        findingSelectors: findingSelectors
      };
    }

    // === AI-OPTIMIZED (DEFAULT): grouped by type, limited examples ===
    var byType = {};
    for (var gi = 0; gi < findings.length; gi++) {
      var gf = findings[gi];
      if (!byType[gf.type]) byType[gf.type] = [];
      if (byType[gf.type].length < 5) {
        byType[gf.type].push({
          id: gf.id,
          severity: gf.severity,
          selector: gf.selector,
          message: gf.message
        });
      }
    }

    return {
      audit: 'api',
      score: score,
      grade: grade,
      summary: summary,
      checkedAt: new Date().toISOString(),
      stats: {
        total: calls.length,
        errors: errorCount,
        warnings: warningCount,
        info: infoCount,
        totalIssues: findings.length
      },
      findingsByType: byType
    };
  }

  window.__devtool_audit_api = {
    auditAPIEfficiency: auditAPIEfficiency
  };

  // Register into the shared audit namespace so it sits alongside the other
  // audit modules (mirrors how audit-quality re-exports under __devtool_audit).
  if (!window.__devtool) { window.__devtool = {}; }
  if (!window.__devtool.audit) { window.__devtool.audit = {}; }
  window.__devtool.audit.auditAPIEfficiency = auditAPIEfficiency;
})();
