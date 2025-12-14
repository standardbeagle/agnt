// Design Iteration Module
// Enables iterative design exploration by selecting elements and generating alternatives

(function() {
  'use strict';

  // Use getters to ensure modules are available at call time, not at parse time
  function getCore() { return window.__devtool_core; }
  function getUtils() { return window.__devtool_utils; }

  // State
  var state = {
    isActive: false,
    selectedElement: null, // DOM element
    selector: null,        // CSS selector
    xpath: null,           // XPath for robustness
    originalHTML: '',
    currentIndex: 0,
    alternatives: [],      // Array of HTML strings
    contextHTML: '',       // Parent context for LLM
    metadata: null,        // Element metadata
    chatHistory: [],       // Chat messages about this element
    overlay: null,         // Selection overlay
    controls: null         // Navigation controls UI
  };

  // Start design mode - enable element selection
  function start() {
    if (state.isActive) return;
    state.isActive = true;
    showSelectionOverlay();
  }

  // Stop design mode
  function stop() {
    if (!state.isActive) return;
    state.isActive = false;
    hideSelectionOverlay();
    hideControls();
    clearSelection();
  }

  // Show overlay for element selection
  function showSelectionOverlay() {
    var overlay = document.createElement('div');
    overlay.id = '__devtool-design-overlay';
    overlay.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'z-index: 2147483647',
      'cursor: crosshair',
      'background: rgba(99, 102, 241, 0.05)'
    ].join(';');

    var highlight = document.createElement('div');
    highlight.id = '__devtool-design-highlight';
    highlight.style.cssText = [
      'position: absolute',
      'border: 2px solid #6366f1',
      'background: rgba(99, 102, 241, 0.1)',
      'pointer-events: none',
      'border-radius: 4px',
      'display: none'
    ].join(';');
    overlay.appendChild(highlight);

    var tooltip = document.createElement('div');
    tooltip.id = '__devtool-design-tooltip';
    tooltip.style.cssText = [
      'position: absolute',
      'background: #1e293b',
      'color: white',
      'padding: 4px 8px',
      'border-radius: 6px',
      'font-size: 11px',
      'font-family: ui-monospace, monospace',
      'white-space: nowrap',
      'pointer-events: none',
      'display: none'
    ].join(';');
    overlay.appendChild(tooltip);

    var instructions = document.createElement('div');
    instructions.style.cssText = [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: #1e293b',
      'color: white',
      'padding: 10px 20px',
      'border-radius: 9999px',
      'font-size: 13px',
      'font-weight: 500',
      'box-shadow: 0 10px 40px rgba(0,0,0,0.15)',
      'z-index: 2147483648'
    ].join(';');
    instructions.textContent = 'Click an element to start design iteration • ESC to cancel';
    overlay.appendChild(instructions);

    var hoveredElement = null;

    overlay.addEventListener('mousemove', function(e) {
      overlay.style.pointerEvents = 'none';
      var el = document.elementFromPoint(e.clientX, e.clientY);
      overlay.style.pointerEvents = 'auto';

      // Ignore devtool elements
      if (!el || el.id && el.id.startsWith('__devtool')) {
        highlight.style.display = 'none';
        tooltip.style.display = 'none';
        hoveredElement = null;
        return;
      }

      hoveredElement = el;
      var rect = el.getBoundingClientRect();

      highlight.style.display = 'block';
      highlight.style.left = rect.left + 'px';
      highlight.style.top = rect.top + 'px';
      highlight.style.width = rect.width + 'px';
      highlight.style.height = rect.height + 'px';

      var selector = getUtils().generateSelector(el);
      tooltip.textContent = selector;
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(rect.left, window.innerWidth - 200) + 'px';
      tooltip.style.top = Math.max(rect.top - 28, 5) + 'px';
    });

    overlay.addEventListener('click', function(e) {
      e.preventDefault();
      e.stopPropagation();

      if (hoveredElement) {
        selectElement(hoveredElement);
      }
    });

    function handleEscape(e) {
      if (e.key === 'Escape') {
        stop();
        document.removeEventListener('keydown', handleEscape);
      }
    }
    document.addEventListener('keydown', handleEscape);

    state.overlay = overlay;
    document.body.appendChild(overlay);
  }

  // Hide selection overlay
  function hideSelectionOverlay() {
    if (state.overlay) {
      if (state.overlay.parentNode) {
        state.overlay.parentNode.removeChild(state.overlay);
      }
      state.overlay = null;
    }
  }

  // Select an element for design iteration
  function selectElement(element) {
    if (!element) return;

    // Hide selection overlay
    hideSelectionOverlay();

    // Store element reference
    state.selectedElement = element;
    state.selector = getUtils().generateSelector(element);
    state.xpath = generateXPath(element);
    state.originalHTML = element.innerHTML;
    state.currentIndex = 0;
    state.alternatives = [element.innerHTML]; // Start with original

    // Capture context (parent with siblings)
    state.contextHTML = captureContext(element);

    // Capture metadata
    state.metadata = {
      tag: element.tagName.toLowerCase(),
      id: element.id || null,
      classes: Array.from(element.classList),
      attributes: captureAttributes(element),
      text: element.textContent.trim().substring(0, 100),
      rect: {
        width: element.offsetWidth,
        height: element.offsetHeight
      }
    };

    // Reset chat history
    state.chatHistory = [];

    // Show navigation controls
    showControls();

    // Send initial state to agent
    sendDesignState();
  }

  // Generate XPath for element
  function generateXPath(element) {
    if (element.id) {
      return '//*[@id="' + element.id + '"]';
    }

    var path = '';
    var node = element;

    while (node && node.nodeType === Node.ELEMENT_NODE) {
      var index = 0;
      var sibling = node.previousSibling;

      while (sibling) {
        if (sibling.nodeType === Node.ELEMENT_NODE && sibling.nodeName === node.nodeName) {
          index++;
        }
        sibling = sibling.previousSibling;
      }

      var tagName = node.nodeName.toLowerCase();
      var pathIndex = index > 0 ? '[' + (index + 1) + ']' : '';
      path = '/' + tagName + pathIndex + path;

      node = node.parentNode;
    }

    return path;
  }

  // Capture parent context with siblings
  function captureContext(element) {
    var parent = element.parentElement;
    if (!parent) return '';

    // Clone parent and truncate sibling content for brevity
    var clone = parent.cloneNode(true);
    var children = clone.children;

    for (var i = 0; i < children.length; i++) {
      var child = children[i];
      var originalChild = parent.children[i];

      if (originalChild === element) {
        // Mark the target element
        child.setAttribute('data-design-target', 'true');
      } else {
        // Truncate siblings to just their tag
        var summary = '<' + child.tagName.toLowerCase();
        if (child.className) summary += ' class="' + child.className + '"';
        if (child.id) summary += ' id="' + child.id + '"';
        summary += '>...</' + child.tagName.toLowerCase() + '>';

        child.outerHTML = summary;
      }
    }

    return clone.outerHTML;
  }

  // Capture relevant attributes
  function captureAttributes(element) {
    var attrs = {};
    var relevantAttrs = ['href', 'src', 'type', 'placeholder', 'alt', 'title', 'role', 'aria-label'];

    relevantAttrs.forEach(function(name) {
      if (element.hasAttribute(name)) {
        attrs[name] = element.getAttribute(name);
      }
    });

    return attrs;
  }

  // Show navigation controls
  function showControls() {
    var controls = document.createElement('div');
    controls.id = '__devtool-design-controls';
    controls.style.cssText = [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: white',
      'border: 1px solid #e2e8f0',
      'border-radius: 12px',
      'padding: 12px',
      'box-shadow: 0 10px 40px rgba(0,0,0,0.15)',
      'z-index: 2147483646',
      'display: flex',
      'align-items: center',
      'gap: 12px',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif'
    ].join(';');

    // Previous button
    var prevBtn = createNavButton('◀ Prev', function() {
      previous();
    });
    controls.appendChild(prevBtn);

    // Current index display
    var indexDisplay = document.createElement('div');
    indexDisplay.id = '__devtool-design-index';
    indexDisplay.style.cssText = [
      'font-size: 13px',
      'color: #64748b',
      'min-width: 60px',
      'text-align: center'
    ].join(';');
    updateIndexDisplay();
    controls.appendChild(indexDisplay);

    // Next button
    var nextBtn = createNavButton('Next ▶', function() {
      next();
    });
    controls.appendChild(nextBtn);

    // Chat input
    var chatInput = document.createElement('input');
    chatInput.id = '__devtool-design-chat';
    chatInput.type = 'text';
    chatInput.placeholder = 'Describe changes...';
    chatInput.style.cssText = [
      'border: 1px solid #e2e8f0',
      'border-radius: 10px',
      'padding: 8px 12px',
      'font-size: 14px',
      'font-family: inherit',
      'line-height: 1.5',
      'outline: none',
      'width: 200px',
      'color: #1e293b',
      'background: #ffffff',
      'box-shadow: inset 0 1px 2px rgba(0,0,0,0.05)',
      'transition: border-color 0.2s ease, box-shadow 0.2s ease'
    ].join(';');
    chatInput.addEventListener('focus', function() {
      chatInput.style.borderColor = '#6366f1';
      chatInput.style.boxShadow = '0 0 0 3px rgba(99,102,241,0.1)';
    });
    chatInput.addEventListener('blur', function() {
      chatInput.style.borderColor = '#e2e8f0';
      chatInput.style.boxShadow = 'inset 0 1px 2px rgba(0,0,0,0.05)';
    });
    chatInput.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' && chatInput.value.trim()) {
        chat(chatInput.value.trim());
        chatInput.value = '';
      }
    });
    controls.appendChild(chatInput);

    // Close button
    var closeBtn = createNavButton('✕', function() {
      stop();
    });
    closeBtn.style.marginLeft = '8px';
    controls.appendChild(closeBtn);

    state.controls = controls;
    document.body.appendChild(controls);
  }

  // Create navigation button
  function createNavButton(label, onClick) {
    var btn = document.createElement('button');
    btn.textContent = label;
    btn.style.cssText = [
      'padding: 6px 12px',
      'background: #6366f1',
      'color: white',
      'border: none',
      'border-radius: 6px',
      'font-size: 13px',
      'font-weight: 500',
      'cursor: pointer',
      'transition: background 0.15s ease'
    ].join(';');
    btn.addEventListener('mouseenter', function() {
      btn.style.background = '#4f46e5';
    });
    btn.addEventListener('mouseleave', function() {
      btn.style.background = '#6366f1';
    });
    btn.addEventListener('click', onClick);
    return btn;
  }

  // Update index display
  function updateIndexDisplay() {
    var display = document.getElementById('__devtool-design-index');
    if (display) {
      display.textContent = (state.currentIndex + 1) + ' / ' + state.alternatives.length;
    }
  }

  // Hide navigation controls
  function hideControls() {
    if (state.controls) {
      if (state.controls.parentNode) {
        state.controls.parentNode.removeChild(state.controls);
      }
      state.controls = null;
    }
  }

  // Navigate to next alternative
  function next() {
    if (state.currentIndex < state.alternatives.length - 1) {
      state.currentIndex++;
      applyAlternative(state.currentIndex);
      updateIndexDisplay();
    } else {
      // No more alternatives - request new ones from agent
      requestAlternatives();
    }
  }

  // Navigate to previous alternative
  function previous() {
    if (state.currentIndex > 0) {
      state.currentIndex--;
      applyAlternative(state.currentIndex);
      updateIndexDisplay();
    }
  }

  // Apply an alternative HTML to the selected element
  function applyAlternative(index) {
    if (index < 0 || index >= state.alternatives.length) return;
    if (!state.selectedElement) return;

    var html = state.alternatives[index];
    state.selectedElement.innerHTML = html;
    state.currentIndex = index;
  }

  // Add a new alternative (called from proxy exec)
  function addAlternative(html) {
    if (!state.selectedElement) {
      return { error: 'No element selected' };
    }

    state.alternatives.push(html);
    state.currentIndex = state.alternatives.length - 1;
    applyAlternative(state.currentIndex);
    updateIndexDisplay();

    return {
      success: true,
      index: state.currentIndex,
      total: state.alternatives.length
    };
  }

  // Request new alternatives from agent
  function requestAlternatives() {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    console.log('[Design] Requesting alternatives for:', state.selector);
    core.send('design_request', {
      timestamp: Date.now(),
      selector: state.selector,
      xpath: state.xpath,
      currentHTML: state.alternatives[state.currentIndex],
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      alternativesCount: state.alternatives.length,
      chatHistory: state.chatHistory
    });
  }

  // Send design state to agent
  function sendDesignState() {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    console.log('[Design] Sending design state for:', state.selector);
    core.send('design_state', {
      timestamp: Date.now(),
      selector: state.selector,
      xpath: state.xpath,
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      url: window.location.href
    });
  }

  // Chat with LLM about current element
  function chat(message) {
    if (!state.selectedElement) {
      console.error('[Design] No element selected');
      return;
    }

    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    state.chatHistory.push({
      timestamp: Date.now(),
      message: message,
      role: 'user'
    });

    core.send('design_chat', {
      timestamp: Date.now(),
      message: message,
      selector: state.selector,
      xpath: state.xpath,
      currentHTML: state.alternatives[state.currentIndex],
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      chatHistory: state.chatHistory,
      url: window.location.href
    });

    // Request alternatives based on chat
    requestAlternatives();
  }

  // Get current state
  function getState() {
    return {
      isActive: state.isActive,
      hasSelection: !!state.selectedElement,
      selector: state.selector,
      currentIndex: state.currentIndex,
      alternativesCount: state.alternatives.length,
      metadata: state.metadata,
      chatHistory: state.chatHistory
    };
  }

  // Clear selection
  function clearSelection() {
    state.selectedElement = null;
    state.selector = null;
    state.xpath = null;
    state.originalHTML = '';
    state.currentIndex = 0;
    state.alternatives = [];
    state.contextHTML = '';
    state.metadata = null;
    state.chatHistory = [];
  }

  // Export public API
  window.__devtool_design = {
    start: start,
    stop: stop,
    selectElement: selectElement,
    next: next,
    previous: previous,
    addAlternative: addAlternative,
    applyAlternative: applyAlternative,
    chat: chat,
    getState: getState
  };
})();
