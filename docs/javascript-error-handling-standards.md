# JavaScript Error Handling Standards for Injected Code

## Critical Requirements

The DevTool instrumentation code runs in **untrusted, unpredictable environments**. It must be bulletproof to avoid breaking user pages.

## Core Principles

### 1. Never Throw Exceptions
- **Always** wrap top-level code in try-catch
- Return `{error: "message"}` instead of throwing
- Use error objects: `{success: false, error: "details"}`

### 2. Validate Everything
- Check all inputs before use
- Validate types: `typeof x === 'expected'`
- Check existence: `if (x && x.property)`
- Validate DOM: `instanceof HTMLElement`

### 3. Graceful Degradation
- If a feature fails, log and continue
- Never break the entire module
- Provide fallbacks for missing APIs

### 4. Defensive DOM Operations
```javascript
// BAD
element.style.color = 'red';

// GOOD
try {
  if (element && element.style) {
    element.style.color = 'red';
  }
} catch (e) {
  reportError('style_update_failed', e);
}
```

### 5. Safe Dependency Loading
```javascript
// BAD
var utils = window.__devtool_utils;
utils.someFunction();

// GOOD
var utils = window.__devtool_utils;
if (!utils) {
  console.error('[DevTool] Missing dependency: utils');
  return;
}
if (typeof utils.someFunction !== 'function') {
  console.error('[DevTool] Invalid utils.someFunction');
  return;
}
```

## Module Structure Template

```javascript
(function() {
  'use strict';

  // Top-level error boundary
  try {
    // Dependency validation
    var core = window.__devtool_core;
    if (!core || typeof core.send !== 'function') {
      console.error('[DevTool] Module disabled - missing core');
      return;
    }

    // Module code with error handling
    function safeFunction(param) {
      // Input validation
      if (typeof param !== 'string') {
        return {error: 'Invalid parameter type'};
      }

      try {
        // Operation
        var result = doSomething(param);
        return {success: true, data: result};
      } catch (e) {
        reportError('function_failed', e);
        return {error: e.message};
      }
    }

    // Error reporting
    function reportError(context, error) {
      try {
        if (core && typeof core.send === 'function') {
          core.send('error', {
            context: context,
            message: error.message || String(error),
            stack: error.stack || '',
            module: 'module_name',
            timestamp: Date.now()
          });
        }
        console.error('[DevTool][module_name]', context, error);
      } catch (e) {
        // Last resort - just log
        console.error('[DevTool] Error reporting failed:', e);
      }
    }

    // Export with existence check
    if (!window.__devtool) {
      window.__devtool = {};
    }
    window.__devtool.module = {
      safeFunction: safeFunction
    };

  } catch (e) {
    // Top-level failure
    console.error('[DevTool] Module initialization failed:', e);
  }
})();
```

## Specific Patterns

### DOM Operations
```javascript
function safeQuerySelector(selector) {
  if (typeof selector !== 'string' || !selector) {
    return null;
  }

  try {
    return document.querySelector(selector);
  } catch (e) {
    reportError('invalid_selector', e);
    return null;
  }
}

function safeSetStyle(element, property, value) {
  if (!element || !(element instanceof HTMLElement)) {
    return false;
  }

  if (!element.style) {
    return false;
  }

  try {
    element.style[property] = value;
    return true;
  } catch (e) {
    reportError('style_update_failed', e);
    return false;
  }
}
```

### Event Handlers
```javascript
function safeAddEventListener(target, event, handler) {
  if (!target || typeof target.addEventListener !== 'function') {
    return false;
  }

  if (typeof handler !== 'function') {
    return false;
  }

  try {
    // Wrap handler in try-catch
    var safeHandler = function(e) {
      try {
        handler(e);
      } catch (err) {
        reportError('event_handler_error', err);
      }
    };

    target.addEventListener(event, safeHandler);
    return true;
  } catch (e) {
    reportError('addEventListener_failed', e);
    return false;
  }
}
```

### WebSocket Operations
```javascript
function safeWebSocketSend(ws, data) {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    return false;
  }

  try {
    ws.send(data);
    return true;
  } catch (e) {
    reportError('websocket_send_failed', e);
    return false;
  }
}
```

### API Feature Detection
```javascript
function hasWebSocketSupport() {
  return typeof WebSocket !== 'undefined';
}

function hasSessionStorage() {
  try {
    var test = '__test__';
    sessionStorage.setItem(test, test);
    sessionStorage.removeItem(test);
    return true;
  } catch (e) {
    return false;
  }
}

function hasPerformanceAPI() {
  return window.performance &&
         typeof window.performance.now === 'function';
}
```

### JSON Operations
```javascript
function safeJsonParse(str) {
  if (typeof str !== 'string') {
    return {error: 'Invalid input type'};
  }

  try {
    return {success: true, data: JSON.parse(str)};
  } catch (e) {
    return {error: 'JSON parse failed: ' + e.message};
  }
}

function safeJsonStringify(obj) {
  try {
    return {success: true, data: JSON.stringify(obj)};
  } catch (e) {
    // Circular reference or non-serializable
    try {
      // Fallback: stringify with replacer
      return {
        success: true,
        data: JSON.stringify(obj, function(k, v) {
          if (typeof v === 'function') return '[Function]';
          if (typeof v === 'symbol') return '[Symbol]';
          return v;
        })
      };
    } catch (e2) {
      return {error: 'Stringify failed: ' + e2.message};
    }
  }
}
```

### MutationObserver
```javascript
function createSafeMutationObserver(callback) {
  if (typeof MutationObserver === 'undefined') {
    return null;
  }

  if (typeof callback !== 'function') {
    return null;
  }

  try {
    return new MutationObserver(function(mutations) {
      try {
        callback(mutations);
      } catch (e) {
        reportError('mutation_callback_error', e);
      }
    });
  } catch (e) {
    reportError('observer_creation_failed', e);
    return null;
  }
}

function safeObserve(observer, target, options) {
  if (!observer || typeof observer.observe !== 'function') {
    return false;
  }

  if (!target || !(target instanceof Node)) {
    return false;
  }

  // Validate options
  if (!options || typeof options !== 'object') {
    return false;
  }

  // Fix invalid combinations
  if (options.characterDataOldValue && !options.characterData) {
    options.characterData = true;
  }
  if (options.attributeOldValue && !options.attributes) {
    options.attributes = true;
  }

  try {
    observer.observe(target, options);
    return true;
  } catch (e) {
    reportError('observer_observe_failed', e);
    return false;
  }
}
```

### Async Operations
```javascript
function safePromise(promiseFn) {
  return new Promise(function(resolve) {
    try {
      promiseFn()
        .then(function(result) {
          resolve({success: true, data: result});
        })
        .catch(function(error) {
          resolve({error: error.message || String(error)});
        });
    } catch (e) {
      resolve({error: e.message || String(e)});
    }
  });
}
```

## Error Recovery Strategies

### 1. Retry Logic
```javascript
function retryOperation(operation, maxAttempts, delay) {
  var attempts = 0;

  function attempt() {
    try {
      var result = operation();
      if (result && result.success) {
        return result;
      }
    } catch (e) {
      attempts++;
      if (attempts < maxAttempts) {
        setTimeout(attempt, delay * attempts);
        return;
      }
      return {error: 'Max retries exceeded'};
    }
  }

  return attempt();
}
```

### 2. Fallback Values
```javascript
function getConfigValue(key, fallback) {
  try {
    var value = config[key];
    if (value !== undefined && value !== null) {
      return value;
    }
  } catch (e) {
    reportError('config_access_failed', e);
  }
  return fallback;
}
```

### 3. Circuit Breaker
```javascript
var circuitBreaker = {
  failures: 0,
  threshold: 5,
  timeout: 60000,
  lastFailure: 0,

  isOpen: function() {
    if (this.failures >= this.threshold) {
      if (Date.now() - this.lastFailure < this.timeout) {
        return true;
      }
      // Reset after timeout
      this.failures = 0;
    }
    return false;
  },

  recordFailure: function() {
    this.failures++;
    this.lastFailure = Date.now();
  },

  recordSuccess: function() {
    this.failures = 0;
  }
};
```

## Testing Checklist

For each module, verify:

- [ ] Top-level try-catch wraps entire module
- [ ] All dependencies validated before use
- [ ] All DOM operations have null checks
- [ ] All event handlers wrapped in try-catch
- [ ] All API features detected before use
- [ ] All inputs validated (type, existence)
- [ ] All errors reported to server
- [ ] Module degrades gracefully on failure
- [ ] No operations can throw unhandled exceptions
- [ ] Module exports are properly guarded

## Priority Modules (Critical Path)

1. **core.js** - Foundation for all modules
2. **utils.js** - Used by all other modules
3. **mutation.js** - DOM observation, high failure risk
4. **interaction.js** - Event handlers, high volume
5. **indicator.js** - User-facing UI
6. **sketch.js** - Complex DOM manipulation
7. **design.js** - Dynamic HTML injection

## Common Pitfalls

### ❌ Unsafe Patterns
```javascript
// No validation
element.style.color = 'red';

// Uncaught errors
JSON.parse(untrustedData);

// No feature detection
observer.observe(target, options);

// Throwing errors
throw new Error('Failed');
```

### ✅ Safe Patterns
```javascript
// Validated
if (element && element.style) {
  try {
    element.style.color = 'red';
  } catch (e) {}
}

// Safe parsing
var result = safeJsonParse(untrustedData);
if (result.error) return;

// Feature detection
if (typeof MutationObserver !== 'undefined') {
  var observer = createSafeMutationObserver(callback);
}

// Return errors
return {error: 'Failed', details: e.message};
```
