// Loading-UX audit — analyzes the spinner timeline recorded by C1
// (window.__devtool_spinners) for two loading-UX failure modes:
//
//   1. CASCADE  — loaders that fire serially (B appears after A disappears, in
//      the same DOM region, where A's load gated B's). Serial loading chains
//      waste wall-time that could have been spent loading in parallel.
//   2. FRAGMENTATION — N+ loaders active simultaneously under one ancestor,
//      which should be consolidated into a single master loader.
//
// DATA SOURCE: window.__devtool_spinners.getTimeline() returns records
// (newest-first) of shape:
//   { id, selector, ancestorPath, appearedAt, disappearedAt, pendingAPI }
// ancestorPath is LEAF-WARD → ROOT-WARD: index 0 is the spinner's nearest
// parent, the last index is the furthest ancestor (confirmed against
// mutation.js spinnerAncestorPath, which walks el.parentElement upward up to 4
// levels). disappearedAt is null while a spinner is still active. pendingAPI is
// only populated after the spinner disappears.

(function() {
  'use strict';

  // === THRESHOLDS (named constants) ===
  var CASCADE_GAP_MS = 400;        // B may appear up to this long after A disappears → serial link
  var CASCADE_CRITICAL_MS = 1000;  // total serial span above this → critical
  var CASCADE_MAX_CHAIN = 12;      // cap chain length to bound work
  var FRAGMENT_MIN = 3;            // concurrent sub-spinners under one ancestor to flag
  var FRAGMENT_MAX_MEMBERS = 12;   // cap members listed per fragmentation finding

  /**
   * Generate a stable 8-char hex finding ID from type, selector, and message.
   * FNV-1a 32-bit hash — same inputs always produce same output across runs.
   */
  function computeFindingID(type, selector, message) {
    var input = type + '\x00' + (selector || '') + '\x00' + (message || '');
    var h = 0x811c9dc5;
    for (var i = 0; i < input.length; i++) {
      h = h ^ input.charCodeAt(i);
      h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)) >>> 0;
    }
    return ('00000000' + h.toString(16)).slice(-8);
  }

  function registerFinding(id, selector) {
    if (!window.__devtool) { window.__devtool = {}; }
    if (!window.__devtool.audit) { window.__devtool.audit = {}; }
    if (!window.__devtool.audit.findingSelectors) { window.__devtool.audit.findingSelectors = {}; }
    window.__devtool.audit.findingSelectors[id] = selector || '';
  }

  function calculateGrade(score) {
    if (score >= 90) return 'A';
    if (score >= 80) return 'B';
    if (score >= 70) return 'C';
    if (score >= 60) return 'D';
    return 'F';
  }

  // Does this record's ancestorPath share any token with the other's path, or
  // does either record's selector appear as a token in the other's ancestorPath?
  // This is the cheap "same DOM region" proxy. ancestorPath tokens look like
  // "div#main" / "section.card"; selectors are full CSS selectors, so we test
  // token containment in both directions.
  function shareRegion(a, b) {
    var ap = a.ancestorPath || [];
    var bp = b.ancestorPath || [];
    var i, j;
    // Common ancestor token.
    for (i = 0; i < ap.length; i++) {
      for (j = 0; j < bp.length; j++) {
        if (ap[i] && ap[i] === bp[j]) return true;
      }
    }
    // B revealed by A: A's selector appears in B's ancestor chain (B is a
    // descendant of A's container), or vice versa.
    if (a.selector) {
      for (j = 0; j < bp.length; j++) {
        if (bp[j] && (bp[j] === a.selector || a.selector.indexOf(bp[j]) >= 0)) return true;
      }
    }
    if (b.selector) {
      for (i = 0; i < ap.length; i++) {
        if (ap[i] && (ap[i] === b.selector || b.selector.indexOf(ap[i]) >= 0)) return true;
      }
    }
    return false;
  }

  // Find the deepest (furthest-from-leaf) ancestor token shared by all records
  // in a set, used as the fragmentation finding's selector. Falls back to any
  // common token, then to the first member's nearest ancestor, then selector.
  function commonAncestorToken(records) {
    if (!records.length) return '';
    // Intersect ancestorPath token sets across all members.
    var first = records[0].ancestorPath || [];
    var candidates = first.slice();
    for (var r = 1; r < records.length; r++) {
      var pathR = records[r].ancestorPath || [];
      var next = [];
      for (var c = 0; c < candidates.length; c++) {
        for (var p = 0; p < pathR.length; p++) {
          if (candidates[c] && candidates[c] === pathR[p]) {
            next.push(candidates[c]);
            break;
          }
        }
      }
      candidates = next;
      if (!candidates.length) break;
    }
    if (candidates.length) {
      // ancestorPath is leaf-ward→root-ward; the last surviving candidate (the
      // one furthest down the first member's list) is the deepest shared one.
      return candidates[candidates.length - 1];
    }
    // No common token — fall back to nearest ancestor of first member, else its
    // own selector.
    if (first.length) return first[0];
    return records[0].selector || '';
  }

  // Options:
  //   raw: boolean - if true, returns verbose detailed format
  //                  (default: false, AI-optimized grouped format)
  function auditLoading(options) {
    options = options || {};
    var raw = options.raw === true;

    var spinners = window.__devtool_spinners;
    var timeline = (spinners && typeof spinners.getTimeline === 'function') ? spinners.getTimeline() : [];

    var findings = [];
    var findingSelectors = {};

    function addFinding(f) {
      findingSelectors[f.id] = f.selector || '';
      registerFinding(f.id, f.selector || '');
      findings.push(f);
    }

    // === EMPTY TIMELINE: honest empty report ===
    if (!timeline || timeline.length === 0) {
      var emptySummary = 'No loading indicators recorded — reload page then re-run.';
      if (!raw) {
        return {
          audit: 'loading',
          score: 100,
          grade: 'A',
          summary: emptySummary,
          checkedAt: new Date().toISOString(),
          stats: { total: 0, errors: 0, warnings: 0, info: 0, totalIssues: 0 },
          findingsByType: {}
        };
      }
      return {
        audit: 'loading',
        score: 100,
        grade: 'A',
        summary: emptySummary,
        checkedAt: new Date().toISOString(),
        findings: [],
        findingSelectors: {}
      };
    }

    // Work on an appearedAt-sorted copy (oldest-first). getTimeline() is
    // newest-first; sort ascending for chain walking.
    var sorted = timeline.slice().sort(function(a, b) {
      return (a.appearedAt || 0) - (b.appearedAt || 0);
    });
    var n = sorted.length;

    // Treat a still-active (null disappearedAt) spinner's end as "now".
    var nowMs = Date.now();
    function endOf(rec) {
      return (typeof rec.disappearedAt === 'number') ? rec.disappearedAt : nowMs;
    }
    function isResolved(rec) {
      return rec.pendingAPI && rec.pendingAPI.length > 0;
    }

    // === 1. CASCADE (serial chain) ===
    // A→B link: B appeared at/after A disappeared (within CASCADE_GAP_MS),
    // A had a resolved API (A gated B), and B shares A's DOM region. Build
    // chains of depth ≥2 greedily by appearedAt order. Members consumed by a
    // chain are not re-used as chain roots.
    function detectCascade() {
      var consumed = {}; // index → true once it is an interior/tail chain member
      for (var i = 0; i < n; i++) {
        if (consumed[i]) continue;
        var a = sorted[i];
        // Root must have a resolved API to claim it gated the next loader.
        if (!isResolved(a)) continue;
        var aDisappear = a.disappearedAt;
        if (typeof aDisappear !== 'number') continue; // still active → cannot have gated a successor

        var chain = [a];
        var chainIdx = [i];
        var prev = a;
        var prevDisappear = aDisappear;

        var k = i + 1;
        while (k < n && chain.length < CASCADE_MAX_CHAIN) {
          if (consumed[k]) { k++; continue; }
          var b = sorted[k];
          var bAppear = b.appearedAt || 0;
          var gap = bAppear - prevDisappear;
          if (bAppear >= prevDisappear && gap <= CASCADE_GAP_MS && shareRegion(prev, b)) {
            chain.push(b);
            chainIdx.push(k);
            consumed[k] = true;
            prev = b;
            // Extend only if this link also resolved (gated its successor).
            // If it is still active or unresolved, it can be the tail.
            if (typeof b.disappearedAt === 'number') {
              prevDisappear = b.disappearedAt;
            } else {
              break; // active tail terminates the chain
            }
            k++;
          } else {
            k++;
          }
        }

        if (chain.length >= 2) {
          var first = chain[0];
          var last = chain[chain.length - 1];
          var serial = endOf(last) - (first.appearedAt || 0);
          var maxSingle = 0;
          for (var c = 0; c < chain.length; c++) {
            var dur = endOf(chain[c]) - (chain[c].appearedAt || 0);
            if (dur > maxSingle) maxSingle = dur;
          }
          var depth = chain.length;
          var sel = first.selector || '';
          var chainLabel = chain.map(function(rec) {
            return rec.selector || rec.id || '?';
          });
          var preview = chainLabel.slice(0, 5).join('→');
          if (chainLabel.length > 5) preview += '→…';
          var msg = depth + ' loaders fire serially (' + preview + '), ~' +
                    Math.round(serial) + 'ms serial vs ~' + Math.round(maxSingle) +
                    'ms if parallel';
          var sev = serial > CASCADE_CRITICAL_MS ? 'critical' : 'warning';
          var id = computeFindingID('spinner-cascade', sel, msg);
          // Register every chain member's selector so the whole chain highlights.
          for (var s = 0; s < chain.length; s++) {
            if (chain[s].selector) registerFinding(id, chain[s].selector);
          }
          // findingSelectors keeps the root as the primary highlight target.
          addFinding({
            id: id,
            type: 'spinner-cascade',
            severity: sev,
            selector: sel,
            message: msg,
            depth: depth,
            serialMs: Math.round(serial),
            parallelMs: Math.round(maxSingle),
            chain: chainLabel
          });
        }
      }
    }

    // === 2. FRAGMENTATION (concurrent sub-spinners) ===
    // Find sets of spinners whose active intervals all mutually overlap and
    // that share a common ancestor region. If FRAGMENT_MIN+ hang under one
    // ancestor → flag for consolidation. Greedy clustering by overlap+region.
    function intervalsOverlap(a, b) {
      var aStart = a.appearedAt || 0, aEnd = endOf(a);
      var bStart = b.appearedAt || 0, bEnd = endOf(b);
      return aEnd >= bStart && bEnd >= aStart;
    }

    function detectFragmentation() {
      var used = {};
      for (var i = 0; i < n; i++) {
        if (used[i]) continue;
        var seed = sorted[i];
        var cluster = [seed];
        var clusterIdx = [i];
        for (var j = i + 1; j < n; j++) {
          if (used[j]) continue;
          var cand = sorted[j];
          // Must overlap AND share a region with every existing member so the
          // whole set is truly concurrent-under-one-ancestor.
          var ok = true;
          for (var m = 0; m < cluster.length; m++) {
            if (!intervalsOverlap(cluster[m], cand) || !shareRegion(cluster[m], cand)) {
              ok = false;
              break;
            }
          }
          if (ok) {
            cluster.push(cand);
            clusterIdx.push(j);
          }
        }

        if (cluster.length >= FRAGMENT_MIN) {
          for (var u = 0; u < clusterIdx.length; u++) used[clusterIdx[u]] = true;
          var ancestor = commonAncestorToken(cluster);
          var members = cluster.slice(0, FRAGMENT_MAX_MEMBERS).map(function(rec) {
            return rec.selector || rec.id || '?';
          });
          var msg = cluster.length + ' concurrent loaders under ' +
                    (ancestor || 'a shared ancestor') +
                    ' — consolidate to one master loader';
          var sel = ancestor || (seed.selector || '');
          var id = computeFindingID('spinner-fragmentation', sel, msg);
          // Register member selectors so they all highlight.
          for (var mm = 0; mm < cluster.length; mm++) {
            if (cluster[mm].selector) registerFinding(id, cluster[mm].selector);
          }
          addFinding({
            id: id,
            type: 'spinner-fragmentation',
            severity: 'warning',
            selector: sel,
            message: msg,
            ancestor: ancestor,
            count: cluster.length,
            members: members
          });
        }
      }
    }

    detectCascade();
    detectFragmentation();

    // === SCORING ===
    // Cascade: depth-weighted, heavier when critical. Fragmentation: base plus
    // members beyond the threshold.
    var score = 100;
    for (var fi = 0; fi < findings.length; fi++) {
      var f = findings[fi];
      var weight;
      if (f.type === 'spinner-cascade') {
        weight = 8 + 4 * ((f.depth || 2) - 2);
        if (f.severity === 'critical') weight += 6;
      } else if (f.type === 'spinner-fragmentation') {
        weight = 6 + Math.max(0, (f.count || FRAGMENT_MIN) - FRAGMENT_MIN);
      } else {
        weight = 0;
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
    if (score >= 80) summaryParts.push('Loading UX is good');
    else if (score >= 60) summaryParts.push('Loading UX is moderate');
    else summaryParts.push('Loading UX needs attention');
    summaryParts.push('(' + timeline.length + ' loaders analyzed)');
    var actionable = errorCount + warningCount;
    if (actionable > 0) {
      summaryParts.push(actionable + ' issue' + (actionable === 1 ? '' : 's') + ' to address');
    }
    var summary = summaryParts.join('. ');

    // === RAW RESPONSE ===
    if (raw) {
      return {
        audit: 'loading',
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
      audit: 'loading',
      score: score,
      grade: grade,
      summary: summary,
      checkedAt: new Date().toISOString(),
      stats: {
        total: timeline.length,
        errors: errorCount,
        warnings: warningCount,
        info: infoCount,
        totalIssues: findings.length
      },
      findingsByType: byType
    };
  }

  window.__devtool_audit_loading = {
    auditLoading: auditLoading
  };

  // Register into the shared audit namespace so it sits alongside the other
  // audit modules (mirrors how audit-api re-exports under __devtool.audit).
  if (!window.__devtool) { window.__devtool = {}; }
  if (!window.__devtool.audit) { window.__devtool.audit = {}; }
  window.__devtool.audit.auditLoading = auditLoading;
})();
