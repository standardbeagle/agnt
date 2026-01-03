/**
 * Response Stream Panel - Usage Examples
 *
 * Examples demonstrating how to use the Response Stream Panel component
 * in various scenarios.
 */

// ============================================================================
// EXAMPLE 1: Basic Usage with Default Settings
// ============================================================================

import React from 'react';
import { ResponseStreamPanel } from './ResponseStreamPanel';

function BasicExample() {
  return (
    <ResponseStreamPanel
      position="bottom-right"
      defaultExpanded={true}
    />
  );
}

// ============================================================================
// EXAMPLE 2: Custom Configuration
// ============================================================================

function CustomConfigExample() {
  return (
    <ResponseStreamPanel
      position="bottom-left"
      width={500}
      height={400}
      showLineNumbers={true}
      typewriterEffect={true}
      typingAnimationStyle="waveform"
      animationSpeed={50}
      onStreamStart={() => console.log('Stream started')}
      onStreamEnd={() => console.log('Stream ended')}
    />
  );
}

// ============================================================================
// EXAMPLE 3: Controlled Component
// ============================================================================

import { useState } from 'react';

function ControlledExample() {
  const [isExpanded, setIsExpanded] = useState(true);
  const [isVisible, setIsVisible] = useState(true);
  const [agentStatus, setAgentStatus] = useState<'idle' | 'thinking' | 'responding' | 'complete' | 'error'>('idle');

  return (
    <>
      <div className="controls">
        <button onClick={() => setIsVisible(!isVisible)}>
          {isVisible ? 'Hide' : 'Show'} Panel
        </button>
        <button onClick={() => setIsExpanded(!isExpanded)}>
          {isExpanded ? 'Collapse' : 'Expand'} Panel
        </button>
        <select
          value={agentStatus}
          onChange={(e) => setAgentStatus(e.target.value as any)}
        >
          <option value="idle">Idle</option>
          <option value="thinking">Thinking</option>
          <option value="responding">Responding</option>
          <option value="complete">Complete</option>
          <option value="error">Error</option>
        </select>
      </div>

      <ResponseStreamPanel
        visible={isVisible}
        defaultExpanded={isExpanded}
        status={agentStatus}
        onOpen={() => console.log('Panel opened')}
        onClose={() => console.log('Panel closed')}
      />
    </>
  );
}

// ============================================================================
// EXAMPLE 4: Using the Hook
// ============================================================================

import { useAgentStream } from './useAgentStream';

function HookExample() {
  const {
    isStreaming,
    content,
    messages,
    status,
    start,
    addContent,
    addChunk,
    end,
    clear,
  } = useAgentStream({
    onStart: () => console.log('Agent started thinking'),
    onContent: (content) => console.log(`Received ${content.length} chars`),
    onEnd: () => console.log('Agent finished'),
  });

  const simulateStreaming = async () => {
    start('session-123');

    const response = "Here's a simulated streaming response from the agent...";
    for (const char of response) {
      await new Promise(r => setTimeout(r, 50));
      addChunk(char, false);
    }

    await new Promise(r => setTimeout(r, 500));
    end();
  };

  return (
    <div>
      <button onClick={simulateStreaming}>Simulate Agent Response</button>
      <button onClick={clear}>Clear</button>
      <p>Status: {status}</p>
      <p>Content length: {content.length}</p>
      <p>Messages: {messages.length}</p>
    </div>
  );
}

// ============================================================================
// EXAMPLE 5: useStreamPanel Hook (Full Integration)
// ============================================================================

import { useStreamPanel } from './useAgentStream';

function FullPanelExample() {
  const {
    isStreaming,
    content,
    messages,
    status,
    error,
    isExpanded,
    isVisible,
    start,
    addChunk,
    end,
    clear,
    toggleExpanded,
    hide,
    show,
  } = useStreamPanel({
    position: 'bottom-right',
    defaultExpanded: true,
    showLineNumbers: false,
    typewriterEffect: true,
    animationStyle: 'waveform',
    onStart: () => console.log('Stream started'),
    onEnd: () => console.log('Stream ended'),
    onContent: (c) => console.log('Content updated:', c.length),
  });

  const simulateComplexStream = async () => {
    start('complex-session');

    const parts = [
      "Analyzing your request...",
      "\n\nI've found the following:\n\n",
      "```typescript\ninterface User {\n  id: string;\n  name: string;\n  email: string;\n}\n```\n\n",
      "This interface defines the user structure. The `id` field is a unique identifier, ",
      "the `name` field stores the user's display name, ",
      "and the `email` field contains their contact information.",
      "\n\nWould you like me to add any additional fields?"
    ];

    for (const part of parts) {
      await new Promise(r => setTimeout(r, 100));
      addChunk(part, part === parts[parts.length - 1]);
    }
  };

  return (
    <div className="example-container">
      <div className="control-bar">
        <button onClick={simulateComplexStream}>Start Complex Stream</button>
        <button onClick={end}>End Stream</button>
        <button onClick={clear}>Clear</button>
        <button onClick={toggleExpanded}>
          {isExpanded ? 'Collapse' : 'Expand'} Panel
        </button>
        <button onClick={isVisible ? hide : show}>
          {isVisible ? 'Hide' : 'Show'} Panel
        </button>
      </div>

      <ResponseStreamPanel
        visible={isVisible}
        defaultExpanded={isExpanded}
      />

      <div className="status-display">
        <p>Status: <strong>{status}</strong></p>
        <p>Streaming: {isStreaming ? 'Yes' : 'No'}</p>
        <p>Messages: {messages.length}</p>
        {error && <p className="error">Error: {error}</p>}
      </div>
    </div>
  );
}

// ============================================================================
// EXAMPLE 6: Integration with __devtool API
// ============================================================================

/**
 * In your proxy's JavaScript injection, you can emit events
 * to control the stream panel from the backend:
 */

// Example: From backend (Go)
function backendExample() {
  // This would be called when the agent sends a message
  // dispatchAgentEvent is available in the integration module
  // dispatchStreamStart(sessionID);
  // dispatchStreamChunk("部分内容");
  // dispatchStreamEnd();
}

// Example: From frontend JavaScript injection
function frontendExample() {
  // Listen for stream events
  window.addEventListener('__devtool:agent:message', (event) => {
    const { type, content, chunk } = event.detail;

    if (type === 'start') {
      console.log('Agent started responding');
    } else if (type === 'chunk') {
      console.log('Received chunk:', chunk);
    } else if (type === 'end') {
      console.log('Agent finished responding');
    }
  });

  // Or use the __devtool.stream API directly
  if (window.__devtool?.stream) {
    window.__devtool.stream.start('my-session');
    window.__devtool.stream.addChunk('Hello');
    window.__devtool.stream.addChunk(', world!');
    window.__devtool.stream.end();
  }
}

// ============================================================================
// EXAMPLE 7: Using Standalone JS Version
// ============================================================================

/**
 * For non-React environments, use the standalone JS version:
 */

// In your proxy's JavaScript injection file:
function initStandalonePanel() {
  if (typeof window.__devtool?.streamPanel !== 'undefined') {
    // Panel already initialized
    return window.__devtool.streamPanel;
  }

  // Initialize with custom options
  return window.__devtool.streamPanel.init({
    position: 'bottom-right',
    width: 400,
    maxContentLength: 8000,
    showLineNumbers: false,
    typewriterEffect: true,
    typingAnimationStyle: 'waveform',
  });
}

// Later, when agent responds:
// window.__devtool.streamPanel.startStream('session-123');
// window.__devtool.streamPanel.addChunk('Response text...');
// window.__devtool.streamPanel.end();

// ============================================================================
// EXAMPLE 8: Custom Styling via CSS Variables
// ============================================================================

/**
 * Override the default appearance by setting CSS variables:
 */

const customStyles = `
  .response-stream-panel {
    --panel-bg: #1a1a2e;
    --panel-bg-secondary: #16213e;
    --panel-border: #0f3460;
    --text-primary: #eaeaea;
    --accent-primary: #e94560;
    --accent-secondary: #00d4aa;
    --font-mono: 'Fira Code', monospace;
    --font-display: 'Inter', system-ui, sans-serif;
  }
`;

// ============================================================================
// EXPORTS
// ============================================================================

export {
  BasicExample,
  CustomConfigExample,
  ControlledExample,
  HookExample,
  FullPanelExample,
  initStandalonePanel,
};
