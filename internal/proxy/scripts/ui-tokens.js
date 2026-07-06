// Shared UI design tokens for all DevTool chrome surfaces.
//
// Loaded early (right after core, before every UI module) so that
// window.__devtoolTokens is available when indicator/toast/palette/design/
// style-editor build their style strings at module-eval time.
//
// Provides:
//   __devtoolTokens.z         - z-index layer scale (int32-safe; the old
//                               2147483648 literals overflowed int32 and were
//                               silently clamped by the browser)
//   __devtoolTokens.color     - light color palette
//   __devtoolTokens.colorDark - dark color palette (WCAG AA contrast on
//                               surface #1e293b)
//   __devtoolTokens.theme()   - palette for the current prefers-color-scheme
//   __devtoolTokens.isDark()  - current scheme flag
//   __devtoolTokens.motion()  - { reduce, transition(spec) } honoring
//                               prefers-reduced-motion
//   __devtoolOverlayStack     - Escape-key coordinator: one document-level
//                               keydown listener closes only the top-most
//                               registered overlay/panel.
//
// Theme changes fire a 'devtool:theme-change' CustomEvent on document.
// Consumers currently read theme() once at module-eval time; live re-theming
// of already-built style strings is not attempted.
(function() {
  'use strict';

  if (window.__devtoolTokens) return;

  var uiDarkQuery = null;
  var uiMotionQuery = null;
  try {
    if (window.matchMedia) {
      uiDarkQuery = window.matchMedia('(prefers-color-scheme: dark)');
      uiMotionQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
    }
  } catch (e) { /* matchMedia unavailable — default to light / full motion */ }

  var uiTokens = {
    // Layer scale. Ordering contract: highlight < overlay < panel < toast <
    // critical. All values fit in int32 (max 2147483647). Every UI module
    // must reference these instead of raw 21474836xx literals.
    z: {
      highlight: 2147483640, // element highlights, walkthrough hilites
      overlay: 2147483642,   // full-screen mode overlays (selection, sketch base)
      panel: 2147483644,     // floating panels (palette, style editor, docks)
      toast: 2147483646,     // toasts, the floating bug indicator
      critical: 2147483647   // instruction bars, capture overlays — top of world
    },
    // Light palette. Superset of every per-module TOKENS.colors key so the
    // module-level TOKENS blocks can source from theme() directly.
    color: {
      primary: '#6366f1',
      primaryDark: '#4f46e5',
      secondary: '#64748b',
      success: '#22c55e',
      error: '#ef4444',
      warning: '#f59e0b',
      info: '#3b82f6',
      active: '#f59e0b',
      chaos: '#a855f7',
      chaosDeep: '#7c3aed',
      surface: '#ffffff',
      surfaceAlt: '#f8fafc',
      border: '#e2e8f0',
      text: '#1e293b',
      textMuted: '#64748b',
      textInverse: '#ffffff'
    },
    // Dark palette. surface #1e293b / text #e2e8f0 ≈ 11.5:1, textMuted
    // #94a3b8 on #1e293b ≈ 5.6:1 — both clear WCAG AA.
    colorDark: {
      primary: '#818cf8',
      primaryDark: '#6366f1',
      secondary: '#94a3b8',
      success: '#4ade80',
      error: '#f87171',
      warning: '#fbbf24',
      info: '#60a5fa',
      active: '#fbbf24',
      chaos: '#c084fc',
      chaosDeep: '#a78bfa',
      surface: '#1e293b',
      surfaceAlt: '#0f172a',
      border: '#334155',
      text: '#e2e8f0',
      textMuted: '#94a3b8',
      textInverse: '#0f172a'
    },
    isDark: function() {
      return !!(uiDarkQuery && uiDarkQuery.matches);
    },
    theme: function() {
      return uiTokens.isDark() ? uiTokens.colorDark : uiTokens.color;
    },
    motion: function() {
      var reduce = !!(uiMotionQuery && uiMotionQuery.matches);
      return {
        reduce: reduce,
        // transition('opacity 0.3s ease') -> 'none' under reduced motion.
        transition: function(spec) { return reduce ? 'none' : spec; }
      };
    }
  };

  if (uiDarkQuery && uiDarkQuery.addEventListener) {
    uiDarkQuery.addEventListener('change', function() {
      try {
        document.dispatchEvent(new CustomEvent('devtool:theme-change', {
          detail: { dark: uiTokens.isDark() }
        }));
      } catch (e) { /* CustomEvent unsupported — nothing to notify */ }
    });
  }

  window.__devtoolTokens = uiTokens;

  // ---------------------------------------------------------------------------
  // Escape coordinator
  //
  // Each open devtool overlay/panel registers itself with push(id, closeFn).
  // A single capture-phase document keydown listener closes ONLY the top of
  // the stack on Escape, so nested surfaces (panel -> selection overlay)
  // unwind one layer per keypress instead of every surface tearing down at
  // once. When the stack is empty, Escape is left entirely to the page.
  // push() with an existing id re-registers it at the top; pop(id) is
  // idempotent so closeFn implementations may safely pop themselves.
  // ---------------------------------------------------------------------------
  var uiOverlayStack = [];

  window.__devtoolOverlayStack = {
    push: function(id, closeFn) {
      this.pop(id);
      uiOverlayStack.push({ id: id, close: closeFn });
    },
    pop: function(id) {
      for (var i = uiOverlayStack.length - 1; i >= 0; i--) {
        if (uiOverlayStack[i].id === id) {
          uiOverlayStack.splice(i, 1);
          return true;
        }
      }
      return false;
    },
    top: function() {
      return uiOverlayStack.length ? uiOverlayStack[uiOverlayStack.length - 1].id : null;
    },
    size: function() {
      return uiOverlayStack.length;
    }
  };

  document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    if (uiOverlayStack.length === 0) return; // nothing open — page owns Escape
    var entry = uiOverlayStack.pop();
    e.preventDefault();
    e.stopPropagation();
    try {
      entry.close();
    } catch (err) {
      // A broken closeFn must not wedge the coordinator for other overlays.
      console.error('[DevTool] overlay close failed', entry.id, err);
    }
  }, true);
})();
