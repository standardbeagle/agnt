# Proxy UI Components

This directory contains the UI components for the agnt browser overlay, including the Response Stream Panel.

## Components

### ResponseStreamPanel

A distinctive floating panel that displays real-time streaming responses from the coding agent.

**Files:**
- `ResponseStreamPanel.tsx` - React component
- `ResponseStreamPanel.css` - Component styles
- `ResponseStreamPanel.js` - Standalone JS version (no React)
- `useAgentStream.ts` - React hooks for streaming
- `agentStreamIntegration.ts` - __devtool API integration
- `ResponseStreamPanel.examples.tsx` - Usage examples

## Features

- **Real-time streaming** - Displays agent responses as they arrive
- **Typing indicators** - Animated status indicators (waveform, pulse, dots)
- **Typewriter effect** - Optional character-by-character animation
- **Expandable/collapsible** - Compact header when collapsed
- **Session history** - View previous responses
- **Keyboard shortcuts** - ESC to close, Alt+Arrow to expand/collapse
- **Accessible** - ARIA labels and keyboard navigation

## Usage

### React Component

```tsx
import { ResponseStreamPanel } from './internal/proxy/ui';

<ResponseStreamPanel
  position="bottom-right"
  defaultExpanded={true}
  typingAnimationStyle="waveform"
  onStreamStart={() => console.log('Started')}
  onStreamEnd={() => console.log('Finished')}
/>
```

### Hook Usage

```tsx
import { useAgentStream, useStreamPanel } from './internal/proxy/ui';

const stream = useAgentStream({
  onStart: () => console.log('Agent started'),
  onContent: (content) => console.log('New content:', content),
});

stream.start('session-123');
stream.addChunk('Hello, ');
stream.addChunk('world!');
stream.end();
```

### Standalone JS

```javascript
// Initialize
const panel = window.__devtool.streamPanel.init({
  position: 'bottom-right',
  width: 400,
});

// Stream events
window.__devtool.streamPanel.startStream('session-123');
window.__devtool.streamPanel.addChunk('Response text...');
window.__devtool.streamPanel.end();
```

## Integration with __devtool

The panel integrates with the existing `__devtool` API:

```javascript
// Emit events from backend
dispatchAgentEvent({ type: 'start', sessionId: 'abc' });
dispatchAgentEvent({ type: 'content', content: 'Response...', complete: false });
dispatchAgentEvent({ type: 'chunk', chunk: 'Part of response', complete: false });
dispatchAgentEvent({ type: 'end' });
```

## Styling

The component uses CSS custom properties for theming. Override these in your CSS:

```css
.response-stream-panel {
  --panel-bg: #0a0a0f;
  --panel-border: #2a2a35;
  --text-primary: #e8e8ec;
  --accent-primary: #ff6b35;
  --accent-secondary: #00d4aa;
  --font-mono: 'JetBrains Mono', monospace;
  --font-display: 'Space Grotesk', sans-serif;
}
```

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `ESC` | Close panel (when expanded) / Close history |
| `Alt+↓` | Expand panel |
| `Alt+↑` | Collapse panel |

## Position Options

- `bottom-right` - Bottom right corner
- `bottom-left` - Bottom left corner
- `top-right` - Top right corner
- `top-left` - Top left corner

## Animation Styles

- `waveform` - Audio waveform bars (default)
- `pulse` - Expanding ring animation
- `dots` - Bouncing dots animation
