# Plan: Interaction Logging and Injector Refactoring

## Overview

Add interaction logging to track user mouse/keyboard activity and DOM mutations, enabling AI agents to understand "when I clicked nothing happened" scenarios. Also refactor the 5845-line `injector.go` into a maintainable structure.

---

## Part 1: Injector File Reorganization

### Current State
- `internal/proxy/injector.go`: 5845 lines, single Go file with embedded JavaScript
- Contains: WebSocket logic, utilities, 50+ diagnostic primitives, all in one string literal

### Target Structure

```
internal/proxy/
├── injector.go              # Main injection logic (ShouldInject, InjectInstrumentation)
├── injector_test.go         # Existing tests
├── scripts/
│   ├── embed.go             # //go:embed directive for all .js files
│   ├── core.js              # WebSocket connection, send(), error/perf tracking
│   ├── utils.js             # resolveElement, generateSelector, safeGetComputed, etc.
│   ├── overlay.js           # Overlay management system
│   ├── inspection.js        # Element inspection primitives (getElementInfo, getPosition, etc.)
│   ├── tree.js              # Tree walking (walkChildren, walkParents, findAncestor)
│   ├── visual.js            # Visual state (isVisible, isInViewport, checkOverlap)
│   ├── layout.js            # Layout diagnostics (findOverflows, findStackingContexts)
│   ├── interactive.js       # Interactive (selectElement, waitForElement, ask)
│   ├── capture.js           # State capture (captureDOM, captureStyles, captureState)
│   ├── accessibility.js     # A11y functions (getA11yInfo, getContrast, auditAccessibility)
│   ├── audit.js             # Quality audits (auditDOMComplexity, auditCSS, auditSecurity)
│   ├── interaction.js       # NEW: User interaction tracking
│   └── mutation.js          # NEW: DOM mutation tracking with highlighting
```

### Benefits
- Syntax highlighting and linting for JavaScript files
- Easier to test individual modules
- Clear separation of concerns
- Better IDE support for JS development

### Implementation Steps
1. Create `internal/proxy/scripts/` directory
2. Extract JavaScript sections into individual .js files
3. Create `embed.go` with `//go:embed` directives
4. Update `injector.go` to use embedded scripts
5. Wrap scripts in IIFE and build combined output
6. Update tests to verify embedding works

---

## Part 2: Interaction Logging

### New Log Entry Types

Add to `logger.go`:

```go
// LogTypeInteraction represents a user interaction event.
LogTypeInteraction LogEntryType = "interaction"

// LogTypeMutation represents a DOM mutation event.
LogTypeMutation LogEntryType = "mutation"
```

### Data Structures

```go
// InteractionEvent represents a user interaction.
type InteractionEvent struct {
    ID        string                 `json:"id"`
    Timestamp time.Time              `json:"timestamp"`
    EventType string                 `json:"event_type"` // click, dblclick, keydown, input, scroll, etc.
    Target    InteractionTarget      `json:"target"`
    Position  *InteractionPosition   `json:"position,omitempty"`   // For mouse events
    Key       *KeyboardInfo          `json:"key,omitempty"`        // For keyboard events
    Value     string                 `json:"value,omitempty"`      // For input events (sanitized)
    URL       string                 `json:"url"`
    Data      map[string]interface{} `json:"data,omitempty"`
}

type InteractionTarget struct {
    Selector   string            `json:"selector"`
    Tag        string            `json:"tag"`
    ID         string            `json:"id,omitempty"`
    Classes    []string          `json:"classes,omitempty"`
    Text       string            `json:"text,omitempty"`       // Truncated innerText
    Attributes map[string]string `json:"attributes,omitempty"` // Relevant attrs only
}

type InteractionPosition struct {
    ClientX int `json:"client_x"`
    ClientY int `json:"client_y"`
    PageX   int `json:"page_x"`
    PageY   int `json:"page_y"`
}

type KeyboardInfo struct {
    Key      string `json:"key"`
    Code     string `json:"code"`
    Ctrl     bool   `json:"ctrl,omitempty"`
    Alt      bool   `json:"alt,omitempty"`
    Shift    bool   `json:"shift,omitempty"`
    Meta     bool   `json:"meta,omitempty"`
}

// MutationEvent represents a DOM mutation.
type MutationEvent struct {
    ID          string           `json:"id"`
    Timestamp   time.Time        `json:"timestamp"`
    MutationType string          `json:"mutation_type"` // added, removed, attributes, characterData
    Target      MutationTarget   `json:"target"`
    Added       []MutationNode   `json:"added,omitempty"`
    Removed     []MutationNode   `json:"removed,omitempty"`
    Attribute   *AttributeChange `json:"attribute,omitempty"`
    URL         string           `json:"url"`
}

type MutationTarget struct {
    Selector string `json:"selector"`
    Tag      string `json:"tag"`
    ID       string `json:"id,omitempty"`
}

type MutationNode struct {
    Selector string `json:"selector"`
    Tag      string `json:"tag"`
    ID       string `json:"id,omitempty"`
    HTML     string `json:"html,omitempty"` // Truncated outerHTML
}

type AttributeChange struct {
    Name     string `json:"name"`
    OldValue string `json:"old_value,omitempty"`
    NewValue string `json:"new_value,omitempty"`
}
```

### Frontend JavaScript (`interaction.js`)

```javascript
// Interaction tracking module
(function() {
  'use strict';

  // Configuration
  var config = {
    maxHistorySize: 500,       // Max interactions in local buffer
    debounceScroll: 100,       // ms to debounce scroll events
    debounceInput: 300,        // ms to debounce input events
    truncateText: 100,         // Max chars for text content
    mouseMoveWindow: 60000,    // Sample mousemove for 1 minute around interactions
    mouseMoveInterval: 100,    // Sample mousemove every 100ms (interaction time)
    sendBatchSize: 10,         // Send in batches
    sendInterval: 1000         // ms between batch sends
  };

  // Mousemove sampling state
  var mouseMoveBuffer = [];           // Recent mousemove samples
  var lastInteractionTime = 0;        // Wall time of last meaningful interaction
  var lastMouseSampleTime = 0;        // Interaction-time of last mousemove sample
  var interactionTimeBase = 0;        // Base for interaction-time calculation

  // Local interaction buffer (circular)
  var interactions = [];
  var interactionIndex = 0;

  // Pending batch for server
  var pendingBatch = [];

  // Track last scroll/input to debounce
  var lastScroll = 0;
  var inputDebounce = {};

  // Generate target info from element
  function getTargetInfo(el) {
    if (!el || !(el instanceof HTMLElement)) return null;

    var text = (el.innerText || '').substring(0, config.truncateText);
    var attrs = {};

    // Only include relevant attributes
    ['href', 'src', 'type', 'name', 'placeholder', 'role', 'aria-label'].forEach(function(attr) {
      if (el.hasAttribute(attr)) {
        attrs[attr] = el.getAttribute(attr);
      }
    });

    return {
      selector: generateSelector(el),
      tag: el.tagName.toLowerCase(),
      id: el.id || undefined,
      classes: Array.from(el.classList).slice(0, 5),
      text: text || undefined,
      attributes: Object.keys(attrs).length > 0 ? attrs : undefined
    };
  }

  // Record interaction
  function recordInteraction(eventType, event, extra) {
    var target = event.target || event.srcElement;
    var targetInfo = getTargetInfo(target);
    if (!targetInfo) return;

    var interaction = {
      event_type: eventType,
      target: targetInfo,
      timestamp: Date.now()
    };

    // Add position for mouse events
    if (event.clientX !== undefined) {
      interaction.position = {
        client_x: event.clientX,
        client_y: event.clientY,
        page_x: event.pageX,
        page_y: event.pageY
      };
    }

    // Add key info for keyboard events
    if (event.key !== undefined) {
      interaction.key = {
        key: event.key,
        code: event.code,
        ctrl: event.ctrlKey || undefined,
        alt: event.altKey || undefined,
        shift: event.shiftKey || undefined,
        meta: event.metaKey || undefined
      };
    }

    // Add extra data
    if (extra) {
      Object.assign(interaction, extra);
    }

    // Store locally
    if (interactions.length < config.maxHistorySize) {
      interactions.push(interaction);
    } else {
      interactions[interactionIndex % config.maxHistorySize] = interaction;
    }
    interactionIndex++;

    // Queue for server
    pendingBatch.push(interaction);
  }

  // Event handlers
  function handleClick(e) {
    recordInteraction('click', e);
  }

  function handleDblClick(e) {
    recordInteraction('dblclick', e);
  }

  function handleKeyDown(e) {
    // Only track meaningful keys (skip modifiers alone)
    if (['Control', 'Alt', 'Shift', 'Meta'].indexOf(e.key) !== -1) return;
    recordInteraction('keydown', e);
  }

  function handleInput(e) {
    var target = e.target;
    var key = generateSelector(target);

    // Debounce per-element
    clearTimeout(inputDebounce[key]);
    inputDebounce[key] = setTimeout(function() {
      // Sanitize value (don't send passwords)
      var value = '';
      if (target.type !== 'password') {
        value = (target.value || '').substring(0, config.truncateText);
      }

      recordInteraction('input', e, { value: value });
    }, config.debounceInput);
  }

  function handleFocus(e) {
    recordInteraction('focus', e);
  }

  function handleBlur(e) {
    recordInteraction('blur', e);
  }

  function handleScroll(e) {
    var now = Date.now();
    if (now - lastScroll < config.debounceScroll) return;
    lastScroll = now;

    recordInteraction('scroll', {
      target: e.target === document ? document.documentElement : e.target,
      clientX: undefined,
      clientY: undefined
    }, {
      scroll_position: {
        x: window.scrollX || document.documentElement.scrollLeft,
        y: window.scrollY || document.documentElement.scrollTop
      }
    });
  }

  function handleSubmit(e) {
    recordInteraction('submit', e);
  }

  function handleContextMenu(e) {
    recordInteraction('contextmenu', e);
  }

  // Mousemove handler - samples based on interaction time
  function handleMouseMove(e) {
    var now = Date.now();

    // Only sample if within window of last meaningful interaction
    if (now - lastInteractionTime > config.mouseMoveWindow) {
      return;
    }

    // Calculate interaction time (time since last interaction, not wall time)
    var interactionTime = now - interactionTimeBase;

    // Sample at configured interval based on interaction time
    if (interactionTime - lastMouseSampleTime < config.mouseMoveInterval) {
      return;
    }
    lastMouseSampleTime = interactionTime;

    // Record the mousemove sample
    var sample = {
      event_type: 'mousemove',
      position: {
        client_x: e.clientX,
        client_y: e.clientY,
        page_x: e.pageX,
        page_y: e.pageY
      },
      wall_time: now,
      interaction_time: interactionTime,
      timestamp: now
    };

    // Get element under cursor
    var target = document.elementFromPoint(e.clientX, e.clientY);
    if (target) {
      sample.target = getTargetInfo(target);
    }

    mouseMoveBuffer.push(sample);

    // Trim buffer to last minute of samples
    var cutoff = now - config.mouseMoveWindow;
    while (mouseMoveBuffer.length > 0 && mouseMoveBuffer[0].wall_time < cutoff) {
      mouseMoveBuffer.shift();
    }
  }

  // Reset interaction time base on meaningful interactions
  function resetInteractionTime() {
    var now = Date.now();
    lastInteractionTime = now;
    interactionTimeBase = now;
    lastMouseSampleTime = 0;
  }

  // Wrap click/key handlers to reset interaction time
  var originalHandleClick = handleClick;
  handleClick = function(e) {
    resetInteractionTime();
    originalHandleClick(e);
  };

  var originalHandleKeyDown = handleKeyDown;
  handleKeyDown = function(e) {
    if (['Control', 'Alt', 'Shift', 'Meta'].indexOf(e.key) === -1) {
      resetInteractionTime();
    }
    originalHandleKeyDown(e);
  };

  // Attach listeners
  function attachListeners() {
    document.addEventListener('click', handleClick, true);
    document.addEventListener('dblclick', handleDblClick, true);
    document.addEventListener('keydown', handleKeyDown, true);
    document.addEventListener('input', handleInput, true);
    document.addEventListener('focus', handleFocus, true);
    document.addEventListener('blur', handleBlur, true);
    document.addEventListener('scroll', handleScroll, true);
    document.addEventListener('submit', handleSubmit, true);
    document.addEventListener('contextmenu', handleContextMenu, true);
    document.addEventListener('mousemove', handleMouseMove, true);
  }

  // Send batch to server
  function sendBatch() {
    if (pendingBatch.length === 0) return;

    var batch = pendingBatch.splice(0, config.sendBatchSize);
    send('interactions', { events: batch });
  }

  // Start batch sender
  setInterval(sendBatch, config.sendInterval);

  // Initialize
  attachListeners();

  // Expose API
  window.__devtool = window.__devtool || {};
  window.__devtool.interactions = {
    getHistory: function(count) {
      count = count || 50;
      var start = Math.max(0, interactions.length - count);
      return interactions.slice(start);
    },

    getLastClick: function() {
      for (var i = interactions.length - 1; i >= 0; i--) {
        if (interactions[i].event_type === 'click') {
          return interactions[i];
        }
      }
      return null;
    },

    getClicksOn: function(selector) {
      return interactions.filter(function(i) {
        return i.event_type === 'click' &&
               i.target.selector.indexOf(selector) !== -1;
      });
    },

    // Get mousemove samples around a specific interaction
    getMouseTrail: function(interactionTimestamp, windowMs) {
      windowMs = windowMs || 5000;
      var start = interactionTimestamp - windowMs;
      var end = interactionTimestamp + windowMs;

      return mouseMoveBuffer.filter(function(m) {
        return m.wall_time >= start && m.wall_time <= end;
      });
    },

    // Get all mousemove samples in buffer
    getMouseBuffer: function() {
      return mouseMoveBuffer.slice();
    },

    // Get context around last click (click + mouse trail)
    getLastClickContext: function(trailMs) {
      var click = this.getLastClick();
      if (!click) return null;

      return {
        click: click,
        mouseTrail: this.getMouseTrail(click.timestamp, trailMs || 2000)
      };
    },

    clear: function() {
      interactions = [];
      interactionIndex = 0;
      mouseMoveBuffer = [];
    },

    config: config
  };
})();
```

### Frontend JavaScript (`mutation.js`)

```javascript
// DOM mutation tracking module
(function() {
  'use strict';

  var config = {
    maxHistorySize: 200,
    highlightDuration: 2000,
    highlightAddedColor: 'rgba(0, 255, 0, 0.2)',
    highlightRemovedColor: 'rgba(255, 0, 0, 0.2)',
    highlightModifiedColor: 'rgba(255, 255, 0, 0.2)',
    trackAttributes: true,
    trackCharacterData: false,
    ignoreSelectors: ['.__devtool', 'script', 'style', 'link'],
    sendBatchSize: 10,
    sendInterval: 1000
  };

  var mutations = [];
  var pendingBatch = [];
  var highlightElements = new Map();

  // Check if element should be ignored
  function shouldIgnore(node) {
    if (!(node instanceof HTMLElement)) return true;

    for (var i = 0; i < config.ignoreSelectors.length; i++) {
      try {
        if (node.matches && node.matches(config.ignoreSelectors[i])) return true;
        if (node.closest && node.closest(config.ignoreSelectors[i])) return true;
      } catch (e) {}
    }
    return false;
  }

  // Create mutation record
  function createMutationRecord(type, mutation) {
    var target = mutation.target;
    if (shouldIgnore(target)) return null;

    var record = {
      mutation_type: type,
      target: {
        selector: generateSelector(target),
        tag: target.tagName ? target.tagName.toLowerCase() : 'text',
        id: target.id || undefined
      },
      timestamp: Date.now()
    };

    return record;
  }

  // Highlight element temporarily
  function highlightMutation(element, color) {
    if (!element || !(element instanceof HTMLElement)) return;
    if (shouldIgnore(element)) return;

    var originalBg = element.style.backgroundColor;
    var originalOutline = element.style.outline;

    element.style.backgroundColor = color;
    element.style.outline = '2px solid ' + color.replace('0.2', '0.8');

    var id = 'mutation-' + Date.now() + '-' + Math.random();
    highlightElements.set(id, { element: element, originalBg: originalBg, originalOutline: originalOutline });

    setTimeout(function() {
      var info = highlightElements.get(id);
      if (info && info.element) {
        info.element.style.backgroundColor = info.originalBg;
        info.element.style.outline = info.originalOutline;
        highlightElements.delete(id);
      }
    }, config.highlightDuration);
  }

  // Mutation observer callback
  function handleMutations(mutationsList) {
    for (var i = 0; i < mutationsList.length; i++) {
      var mutation = mutationsList[i];

      if (mutation.type === 'childList') {
        // Added nodes
        mutation.addedNodes.forEach(function(node) {
          if (shouldIgnore(node)) return;

          var record = {
            mutation_type: 'added',
            target: {
              selector: generateSelector(mutation.target),
              tag: mutation.target.tagName.toLowerCase(),
              id: mutation.target.id || undefined
            },
            added: [{
              selector: node.nodeType === 1 ? generateSelector(node) : null,
              tag: node.nodeName.toLowerCase(),
              id: node.id || undefined,
              html: node.outerHTML ? node.outerHTML.substring(0, 500) : undefined
            }],
            timestamp: Date.now()
          };

          mutations.push(record);
          pendingBatch.push(record);

          if (node instanceof HTMLElement) {
            highlightMutation(node, config.highlightAddedColor);
          }
        });

        // Removed nodes
        mutation.removedNodes.forEach(function(node) {
          if (shouldIgnore(node)) return;

          var record = {
            mutation_type: 'removed',
            target: {
              selector: generateSelector(mutation.target),
              tag: mutation.target.tagName.toLowerCase(),
              id: mutation.target.id || undefined
            },
            removed: [{
              tag: node.nodeName.toLowerCase(),
              id: node.id || undefined,
              html: node.outerHTML ? node.outerHTML.substring(0, 200) : undefined
            }],
            timestamp: Date.now()
          };

          mutations.push(record);
          pendingBatch.push(record);
        });
      }

      if (mutation.type === 'attributes' && config.trackAttributes) {
        var target = mutation.target;
        if (shouldIgnore(target)) continue;

        var record = {
          mutation_type: 'attributes',
          target: {
            selector: generateSelector(target),
            tag: target.tagName.toLowerCase(),
            id: target.id || undefined
          },
          attribute: {
            name: mutation.attributeName,
            old_value: mutation.oldValue,
            new_value: target.getAttribute(mutation.attributeName)
          },
          timestamp: Date.now()
        };

        mutations.push(record);
        pendingBatch.push(record);
        highlightMutation(target, config.highlightModifiedColor);
      }
    }

    // Trim history
    if (mutations.length > config.maxHistorySize) {
      mutations = mutations.slice(-config.maxHistorySize);
    }
  }

  // Send batch to server
  function sendBatch() {
    if (pendingBatch.length === 0) return;

    var batch = pendingBatch.splice(0, config.sendBatchSize);
    send('mutations', { events: batch });
  }

  // Start observer
  var observer = new MutationObserver(handleMutations);

  observer.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: config.trackAttributes,
    attributeOldValue: true,
    characterData: config.trackCharacterData,
    characterDataOldValue: true
  });

  // Start batch sender
  setInterval(sendBatch, config.sendInterval);

  // Expose API
  window.__devtool = window.__devtool || {};
  window.__devtool.mutations = {
    getHistory: function(count) {
      count = count || 50;
      var start = Math.max(0, mutations.length - count);
      return mutations.slice(start);
    },

    getAdded: function(since) {
      since = since || 0;
      return mutations.filter(function(m) {
        return m.mutation_type === 'added' && m.timestamp > since;
      });
    },

    getRemoved: function(since) {
      since = since || 0;
      return mutations.filter(function(m) {
        return m.mutation_type === 'removed' && m.timestamp > since;
      });
    },

    getModified: function(since) {
      since = since || 0;
      return mutations.filter(function(m) {
        return m.mutation_type === 'attributes' && m.timestamp > since;
      });
    },

    highlightRecent: function(duration) {
      duration = duration || 5000;
      var since = Date.now() - duration;

      mutations.forEach(function(m) {
        if (m.timestamp > since && m.mutation_type === 'added' && m.added) {
          m.added.forEach(function(node) {
            if (node.selector) {
              var el = document.querySelector(node.selector);
              if (el) highlightMutation(el, config.highlightAddedColor);
            }
          });
        }
      });
    },

    clear: function() {
      mutations = [];
    },

    pause: function() {
      observer.disconnect();
    },

    resume: function() {
      observer.observe(document.body, {
        childList: true,
        subtree: true,
        attributes: config.trackAttributes,
        attributeOldValue: true
      });
    },

    config: config
  };
})();
```

---

## Part 3: Backend Changes

### Logger Updates (`logger.go`)

1. Add new log entry types:
```go
LogTypeInteraction LogEntryType = "interaction"
LogTypeMutation LogEntryType = "mutation"
```

2. Add new entry structs (InteractionEvent, MutationEvent as defined above)

3. Add to LogEntry union:
```go
type LogEntry struct {
    // ... existing fields ...
    Interaction *InteractionEvent `json:"interaction,omitempty"`
    Mutation    *MutationEvent    `json:"mutation,omitempty"`
}
```

4. Add logging methods:
```go
func (tl *TrafficLogger) LogInteraction(entry InteractionEvent)
func (tl *TrafficLogger) LogMutation(entry MutationEvent)
```

### Server Updates (`server.go`)

Add WebSocket message handlers:

```go
case "interactions":
    events := getArrayField(msg.Data, "events")
    for _, eventData := range events {
        // Parse and log each interaction
        ps.logger.LogInteraction(...)
    }
    ps.pageTracker.TrackInteractions(...)

case "mutations":
    events := getArrayField(msg.Data, "events")
    for _, eventData := range events {
        // Parse and log each mutation
        ps.logger.LogMutation(...)
    }
    ps.pageTracker.TrackMutations(...)
```

### PageTracker Updates (`pagetracker.go`)

Add interaction/mutation tracking to page sessions (feeds `currentpage` tool):

```go
type PageSession struct {
    // ... existing fields ...
    Interactions     []InteractionEvent `json:"interactions,omitempty"`
    Mutations        []MutationEvent    `json:"mutations,omitempty"`
    InteractionCount int                `json:"interaction_count"` // For list view
    MutationCount    int                `json:"mutation_count"`    // For list view
}

// MaxInteractions and MaxMutations per session (circular buffer behavior)
const MaxInteractionsPerSession = 200
const MaxMutationsPerSession = 100

func (pt *PageTracker) TrackInteractions(events []InteractionEvent, url string)
func (pt *PageTracker) TrackMutations(events []MutationEvent, url string)
```

The `currentpage` tool's list action will show counts, and the get action will return full history.

### Filter Updates (`logger.go`)

Extend LogFilter to support new types:

```go
type LogFilter struct {
    // ... existing fields ...
    InteractionTypes []string `json:"interaction_types,omitempty"` // click, keydown, etc.
    MutationTypes    []string `json:"mutation_types,omitempty"`    // added, removed, attributes
}
```

---

## Part 4: Implementation Order

### Phase 1: Injector Refactoring (2-3 hours)
1. Create `internal/proxy/scripts/` directory
2. Extract core.js (WebSocket, send, error/perf tracking)
3. Extract utils.js (helper functions)
4. Extract remaining modules one by one
5. Create embed.go with //go:embed
6. Update injector.go to use embedded scripts
7. Verify all tests pass

### Phase 2: Backend Data Structures (1 hour)
1. Add InteractionEvent, MutationEvent structs to logger.go
2. Add new LogEntryTypes
3. Add logging methods
4. Update LogEntry union type
5. Update LogFilter
6. Write unit tests

### Phase 3: Frontend Interaction Tracking (2 hours)
1. Create interaction.js with event listeners
2. Implement local circular buffer
3. Implement batching and sending
4. Add __devtool.interactions API
5. Test in browser

### Phase 4: Frontend Mutation Tracking (2 hours)
1. Create mutation.js with MutationObserver
2. Implement highlighting system
3. Implement batching and sending
4. Add __devtool.mutations API
5. Test in browser

### Phase 5: Backend Integration (1-2 hours)
1. Add WebSocket handlers for interactions/mutations
2. Update PageTracker with new tracking methods
3. Write integration tests
4. Update proxylog tool to filter by new types

### Phase 6: Testing & Documentation (1 hour)
1. End-to-end testing with sample application
2. Update CLAUDE.md with new features
3. Add examples to test-diagnostics.html

---

## API Reference (After Implementation)

### Frontend API

```javascript
// Get last 50 interactions
__devtool.interactions.getHistory(50)

// Get last click
__devtool.interactions.getLastClick()

// Get clicks on specific selector
__devtool.interactions.getClicksOn('.button')

// Get mouse trail around last click (for "I clicked but nothing happened" debugging)
__devtool.interactions.getLastClickContext(2000)  // 2 second window

// Get mouse movements around a specific timestamp
__devtool.interactions.getMouseTrail(timestamp, 5000)

// Get all buffered mouse movements (last minute around interactions)
__devtool.interactions.getMouseBuffer()

// Get recent DOM additions
__devtool.mutations.getAdded(Date.now() - 5000)

// Highlight elements added in last 5 seconds
__devtool.mutations.highlightRecent(5000)

// Pause/resume mutation tracking
__devtool.mutations.pause()
__devtool.mutations.resume()
```

### MCP Tool - currentpage (Updated)

The `currentpage` tool will be extended to include interactions and mutations:

**List action** (summary):
```json
currentpage {proxy_id: "dev"}
// Returns:
{
  "sessions": [{
    "id": "page-1",
    "url": "http://localhost:3000/dashboard",
    "active": true,
    "start_time": "...",
    "last_activity": "...",
    "resource_count": 15,
    "error_count": 0,
    "interaction_count": 42,   // NEW
    "mutation_count": 12       // NEW
  }]
}
```

**Get action** (detailed):
```json
currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
// Returns full PageSession with:
{
  "id": "page-1",
  "url": "...",
  "document_request": {...},
  "resources": [...],
  "errors": [...],
  "performance": {...},
  "interactions": [...],  // NEW: Recent interactions
  "mutations": [...]      // NEW: Recent mutations
}
```

No new tools needed - data integrates with existing `currentpage` tool.

---

## Design Decisions (Confirmed)

1. **Mouse tracking**: Track all clicks and sample mousemove at 100ms steps based on **interaction time** (time since last meaningful interaction like click/keydown), not wall time. Also record wall time for each sample. This provides context around user actions without continuous noise.

2. **JS organization**: Use separate `.js` files with `//go:embed` for better developer experience (syntax highlighting, linting, easier testing).

3. **Sensitive data**: Filter `input[type=password]` values only. Other inputs are logged as-is (truncated).

4. **Mutation limits**: 500 chars for outerHTML of added nodes.

5. **Highlight styling**: Green (added), Red (removed), Yellow (modified) - configurable via `__devtool.mutations.config`.
