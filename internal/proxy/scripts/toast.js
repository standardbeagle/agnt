// Toast Notification System for DevTool
// Displays temporary notifications to the user

(function() {
  'use strict';

  // Configuration (can be overridden via __devtool.toast.configure())
  var config = {
    duration: 4000,
    position: 'bottom-right', // top-right, top-left, bottom-right, bottom-left
    maxVisible: 3,
    gap: 10
  };

  // State
  var state = {
    container: null,
    toasts: [], // { id, element, timer }
    nextId: 1
  };

  // Design tokens (shared with indicator)
  var TOKENS = {
    colors: {
      surface: '#ffffff',
      text: '#1e293b',
      textMuted: '#64748b',
      border: '#e2e8f0',
      success: '#22c55e',
      error: '#ef4444',
      warning: '#f59e0b',
      info: '#3b82f6'
    },
    radius: {
      md: '10px'
    },
    shadow: {
      lg: '0 10px 40px rgba(0,0,0,0.15)'
    }
  };

  // Styles
  var STYLES = {
    container: [
      'position: fixed',
      'z-index: 2147483647',
      'display: flex',
      'flex-direction: column',
      'gap: 10px',
      'pointer-events: none',
      'max-width: 380px',
      'width: 100%',
      'padding: 20px'
    ].join(';'),

    toast: [
      'display: flex',
      'align-items: flex-start',
      'gap: 12px',
      'padding: 14px 16px',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.md,
      'box-shadow: ' + TOKENS.shadow.lg,
      'border-left: 4px solid ' + TOKENS.colors.info,
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      'font-size: 14px',
      'color: ' + TOKENS.colors.text,
      'pointer-events: auto',
      'opacity: 0',
      'transform: translateX(100%)',
      'transition: opacity 0.3s ease, transform 0.3s ease'
    ].join(';'),

    toastVisible: [
      'opacity: 1',
      'transform: translateX(0)'
    ].join(';'),

    toastExiting: [
      'opacity: 0',
      'transform: translateX(100%)'
    ].join(';'),

    icon: [
      'flex-shrink: 0',
      'width: 20px',
      'height: 20px'
    ].join(';'),

    content: [
      'flex: 1',
      'min-width: 0'
    ].join(';'),

    title: [
      'font-weight: 600',
      'margin-bottom: 2px',
      'line-height: 1.3'
    ].join(';'),

    message: [
      'color: ' + TOKENS.colors.textMuted,
      'line-height: 1.4',
      'word-wrap: break-word'
    ].join(';'),

    closeBtn: [
      'flex-shrink: 0',
      'background: none',
      'border: none',
      'padding: 2px',
      'cursor: pointer',
      'color: ' + TOKENS.colors.textMuted,
      'opacity: 0.6',
      'transition: opacity 0.15s ease'
    ].join(';'),

    progress: [
      'position: absolute',
      'bottom: 0',
      'left: 0',
      'height: 3px',
      'background: currentColor',
      'opacity: 0.3',
      'border-radius: 0 0 0 ' + TOKENS.radius.md
    ].join(';')
  };

  // Icons
  var ICONS = {
    success: '<svg viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>',
    error: '<svg viewBox="0 0 24 24" fill="none" stroke="#ef4444" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>',
    warning: '<svg viewBox="0 0 24 24" fill="none" stroke="#f59e0b" stroke-width="2"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>',
    info: '<svg viewBox="0 0 24 24" fill="none" stroke="#3b82f6" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>',
    close: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>'
  };

  // Initialize container
  function init() {
    if (state.container) return;

    state.container = document.createElement('div');
    state.container.id = '__devtool-toast-container';
    state.container.style.cssText = STYLES.container;
    updatePosition();

    document.documentElement.appendChild(state.container);
  }

  function updatePosition() {
    if (!state.container) return;

    var pos = config.position.split('-');
    var vertical = pos[0]; // top or bottom
    var horizontal = pos[1]; // left or right

    // Reset all positioning
    state.container.style.top = 'auto';
    state.container.style.bottom = 'auto';
    state.container.style.left = 'auto';
    state.container.style.right = 'auto';

    // Set position
    if (vertical === 'top') {
      state.container.style.top = '0';
      state.container.style.flexDirection = 'column';
    } else {
      state.container.style.bottom = '0';
      state.container.style.flexDirection = 'column-reverse';
    }

    if (horizontal === 'left') {
      state.container.style.left = '0';
    } else {
      state.container.style.right = '0';
    }

    // Update slide direction for toasts
    var slideDir = horizontal === 'left' ? '-100%' : '100%';
    STYLES.toast = STYLES.toast.replace(/translateX\([^)]+\)/g, 'translateX(' + slideDir + ')');
    STYLES.toastExiting = 'opacity: 0; transform: translateX(' + slideDir + ')';
  }

  // Create a toast element
  function createToastElement(options) {
    var toast = document.createElement('div');
    toast.style.cssText = STYLES.toast;
    toast.style.position = 'relative';

    // Set border color based on type
    var borderColor = TOKENS.colors[options.type] || TOKENS.colors.info;
    toast.style.borderLeftColor = borderColor;

    // Icon
    if (options.type && ICONS[options.type]) {
      var icon = document.createElement('div');
      icon.style.cssText = STYLES.icon;
      icon.innerHTML = ICONS[options.type];
      toast.appendChild(icon);
    }

    // Content
    var content = document.createElement('div');
    content.style.cssText = STYLES.content;

    if (options.title) {
      var title = document.createElement('div');
      title.style.cssText = STYLES.title;
      title.textContent = options.title;
      content.appendChild(title);
    }

    if (options.message) {
      var message = document.createElement('div');
      message.style.cssText = STYLES.message;
      message.textContent = options.message;
      content.appendChild(message);
    }

    toast.appendChild(content);

    // Close button
    var closeBtn = document.createElement('button');
    closeBtn.style.cssText = STYLES.closeBtn;
    closeBtn.innerHTML = ICONS.close;
    closeBtn.onmouseenter = function() { closeBtn.style.opacity = '1'; };
    closeBtn.onmouseleave = function() { closeBtn.style.opacity = '0.6'; };
    toast.appendChild(closeBtn);

    // Progress bar (optional)
    if (options.showProgress !== false) {
      var progress = document.createElement('div');
      progress.style.cssText = STYLES.progress;
      progress.style.color = borderColor;
      progress.style.width = '100%';
      progress.style.transition = 'width ' + (options.duration || config.duration) + 'ms linear';
      toast.appendChild(progress);

      // Start progress animation after a tick
      requestAnimationFrame(function() {
        progress.style.width = '0%';
      });
    }

    return { element: toast, closeBtn: closeBtn };
  }

  // Show a toast
  function show(options) {
    init();

    // Handle string shorthand
    if (typeof options === 'string') {
      options = { message: options };
    }

    var id = state.nextId++;
    var duration = options.duration || config.duration;

    // Remove excess toasts
    while (state.toasts.length >= config.maxVisible) {
      dismiss(state.toasts[0].id);
    }

    // Create toast
    var toastData = createToastElement(options);
    var toastObj = {
      id: id,
      element: toastData.element,
      timer: null
    };

    // Close button handler
    toastData.closeBtn.onclick = function() {
      dismiss(id);
    };

    // Add to state and DOM
    state.toasts.push(toastObj);
    state.container.appendChild(toastData.element);

    // Animate in
    requestAnimationFrame(function() {
      toastData.element.style.cssText = STYLES.toast + ';' + STYLES.toastVisible;
      toastData.element.style.borderLeftColor = TOKENS.colors[options.type] || TOKENS.colors.info;
    });

    // Auto dismiss
    if (duration > 0) {
      toastObj.timer = setTimeout(function() {
        dismiss(id);
      }, duration);
    }

    // Pause timer on hover
    toastData.element.onmouseenter = function() {
      if (toastObj.timer) {
        clearTimeout(toastObj.timer);
        toastObj.timer = null;
      }
    };

    toastData.element.onmouseleave = function() {
      if (duration > 0 && !toastObj.timer) {
        toastObj.timer = setTimeout(function() {
          dismiss(id);
        }, 1000); // Short delay after hover
      }
    };

    return id;
  }

  // Dismiss a toast
  function dismiss(id) {
    var index = -1;
    for (var i = 0; i < state.toasts.length; i++) {
      if (state.toasts[i].id === id) {
        index = i;
        break;
      }
    }

    if (index === -1) return;

    var toastObj = state.toasts[index];

    // Clear timer
    if (toastObj.timer) {
      clearTimeout(toastObj.timer);
    }

    // Animate out
    var slideDir = config.position.includes('left') ? '-100%' : '100%';
    toastObj.element.style.opacity = '0';
    toastObj.element.style.transform = 'translateX(' + slideDir + ')';

    // Remove after animation
    setTimeout(function() {
      if (toastObj.element.parentNode) {
        toastObj.element.parentNode.removeChild(toastObj.element);
      }
    }, 300);

    // Remove from state
    state.toasts.splice(index, 1);
  }

  // Dismiss all toasts
  function dismissAll() {
    var ids = state.toasts.map(function(t) { return t.id; });
    ids.forEach(dismiss);
  }

  // Configure toast system
  function configure(options) {
    if (options.duration !== undefined) config.duration = options.duration;
    if (options.position !== undefined) {
      config.position = options.position;
      updatePosition();
    }
    if (options.maxVisible !== undefined) config.maxVisible = options.maxVisible;
  }

  // Convenience methods
  function success(message, title) {
    return show({ type: 'success', message: message, title: title });
  }

  function error(message, title) {
    return show({ type: 'error', message: message, title: title || 'Error' });
  }

  function warning(message, title) {
    return show({ type: 'warning', message: message, title: title || 'Warning' });
  }

  function info(message, title) {
    return show({ type: 'info', message: message, title: title });
  }

  // Listen for toast messages from WebSocket (via core message handlers)
  function handleMessage(message) {
    if (message.type === 'toast') {
      var payload = message.payload || message;
      show({
        type: payload.type || 'info',
        title: payload.title,
        message: payload.message,
        duration: payload.duration
      });
    }
  }

  // Register message handler if core is available
  if (window.__devtool_core && window.__devtool_core.onMessage) {
    window.__devtool_core.onMessage(handleMessage);
  }

  // Export
  window.__devtool_toast = {
    show: show,
    dismiss: dismiss,
    dismissAll: dismissAll,
    configure: configure,
    success: success,
    error: error,
    warning: warning,
    info: info
  };
})();
