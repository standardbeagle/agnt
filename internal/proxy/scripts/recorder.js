// Session recorder for DevTool
// Records user interactions for replay after page refresh

(function() {
  'use strict';

  var core = window.__devtool_core;
  var utils = window.__devtool_utils;

  var STORAGE_KEY = '__devtool_recording';

  var state = {
    isRecording: false,
    isReplaying: false,
    events: [],
    startTime: null,
    replayIndex: 0,
    replayTimeout: null
  };

  /**
   * Get relative timestamp
   */
  function getTime() {
    return state.startTime ? Date.now() - state.startTime : 0;
  }

  /**
   * Record an interaction event
   */
  function recordEvent(type, target, data) {
    if (!state.isRecording) return;

    var selector = null;
    var xpath = null;

    if (target && target.nodeType === 1) {
      selector = utils.generateSelector(target);
      xpath = getXPath(target);
    }

    state.events.push({
      t: getTime(),           // timestamp (relative ms)
      type: type,             // click, input, change, scroll, etc.
      selector: selector,
      xpath: xpath,
      data: data || {}
    });
  }

  /**
   * Get XPath for element (more reliable for replay)
   */
  function getXPath(element) {
    if (!element) return null;
    if (element.id) return '//*[@id="' + element.id + '"]';

    var parts = [];
    while (element && element.nodeType === 1) {
      var index = 1;
      var sibling = element.previousSibling;
      while (sibling) {
        if (sibling.nodeType === 1 && sibling.tagName === element.tagName) index++;
        sibling = sibling.previousSibling;
      }
      parts.unshift(element.tagName.toLowerCase() + '[' + index + ']');
      element = element.parentNode;
    }
    return '/' + parts.join('/');
  }

  /**
   * Find element by selector or xpath
   */
  function findElement(event) {
    var el = null;

    // Try selector first
    if (event.selector) {
      try { el = document.querySelector(event.selector); } catch (e) {}
    }

    // Fall back to xpath
    if (!el && event.xpath) {
      try {
        var result = document.evaluate(event.xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
        el = result.singleNodeValue;
      } catch (e) {}
    }

    return el;
  }

  /**
   * Event handlers for recording
   */
  function onClick(e) {
    // Skip agnt/devtool UI elements
    if (utils.isDevtoolElement && utils.isDevtoolElement(e.target)) return;
    recordEvent('click', e.target, {
      x: e.clientX,
      y: e.clientY,
      button: e.button
    });
  }

  function onInput(e) {
    // Skip agnt/devtool UI elements
    if (utils.isDevtoolElement && utils.isDevtoolElement(e.target)) return;
    recordEvent('input', e.target, {
      value: e.target.value
    });
  }

  function onChange(e) {
    // Skip agnt/devtool UI elements
    if (utils.isDevtoolElement && utils.isDevtoolElement(e.target)) return;
    var data = { value: e.target.value };
    if (e.target.type === 'checkbox' || e.target.type === 'radio') {
      data.checked = e.target.checked;
    }
    if (e.target.tagName === 'SELECT') {
      data.selectedIndex = e.target.selectedIndex;
    }
    recordEvent('change', e.target, data);
  }

  function onKeydown(e) {
    // Skip agnt/devtool UI elements
    if (utils.isDevtoolElement && utils.isDevtoolElement(e.target)) return;
    // Only record special keys (Enter, Tab, Escape, etc.)
    if (['Enter', 'Tab', 'Escape', 'Backspace', 'Delete'].includes(e.key) || e.ctrlKey || e.metaKey) {
      recordEvent('keydown', e.target, {
        key: e.key,
        code: e.code,
        ctrl: e.ctrlKey,
        meta: e.metaKey,
        shift: e.shiftKey
      });
    }
  }

  function onScroll() {
    // Debounced scroll recording
    if (state.scrollTimeout) clearTimeout(state.scrollTimeout);
    state.scrollTimeout = setTimeout(function() {
      recordEvent('scroll', document.documentElement, {
        x: window.scrollX,
        y: window.scrollY
      });
    }, 200);
  }

  function onSubmit(e) {
    // Skip agnt/devtool UI elements
    if (utils.isDevtoolElement && utils.isDevtoolElement(e.target)) return;
    recordEvent('submit', e.target, {});
  }

  /**
   * Start recording
   */
  function start() {
    if (state.isRecording) return { error: 'Already recording' };

    state.isRecording = true;
    state.startTime = Date.now();
    state.events = [];

    // Record initial state
    state.events.push({
      t: 0,
      type: 'init',
      data: {
        url: window.location.href,
        scroll: { x: window.scrollX, y: window.scrollY }
      }
    });

    // Attach listeners
    document.addEventListener('click', onClick, true);
    document.addEventListener('input', onInput, true);
    document.addEventListener('change', onChange, true);
    document.addEventListener('keydown', onKeydown, true);
    document.addEventListener('scroll', onScroll, true);
    document.addEventListener('submit', onSubmit, true);

    // Show indicator
    showRecordingIndicator();

    return { status: 'recording', startTime: state.startTime };
  }

  /**
   * Stop recording and save
   */
  function stop() {
    if (!state.isRecording) return { error: 'Not recording' };

    state.isRecording = false;

    // Remove listeners
    document.removeEventListener('click', onClick, true);
    document.removeEventListener('input', onInput, true);
    document.removeEventListener('change', onChange, true);
    document.removeEventListener('keydown', onKeydown, true);
    document.removeEventListener('scroll', onScroll, true);
    document.removeEventListener('submit', onSubmit, true);

    hideRecordingIndicator();

    var recording = {
      id: 'rec_' + Date.now().toString(36),
      url: window.location.href,
      duration: getTime(),
      events: state.events,
      created: Date.now()
    };

    // Save to sessionStorage for replay after refresh
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(recording));
    } catch (e) {}

    // Send to proxy
    if (core && core.send) {
      core.send('recording', { recording: recording });
    }

    if (window.__devtool && window.__devtool.toast) {
      window.__devtool.toast.success('Recorded ' + state.events.length + ' events', { duration: 3000 });
    }

    return recording;
  }

  /**
   * Replay a recording
   */
  function replay(recording, options) {
    if (state.isReplaying) return { error: 'Already replaying' };
    if (!recording) {
      // Try to load from storage
      try {
        var stored = sessionStorage.getItem(STORAGE_KEY);
        if (stored) recording = JSON.parse(stored);
      } catch (e) {}
    }
    if (!recording || !recording.events) return { error: 'No recording' };

    options = options || {};
    var startIndex = options.startIndex || 0;
    var speed = options.speed || 1;

    state.isReplaying = true;
    state.replayIndex = startIndex;

    showReplayIndicator(recording.events.length);

    function playNext() {
      if (!state.isReplaying || state.replayIndex >= recording.events.length) {
        stopReplay();
        return;
      }

      var event = recording.events[state.replayIndex];
      executeEvent(event);
      updateReplayIndicator(state.replayIndex, recording.events.length);

      state.replayIndex++;

      if (state.replayIndex < recording.events.length) {
        var nextEvent = recording.events[state.replayIndex];
        var delay = (nextEvent.t - event.t) / speed;
        delay = Math.max(50, Math.min(delay, 3000));
        state.replayTimeout = setTimeout(playNext, delay);
      } else {
        stopReplay();
      }
    }

    // Start after short delay
    setTimeout(playNext, 500);

    return { status: 'replaying', total: recording.events.length };
  }

  /**
   * Execute a single recorded event
   */
  function executeEvent(event) {
    var el = findElement(event);

    switch (event.type) {
      case 'init':
        window.scrollTo(event.data.scroll.x, event.data.scroll.y);
        break;

      case 'click':
        if (el) {
          highlightElement(el, '#22c55e');
          el.click();
        }
        break;

      case 'input':
        if (el) {
          highlightElement(el, '#3b82f6');
          el.value = event.data.value;
          el.dispatchEvent(new Event('input', { bubbles: true }));
        }
        break;

      case 'change':
        if (el) {
          highlightElement(el, '#3b82f6');
          if (event.data.checked !== undefined) {
            el.checked = event.data.checked;
          } else if (event.data.selectedIndex !== undefined) {
            el.selectedIndex = event.data.selectedIndex;
          } else {
            el.value = event.data.value;
          }
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
        break;

      case 'keydown':
        if (el) {
          highlightElement(el, '#a855f7');
          var keyEvent = new KeyboardEvent('keydown', {
            key: event.data.key,
            code: event.data.code,
            ctrlKey: event.data.ctrl,
            metaKey: event.data.meta,
            shiftKey: event.data.shift,
            bubbles: true
          });
          el.dispatchEvent(keyEvent);

          // For Enter, also trigger form submit if in form
          if (event.data.key === 'Enter') {
            var form = el.closest('form');
            if (form) form.dispatchEvent(new Event('submit', { bubbles: true }));
          }
        }
        break;

      case 'scroll':
        window.scrollTo(event.data.x, event.data.y);
        break;

      case 'submit':
        if (el && el.tagName === 'FORM') {
          highlightElement(el, '#f59e0b');
          el.dispatchEvent(new Event('submit', { bubbles: true }));
        }
        break;
    }
  }

  /**
   * Stop replay
   */
  function stopReplay() {
    state.isReplaying = false;
    if (state.replayTimeout) {
      clearTimeout(state.replayTimeout);
      state.replayTimeout = null;
    }
    hideReplayIndicator();

    if (window.__devtool && window.__devtool.toast) {
      window.__devtool.toast.info('Replay finished');
    }

    return { status: 'stopped' };
  }

  /**
   * Replay from a specific event index
   */
  function replayFrom(index) {
    var recording = null;
    try {
      var stored = sessionStorage.getItem(STORAGE_KEY);
      if (stored) recording = JSON.parse(stored);
    } catch (e) {}

    if (!recording) return { error: 'No recording' };
    return replay(recording, { startIndex: index });
  }

  /**
   * Get current recording
   */
  function getRecording() {
    try {
      var stored = sessionStorage.getItem(STORAGE_KEY);
      if (stored) return JSON.parse(stored);
    } catch (e) {}
    return null;
  }

  /**
   * Clear stored recording
   */
  function clear() {
    try {
      sessionStorage.removeItem(STORAGE_KEY);
    } catch (e) {}
    state.events = [];
    return { status: 'cleared' };
  }

  /**
   * Highlight element during replay
   */
  function highlightElement(el, color) {
    var overlay = document.createElement('div');
    overlay.className = '__devtool-replay-highlight';
    var rect = el.getBoundingClientRect();
    overlay.style.cssText = [
      'position: fixed',
      'top: ' + rect.top + 'px',
      'left: ' + rect.left + 'px',
      'width: ' + rect.width + 'px',
      'height: ' + rect.height + 'px',
      'background: ' + color + '40',
      'border: 2px solid ' + color,
      'border-radius: 4px',
      'pointer-events: none',
      'z-index: 2147483646',
      'transition: opacity 0.3s'
    ].join(';');
    document.body.appendChild(overlay);
    setTimeout(function() {
      overlay.style.opacity = '0';
      setTimeout(function() { overlay.remove(); }, 300);
    }, 500);
  }

  /**
   * Show recording indicator (red dot)
   */
  function showRecordingIndicator() {
    var indicator = document.createElement('div');
    indicator.id = '__devtool-rec-indicator';
    indicator.style.cssText = [
      'position: fixed',
      'top: 10px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: #ef4444',
      'color: white',
      'padding: 6px 14px',
      'border-radius: 20px',
      'font: 13px -apple-system, sans-serif',
      'z-index: 2147483647',
      'display: flex',
      'align-items: center',
      'gap: 8px',
      'box-shadow: 0 2px 10px rgba(0,0,0,0.2)'
    ].join(';');
    indicator.innerHTML = '<span style="width:8px;height:8px;background:white;border-radius:50%;animation:__devtool-pulse 1s infinite"></span> Recording';

    var style = document.createElement('style');
    style.textContent = '@keyframes __devtool-pulse { 0%,100% { opacity:1; } 50% { opacity:0.5; } }';
    indicator.appendChild(style);

    document.body.appendChild(indicator);
  }

  function hideRecordingIndicator() {
    var el = document.getElementById('__devtool-rec-indicator');
    if (el) el.remove();
  }

  /**
   * Show replay indicator
   */
  function showReplayIndicator(total) {
    var indicator = document.createElement('div');
    indicator.id = '__devtool-replay-indicator';
    indicator.style.cssText = [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: rgba(17,24,39,0.95)',
      'color: white',
      'padding: 10px 20px',
      'border-radius: 10px',
      'font: 13px -apple-system, sans-serif',
      'z-index: 2147483647',
      'display: flex',
      'align-items: center',
      'gap: 12px',
      'box-shadow: 0 4px 20px rgba(0,0,0,0.3)'
    ].join(';');

    indicator.innerHTML = [
      '<span style="color:#6366f1">Replaying</span>',
      '<div style="width:120px;height:4px;background:rgba(255,255,255,0.2);border-radius:2px;overflow:hidden">',
      '<div id="__devtool-replay-progress" style="width:0%;height:100%;background:#6366f1;transition:width 0.1s"></div>',
      '</div>',
      '<span id="__devtool-replay-count">0/' + total + '</span>',
      '<button onclick="window.__devtool_recorder.stopReplay()" style="background:#ef4444;border:none;color:white;padding:4px 10px;border-radius:6px;cursor:pointer;font-size:12px">Stop</button>'
    ].join('');

    document.body.appendChild(indicator);
  }

  function updateReplayIndicator(current, total) {
    var progress = document.getElementById('__devtool-replay-progress');
    var count = document.getElementById('__devtool-replay-count');
    if (progress) progress.style.width = ((current / total) * 100) + '%';
    if (count) count.textContent = current + '/' + total;
  }

  function hideReplayIndicator() {
    var el = document.getElementById('__devtool-replay-indicator');
    if (el) el.remove();
  }

  /**
   * Get status
   */
  function getStatus() {
    return {
      isRecording: state.isRecording,
      isReplaying: state.isReplaying,
      eventCount: state.events.length,
      hasRecording: !!getRecording()
    };
  }

  // Export API
  window.__devtool_recorder = {
    start: start,
    stop: stop,
    replay: replay,
    replayFrom: replayFrom,
    stopReplay: stopReplay,
    getRecording: getRecording,
    getStatus: getStatus,
    clear: clear
  };
})();
