/**
 * Response Stream Panel - Standalone Version
 *
 * A lightweight, framework-agnostic implementation of the Response Stream Panel
 * that can be used directly in the browser without React.
 *
 * Features:
 * - Real-time streaming content display
 * - Animated typing indicator
 * - Expandable/collapsible panel
 * - Session history
 * - Keyboard shortcuts
 * - Accessible controls
 */

(function() {
  'use strict';

  // ============================================================================
  // CONFIGURATION
  // ============================================================================

  const DEFAULT_CONFIG = {
    position: 'bottom-right',
    width: 400,
    maxContentLength: 8000,
    maxHistoryItems: 20,
    showLineNumbers: false,
    typewriterEffect: true,
    animationSpeed: 30,
    typingAnimationStyle: 'waveform',
    autoScroll: true,
    zIndex: 99998,
  };

  // ============================================================================
  // STATE MANAGEMENT
  // ============================================================================

  const state = {
    isExpanded: true,
    isVisible: true,
    isStreaming: false,
    currentContent: '',
    messages: [],
    status: 'idle',
    error: null,
    config: { ...DEFAULT_CONFIG },
  };

  const listeners = new Set();
  const contentBuffer = [];

  // ============================================================================
  // UTILITY FUNCTIONS
  // ============================================================================

  function generateId(prefix = 'rsp') {
    return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
  }

  function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  function formatTimestamp(timestamp) {
    return new Date(timestamp).toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit'
    });
  }

  function getStatusColor(status) {
    const colors = {
      idle: '#686870',
      thinking: '#f59e0b',
      responding: '#00d4aa',
      complete: '#686870',
      error: '#ff4757',
    };
    return colors[status] || colors.idle;
  }

  function getStatusText(status) {
    const texts = {
      idle: 'Ready',
      thinking: 'Thinking...',
      responding: 'Streaming...',
      complete: 'Complete',
      error: 'Error',
    };
    return texts[status] || texts.idle;
  }

  // ============================================================================
  // DOM CREATION
  // ============================================================================

  function createPanel() {
    const panel = document.createElement('div');
    panel.id = 'response-stream-panel';
    panel.className = `rsp-panel rsp-${state.config.position} rsp-expanded`;
    panel.style.setProperty('--panel-width', typeof state.config.width === 'number' ? `${state.config.width}px` : state.config.width);
    panel.style.zIndex = state.config.zIndex;

    panel.innerHTML = `
      <!-- Header -->
      <div class="rsp-header" data-action="toggle">
        <div class="rsp-header-left">
          <div class="rsp-status-indicator rsp-${state.status}">
            <div class="rsp-status-dot"></div>
          </div>
          <span class="rsp-title">Agent Response</span>
          <button class="rsp-history-btn" data-action="history" style="display: none;">
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
              <path d="M7 13C10.3137 13 13 10.3137 13 7C13 3.68629 10.3137 1 7 1C3.68629 1 1 3.68629 1 7" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
              <path d="M12 12L9 9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            <span class="rsp-history-count">0</span>
          </button>
        </div>
        <div class="rsp-header-right">
          <button class="rsp-close-btn" data-action="close" aria-label="Close panel">
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
              <path d="M2 2L12 12M12 2L2 12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
          </button>
        </div>
      </div>

      <!-- Content -->
      <div class="rsp-content">
        <!-- Streaming Indicator -->
        <div class="rsp-streaming-indicator" style="display: none;">
          <div class="rsp-typing-indicator rsp-${state.config.typingAnimationStyle}">
            ${createTypingIndicatorHTML(state.config.typingAnimationStyle)}
            <span class="rsp-status-text">${getStatusText(state.status)}</span>
          </div>
        </div>

        <!-- Current Content -->
        <div class="rsp-content-wrapper">
          <div class="rsp-stream-content">
            ${createContentHTML('')}
          </div>
        </div>

        <!-- Controls -->
        <div class="rsp-controls" style="display: none;">
          <button class="rsp-control-btn rsp-stop" data-action="stop">
            <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
              <rect x="2" y="2" width="8" height="8" rx="1"/>
            </svg>
            Stop
          </button>
          <span class="rsp-content-length">0/${state.config.maxContentLength}</span>
        </div>

        <!-- Idle State -->
        <div class="rsp-idle-state">
          <div class="rsp-idle-icon">
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none">
              <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="1.5"/>
              <path d="M8 12L11 15L16 9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </div>
          <p class="rsp-idle-text">Waiting for agent response...</p>
        </div>

        <!-- History Panel -->
        <div class="rsp-history-panel" style="display: none;">
          <div class="rsp-history-header">
            <span class="rsp-history-title">Previous Responses</span>
          </div>
          <div class="rsp-history-list"></div>
        </div>
      </div>

      <!-- Collapse Handle -->
      <button class="rsp-collapse-handle rsp-top" data-action="toggle" aria-label="Collapse panel">
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
          <path d="M3 8L6 5L9 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
      </button>
    `;

    return panel;
  }

  function createTypingIndicatorHTML(style) {
    switch (style) {
      case 'waveform':
        return `
          <div class="rsp-waveform-bar" style="animation-delay: 0ms; height: 40%;"></div>
          <div class="rsp-waveform-bar" style="animation-delay: 120ms; height: 70%;"></div>
          <div class="rsp-waveform-bar" style="animation-delay: 240ms; height: 100%;"></div>
          <div class="rsp-waveform-bar" style="animation-delay: 160ms; height: 60%;"></div>
          <div class="rsp-waveform-bar" style="animation-delay: 80ms; height: 85%;"></div>
        `;
      case 'pulse':
        return `
          <div class="rsp-pulse-ring"></div>
          <div class="rsp-pulse-core"></div>
        `;
      default: // dots
        return `
          <span class="rsp-dot" style="animation-delay: 0ms;"></span>
          <span class="rsp-dot" style="animation-delay: 200ms;"></span>
          <span class="rsp-dot" style="animation-delay: 400ms;"></span>
        `;
    }
  }

  function createContentHTML(content) {
    const lines = content.split('\n');
    const lineNumbers = state.config.showLineNumbers
      ? `<div class="rsp-line-numbers">${lines.map((_, i) => `<span class="rsp-line-number">${i + 1}</span>`).join('')}</div>`
      : '';

    const textContent = lines.map((line, i) => `
      <div class="rsp-content-line">${escapeHtml(line) || '<br>'}</div>
    `).join('');

    return `${lineNumbers}<div class="rsp-content-text">${textContent}</div>`;
  }

  // ============================================================================
  // DOM UPDATES
  // ============================================================================

  function updateStatusIndicator(status) {
    const panel = document.getElementById('response-stream-panel');
    if (!panel) return;

    const indicator = panel.querySelector('.rsp-status-indicator');
    const statusText = panel.querySelector('.rsp-status-text');

    indicator.className = `rsp-status-indicator rsp-${status}`;
    if (statusText) {
      statusText.textContent = getStatusText(status);
    }
  }

  function updateStreamingIndicator(show) {
    const indicator = document.querySelector('.rsp-streaming-indicator');
    if (indicator) {
      indicator.style.display = show ? 'block' : 'none';
    }
  }

  function updateControls(show) {
    const controls = document.querySelector('.rsp-controls');
    if (controls) {
      controls.style.display = show ? 'flex' : 'none';
    }
  }

  function updateIdleState(show) {
    const idleState = document.querySelector('.rsp-idle-state');
    if (idleState) {
      idleState.style.display = show ? 'flex' : 'none';
    }
  }

  function updateContent(content, isStreaming = false) {
    const streamContent = document.querySelector('.rsp-stream-content');
    if (streamContent) {
      streamContent.innerHTML = createContentHTML(content);

      // Add cursor blink for streaming
      if (isStreaming && content) {
        const lastLine = streamContent.querySelector('.rsp-content-line:last-child');
        if (lastLine) {
          lastLine.innerHTML += '<span class="rsp-cursor-blink">_</span>';
        }
      }
    }

    // Update content length
    const lengthDisplay = document.querySelector('.rsp-content-length');
    if (lengthDisplay) {
      lengthDisplay.textContent = `${content.length}/${state.config.maxContentLength}`;
    }
  }

  function updateHistoryButton() {
    const btn = document.querySelector('.rsp-history-btn');
    const count = document.querySelector('.rsp-history-count');
    if (btn && count) {
      btn.style.display = state.messages.length > 0 ? 'flex' : 'none';
      count.textContent = state.messages.length;
    }
  }

  function renderHistory() {
    const historyList = document.querySelector('.rsp-history-list');
    if (!historyList) return;

    if (state.messages.length === 0) {
      historyList.innerHTML = '<p class="rsp-empty-message">No previous responses</p>';
      return;
    }

    historyList.innerHTML = state.messages.map((msg, i) => {
      const preview = msg.slice(0, 150).replace(/[#*`]/g, '');
      return `
        <button class="rsp-history-item" data-index="${i}">
          <div class="rsp-history-meta">
            <span class="rsp-history-time">${formatTimestamp(msg.timestamp)}</span>
            <span class="rsp-history-type">complete</span>
          </div>
          <div class="rsp-history-preview">${escapeHtml(preview)}${msg.content.length > 150 ? '...' : ''}</div>
        </button>
      `;
    }).join('');
  }

  function showHistoryPanel(show) {
    const historyPanel = document.querySelector('.rsp-history-panel');
    const contentWrapper = document.querySelector('.rsp-content-wrapper');
    const streamingIndicator = document.querySelector('.rsp-streaming-indicator');
    const controls = document.querySelector('.rsp-controls');
    const idleState = document.querySelector('.rsp-idle-state');

    if (historyPanel) {
      historyPanel.style.display = show ? 'block' : 'none';
    }
    if (contentWrapper) {
      contentWrapper.style.display = show ? 'none' : 'block';
    }
    if (streamingIndicator) {
      streamingIndicator.style.display = show ? 'none' : (state.isStreaming ? 'block' : 'none');
    }
    if (controls) {
      controls.style.display = show ? 'none' : (state.isStreaming ? 'flex' : 'none');
    }
    if (idleState) {
      idleState.style.display = show ? 'none' : (!state.isStreaming && !state.currentContent ? 'flex' : 'none');
    }
  }

  // ============================================================================
  // EVENT HANDLING
  // ============================================================================

  function handlePanelClick(event) {
    const action = event.target.closest('[data-action]')?.dataset.action;
    if (!action) return;

    switch (action) {
      case 'toggle':
        toggleExpanded();
        break;
      case 'close':
        hide();
        break;
      case 'history':
        toggleHistory();
        break;
      case 'stop':
        clearStream();
        break;
    }
  }

  function handleHistoryItemClick(event) {
    const item = event.target.closest('.rsp-history-item');
    if (!item) return;

    const index = parseInt(item.dataset.index, 10);
    const message = state.messages[index];
    if (message) {
      state.currentContent = message.content;
      updateContent(message.content, false);
      showHistoryPanel(false);
    }
  }

  // ============================================================================
  // PUBLIC API
  // ============================================================================

  function init(options = {}) {
    // Merge config
    state.config = { ...DEFAULT_CONFIG, ...options };

    // Create and append panel
    const panel = createPanel();
    document.body.appendChild(panel);

    // Add event listeners
    panel.addEventListener('click', handlePanelClick);
    document.addEventListener('click', handleHistoryItemClick);

    // Add keyboard shortcuts
    document.addEventListener('keydown', handleKeyDown);

    // Add styles
    addStyles();

    // Subscribe to stream events
    subscribeToStreamEvents();

    return panel;
  }

  function destroy() {
    const panel = document.getElementById('response-stream-panel');
    if (panel) {
      panel.remove();
    }
    document.removeEventListener('click', handleHistoryItemClick);
    document.removeEventListener('keydown', handleKeyDown);
  }

  function show() {
    state.isVisible = true;
    const panel = document.getElementById('response-stream-panel');
    if (panel) {
      panel.style.display = 'block';
    }
  }

  function hide() {
    state.isVisible = false;
    const panel = document.getElementById('response-stream-panel');
    if (panel) {
      panel.style.display = 'none';
    }
  }

  function toggleVisibility() {
    if (state.isVisible) {
      hide();
    } else {
      show();
    }
  }

  function expand() {
    state.isExpanded = true;
    const panel = document.getElementById('response-stream-panel');
    if (panel) {
      panel.classList.add('rsp-expanded');
      panel.classList.remove('rsp-collapsed');
    }
  }

  function collapse() {
    state.isExpanded = false;
    const panel = document.getElementById('response-stream-panel');
    if (panel) {
      panel.classList.remove('rsp-expanded');
      panel.classList.add('rsp-collapsed');
    }
  }

  function toggleExpanded() {
    if (state.isExpanded) {
      collapse();
    } else {
      expand();
    }
  }

  function toggleHistory() {
    const panel = document.querySelector('.rsp-history-panel');
    if (!panel) return;

    const isVisible = panel.style.display !== 'none';
    showHistoryPanel(!isVisible);
    renderHistory();
  }

  function startStream(sessionId) {
    state.isStreaming = true;
    state.status = 'thinking';
    state.currentContent = '';
    state.error = null;
    contentBuffer.length = 0;

    updateStatusIndicator('thinking');
    updateStreamingIndicator(true);
    updateIdleState(false);
    updateControls(true);
    updateContent('');

    notifyListeners({ type: 'start', sessionId });
  }

  function addChunk(chunk, isComplete = false) {
    if (!state.isStreaming) return;

    contentBuffer.push(chunk);
    state.currentContent = contentBuffer.join('').slice(-state.config.maxContentLength);
    state.status = isComplete ? 'complete' : 'responding';

    updateStatusIndicator(state.status);
    updateContent(state.currentContent, !isComplete);

    if (isComplete) {
      endStream();
    }

    notifyListeners({ type: 'chunk', chunk, complete: isComplete });
  }

  function updateContentStream(content, isComplete = false) {
    if (!state.isStreaming) return;

    state.currentContent = content.slice(-state.config.maxContentLength);
    state.status = isComplete ? 'complete' : 'responding';

    updateStatusIndicator(state.status);
    updateContent(state.currentContent, !isComplete);

    if (isComplete) {
      endStream();
    }

    notifyListeners({ type: 'content', content, complete: isComplete });
  }

  function endStream() {
    if (!state.isStreaming) return;

    // Save to history
    if (state.currentContent) {
      state.messages.push({
        content: state.currentContent,
        timestamp: Date.now(),
        type: 'complete',
      });

      // Trim old messages
      if (state.messages.length > state.config.maxHistoryItems) {
        state.messages = state.messages.slice(-state.config.maxHistoryItems);
      }
    }

    state.isStreaming = false;
    state.status = 'idle';

    updateStatusIndicator('idle');
    updateStreamingIndicator(false);
    updateControls(false);
    updateHistoryButton();
    updateIdleState(true);

    notifyListeners({ type: 'end' });
  }

  function addSystemMessage(message) {
    notifyListeners({ type: 'system', content: message });
  }

  function reportError(error) {
    state.isStreaming = false;
    state.status = 'error';
    state.error = error;

    updateStatusIndicator('error');
    updateStreamingIndicator(false);
    updateControls(false);
    updateIdleState(true);

    notifyListeners({ type: 'error', content: error });
  }

  function clearStream() {
    state.isStreaming = false;
    state.status = 'idle';
    state.currentContent = '';
    state.error = null;
    contentBuffer.length = 0;

    updateStatusIndicator('idle');
    updateStreamingIndicator(false);
    updateControls(false);
    updateContent('', false);
    updateIdleState(true);

    notifyListeners({ type: 'clear' });
  }

  function getState() {
    return {
      isExpanded: state.isExpanded,
      isVisible: state.isVisible,
      isStreaming: state.isStreaming,
      currentContent: state.currentContent,
      messages: [...state.messages],
      status: state.status,
      error: state.error,
    };
  }

  function subscribe(callback) {
    listeners.add(callback);
    return () => listeners.delete(callback);
  }

  function notifyListeners(event) {
    listeners.forEach(callback => callback(event));
  }

  function handleKeyDown(event) {
    if (event.key === 'Escape') {
      const historyPanel = document.querySelector('.rsp-history-panel');
      if (historyPanel && historyPanel.style.display !== 'none') {
        showHistoryPanel(false);
        return;
      }
      if (state.isExpanded) {
        hide();
      }
    }
    if (event.key === 'ArrowDown' && event.altKey && !state.isExpanded) {
      expand();
    }
    if (event.key === 'ArrowUp' && event.AltKey && state.isExpanded) {
      collapse();
    }
  }

  function subscribeToStreamEvents() {
    if (typeof window === 'undefined') return;

    const handleEvent = (event) => {
      const { type, content, chunk, complete, sessionId } = event.detail || {};

      switch (type) {
        case 'start':
          startStream(sessionId);
          break;
        case 'content':
          updateContentStream(content, complete);
          break;
        case 'chunk':
          addChunk(chunk, complete);
          break;
        case 'end':
          endStream();
          break;
        case 'system':
          addSystemMessage(content);
          break;
        case 'error':
          reportError(content);
          break;
      }
    };

    window.addEventListener('__devtool:agent:message', handleEvent);

    // Poll for __devtool.stream state
    const interval = setInterval(() => {
      if (window.__devtool?.stream?.getState) {
        const streamState = window.__devtool.stream.getState();
        if (streamState.isActive && !state.isStreaming) {
          startStream(streamState.sessionId);
        }
        if (streamState.content !== state.currentContent) {
          updateContentStream(streamState.content, streamState.status === 'complete');
        }
      }
    }, 100);
  }

  // ============================================================================
  // STYLES
  // ============================================================================

  function addStyles() {
    if (document.getElementById('rsp-styles')) return;

    const css = `
      /* Response Stream Panel Styles */
      #response-stream-panel {
        --rsp-bg: #0a0a0f;
        --rsp-bg-secondary: #12121a;
        --rsp-border: #2a2a35;
        --rsp-text-primary: #e8e8ec;
        --rsp-text-secondary: #9898a8;
        --rsp-text-muted: #686870;
        --rsp-accent: #00d4aa;
        --rsp-accent-secondary: #ff6b35;
        --rsp-error: #ff4757;
        --rsp-font-mono: 'JetBrains Mono', 'Fira Code', monospace;
        --rsp-font-display: 'Space Grotesk', system-ui, sans-serif;

        position: fixed;
        z-index: 99998;
        background: var(--rsp-bg);
        border: 1px solid var(--rsp-border);
        border-radius: 6px;
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5), inset 0 0 0 1px rgba(255, 255, 255, 0.02);
        font-family: var(--rsp-font-mono);
        font-size: 13px;
        color: var(--rsp-text-primary);
        overflow: hidden;
        transition: width 250ms, height 250ms, transform 250ms, opacity 250ms;
        animation: rsp-fade-in 400ms ease-out;
      }

      @keyframes rsp-fade-in {
        from { opacity: 0; transform: translateY(10px); }
        to { opacity: 1; transform: translateY(0); }
      }

      .rsp-bottom-right { bottom: 16px; right: 16px; }
      .rsp-bottom-left { bottom: 16px; left: 16px; }
      .rsp-top-right { top: 16px; right: 16px; }
      .rsp-top-left { top: 16px; left: 16px; }

      .rsp-expanded { width: var(--panel-width); height: 320px; }
      .rsp-collapsed { width: auto; min-width: 180px; height: 44px; }
      .rsp-collapsed .rsp-content { display: none; }
      .rsp-collapsed .rsp-collapse-handle svg { transform: rotate(180deg); }

      /* Header */
      .rsp-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        background: var(--rsp-bg-secondary);
        border-bottom: 1px solid var(--rsp-border);
        height: 44px;
        cursor: pointer;
        user-select: none;
      }

      .rsp-header:hover { background: linear-gradient(180deg, var(--rsp-bg-secondary) 0%, rgba(0, 212, 170, 0.03) 100%); }

      .rsp-header-left, .rsp-header-right { display: flex; align-items: center; gap: 8px; }

      .rsp-status-indicator { position: relative; display: flex; align-items: center; justify-content: center; width: 18px; height: 18px; }
      .rsp-status-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--rsp-text-muted); transition: background 150ms; }
      .rsp-status-indicator.rsp-thinking .rsp-status-dot { background: #f59e0b; animation: rsp-pulse-glow 1.5s ease-in-out infinite; }
      .rsp-status-indicator.rsp-responding .rsp-status-dot { background: var(--rsp-accent); animation: rsp-breathe 2s ease-in-out infinite; }
      .rsp-status-indicator.rsp-error .rsp-status-dot { background: var(--rsp-error); }

      @keyframes rsp-pulse-glow {
        0%, 100% { box-shadow: 0 0 4px #f59e0b; }
        50% { box-shadow: 0 0 12px #f59e0b, 0 0 20px #f59e0b; }
      }

      @keyframes rsp-breathe {
        0%, 100% { box-shadow: 0 0 4px var(--rsp-accent); }
        50% { box-shadow: 0 0 8px var(--rsp-accent); }
      }

      .rsp-title {
        font-family: var(--rsp-font-display);
        font-size: 12px;
        font-weight: 600;
        letter-spacing: 0.5px;
        text-transform: uppercase;
        color: var(--rsp-text-secondary);
      }

      .rsp-history-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        background: transparent;
        border: 1px solid var(--rsp-border);
        border-radius: 2px;
        color: var(--rsp-text-muted);
        font-family: var(--rsp-font-mono);
        font-size: 11px;
        cursor: pointer;
        transition: all 150ms;
      }

      .rsp-history-btn:hover {
        border-color: #7c3aed;
        color: #7c3aed;
        background: rgba(124, 58, 237, 0.1);
      }

      .rsp-close-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        background: transparent;
        border: none;
        border-radius: 2px;
        color: var(--rsp-text-muted);
        cursor: pointer;
        transition: all 150ms;
      }

      .rsp-close-btn:hover { background: rgba(255, 71, 87, 0.1); color: var(--rsp-error); }

      /* Content */
      .rsp-content { height: calc(100% - 44px); overflow: hidden; display: flex; flex-direction: column; }

      .rsp-streaming-indicator {
        padding: 12px 16px;
        border-bottom: 1px solid var(--rsp-border);
        background: rgba(0, 212, 170, 0.03);
      }

      /* Typing Indicator - Waveform */
      .rsp-typing-indicator.waveform {
        display: flex;
        align-items: center;
        gap: 3px;
        height: 24px;
      }

      .rsp-waveform-bar {
        width: 3px;
        height: 100%;
        background: linear-gradient(180deg, var(--rsp-accent) 0%, var(--rsp-accent-secondary) 100%);
        border-radius: 2px;
        animation: rsp-waveform 0.8s ease-in-out infinite;
      }

      @keyframes rsp-waveform {
        0%, 100% { transform: scaleY(0.3); opacity: 0.5; }
        50% { transform: scaleY(1); opacity: 1; }
      }

      .rsp-typing-indicator .rsp-status-text {
        font-family: var(--rsp-font-mono);
        font-size: 11px;
        color: var(--rsp-text-muted);
        margin-left: 8px;
      }

      /* Content Wrapper */
      .rsp-content-wrapper { flex: 1; overflow: hidden; display: flex; flex-direction: column; }

      .rsp-stream-content {
        flex: 1;
        overflow: auto;
        padding: 12px;
        scroll-behavior: smooth;
        white-space: pre-wrap;
        word-break: break-word;
        font-size: 13px;
        line-height: 1.6;
      }

      .rsp-line-numbers {
        display: inline-flex;
        flex-direction: column;
        margin-right: 12px;
        padding-right: 12px;
        border-right: 1px solid var(--rsp-border);
        color: var(--rsp-text-muted);
        font-size: 11px;
        user-select: none;
      }

      .rsp-line-number { height: 20px; line-height: 20px; text-align: right; }

      .rsp-cursor-blink {
        display: inline-block;
        margin-left: 2px;
        color: var(--rsp-accent);
        animation: rsp-cursor-blink 1s step-end infinite;
      }

      @keyframes rsp-cursor-blink {
        0%, 50% { opacity: 1; }
        51%, 100% { opacity: 0; }
      }

      /* Controls */
      .rsp-controls {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        background: var(--rsp-bg-secondary);
        border-top: 1px solid var(--rsp-border);
      }

      .rsp-control-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        background: transparent;
        border: 1px solid var(--rsp-border);
        border-radius: 2px;
        color: var(--rsp-text-secondary);
        font-family: var(--rsp-font-mono);
        font-size: 11px;
        cursor: pointer;
        transition: all 150ms;
      }

      .rsp-control-btn:hover { border-color: var(--rsp-error); color: var(--rsp-error); background: rgba(255, 71, 87, 0.1); }

      .rsp-content-length { font-size: 10px; color: var(--rsp-text-muted); font-family: var(--rsp-font-mono); }

      /* Idle State */
      .rsp-idle-state {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 20px;
        color: var(--rsp-text-muted);
      }

      .rsp-idle-icon { opacity: 0.3; }
      .rsp-idle-text { font-family: var(--rsp-font-mono); font-size: 12px; margin: 0; }

      /* History Panel */
      .rsp-history-panel { flex: 1; overflow: auto; padding: 12px; }
      .rsp-history-header { margin-bottom: 12px; padding-bottom: 8px; border-bottom: 1px solid var(--rsp-border); }
      .rsp-history-title { font-family: var(--rsp-font-display); font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: var(--rsp-text-muted); }
      .rsp-history-list { display: flex; flex-direction: column; gap: 8px; }
      .rsp-history-item { display: flex; flex-direction: column; gap: 4px; padding: 8px; background: var(--rsp-bg-secondary); border: 1px solid var(--rsp-border); border-radius: 4px; cursor: pointer; text-align: left; transition: all 150ms; }
      .rsp-history-item:hover { border-color: var(--rsp-accent); background: rgba(0, 212, 170, 0.05); }
      .rsp-history-meta { display: flex; align-items: center; gap: 8px; }
      .rsp-history-time { font-family: var(--rsp-font-mono); font-size: 10px; color: var(--rsp-text-muted); }
      .rsp-history-type { font-family: var(--rsp-font-display); font-size: 9px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; padding: 2px 6px; border-radius: 2px; background: var(--rsp-border); color: var(--rsp-text-secondary); }
      .rsp-history-preview { font-family: var(--rsp-font-mono); font-size: 11px; color: var(--rsp-text-secondary); line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
      .rsp-empty-message { font-family: var(--rsp-font-mono); font-size: 12px; color: var(--rsp-text-muted); margin: 0; text-align: center; }

      /* Collapse Handle */
      .rsp-collapse-handle {
        position: absolute;
        right: 16px;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        background: var(--rsp-bg);
        border: 1px solid var(--rsp-border);
        border-radius: 2px;
        color: var(--rsp-text-muted);
        cursor: pointer;
        transition: all 150ms;
        z-index: 10;
      }

      .rsp-collapse-handle.rsp-top { top: 22px; }
      .rsp-collapse-handle:hover { border-color: var(--rsp-accent); color: var(--rsp-accent); background: rgba(0, 212, 170, 0.1); }
      .rsp-collapse-handle svg { transition: transform 150ms; }

      /* Scrollbar */
      .rsp-stream-content::-webkit-scrollbar, .rsp-history-panel::-webkit-scrollbar { width: 8px; height: 8px; }
      .rsp-stream-content::-webkit-scrollbar-track, .rsp-history-panel::-webkit-scrollbar-track { background: transparent; }
      .rsp-stream-content::-webkit-scrollbar-thumb, .rsp-history-panel::-webkit-scrollbar-thumb { background: var(--rsp-border); border-radius: 4px; }
      .rsp-stream-content::-webkit-scrollbar-thumb:hover, .rsp-history-panel::-webkit-scrollbar-thumb:hover { background: #3a3a45; }

      /* Reduced Motion */
      @media (prefers-reduced-motion: reduce) {
        *, *::before, *::after { animation-duration: 0.01ms !important; animation-iteration-count: 1 !important; transition-duration: 0.01ms !important; }
      }
    `;

    const style = document.createElement('style');
    style.id = 'rsp-styles';
    style.textContent = css;
    document.head.appendChild(style);
  }

  // ============================================================================
  // INITIALIZATION
  // ============================================================================

  // Auto-initialize if __devtool is present
  if (typeof window !== 'undefined') {
    // Expose global API
    window.__devtool = window.__devtool || {};
    window.__devtool.streamPanel = {
      init,
      destroy,
      show,
      hide,
      toggleVisibility,
      expand,
      collapse,
      toggleExpanded,
      startStream: startStream,
      addChunk,
      updateContent: updateContentStream,
      endStream,
      addSystemMessage,
      reportError,
      clear: clearStream,
      getState,
      subscribe,
    };
  }

  // Export for module systems
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      init,
      destroy,
      show,
      hide,
      toggleVisibility,
      expand,
      collapse,
      toggleExpanded,
      startStream,
      addChunk,
      updateContentStream,
      endStream,
      addSystemMessage,
      reportError,
      clearStream,
      getState,
      subscribe,
    };
  }

})();
