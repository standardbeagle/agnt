// Sketch Mode for DevTool
// An Excalidraw-like drawing interface for wireframes and annotations

(function() {
  'use strict';

  var core = window.__devtool_core;

  // Sketch state
  var sketchState = {
    isActive: false,
    container: null,
    canvas: null,
    ctx: null,
    toolbar: null,
    backgroundImage: null, // Captured page screenshot

    // Drawing state
    tool: 'select',
    isDrawing: false,
    startPoint: null,
    currentElement: null,

    // Elements
    elements: [],
    selectedElements: [],
    hoveredElement: null,

    // History for undo/redo
    history: [],
    historyIndex: -1,

    // View transform
    offsetX: 0,
    offsetY: 0,
    scale: 1,

    // Tool settings
    strokeColor: '#1e1e1e',
    fillColor: 'transparent',
    strokeWidth: 2,
    fontSize: 16,
    fontFamily: 'Virgil, Segoe UI, sans-serif',
    roughness: 1, // 0 = smooth, 1 = sketchy

    // Clipboard
    clipboard: null,

    // Selection
    selectionBox: null,
    isDragging: false,
    isResizing: false,
    resizeHandle: null,
    dragStartPos: null,

    // Voice annotation
    voiceBtn: null,
    isVoiceListening: false,
    voiceAnnotationPos: null,

    // Annotation counter for numbered markers
    annotationCounter: 1
  };

  // Tool definitions
  var TOOLS = {
    select: { name: 'Select', icon: 'M3,3H9V9H3V3M15,3H21V9H15V3M3,15H9V21H3V15M15,15H21V21H15V15' },
    marker: { name: 'Marker', icon: 'M12,2C8.13,2 5,5.13 5,9C5,14.25 12,22 12,22C12,22 19,14.25 19,9C19,5.13 15.87,2 12,2M12,11.5C10.62,11.5 9.5,10.38 9.5,9C9.5,7.62 10.62,6.5 12,6.5C13.38,6.5 14.5,7.62 14.5,9C14.5,10.38 13.38,11.5 12,11.5Z' },
    rectangle: { name: 'Rectangle', icon: 'M3,3H21V21H3V3M5,5V19H19V5H5Z' },
    ellipse: { name: 'Ellipse', icon: 'M12,2A10,10 0 0,1 22,12A10,10 0 0,1 12,22A10,10 0 0,1 2,12A10,10 0 0,1 12,2M12,4A8,8 0 0,0 4,12A8,8 0 0,0 12,20A8,8 0 0,0 20,12A8,8 0 0,0 12,4Z' },
    arrow: { name: 'Arrow', icon: 'M4,11V13H16L10.5,18.5L11.92,19.92L19.84,12L11.92,4.08L10.5,5.5L16,11H4Z' },
    freedraw: { name: 'Pen', icon: 'M20.71,7.04C21.1,6.65 21.1,6 20.71,5.63L18.37,3.29C18,2.9 17.35,2.9 16.96,3.29L15.12,5.12L18.87,8.87M3,17.25V21H6.75L17.81,9.93L14.06,6.18L3,17.25Z' },
    text: { name: 'Text', icon: 'M5,4V7H10.5V19H13.5V7H19V4H5Z' },
    eraser: { name: 'Eraser', icon: 'M16.24,3.56L21.19,8.5C21.97,9.29 21.97,10.55 21.19,11.34L12,20.53C10.44,22.09 7.91,22.09 6.34,20.53L2.81,17C2.03,16.21 2.03,14.95 2.81,14.16L13.41,3.56C14.2,2.78 15.46,2.78 16.24,3.56M4.22,15.58L7.76,19.11C8.54,19.9 9.8,19.9 10.59,19.11L14.12,15.58L9.17,10.63L4.22,15.58Z' }
  };

  // Balsamiq-style sketchy rendering helpers
  var roughness = {
    jitter: function(value, amount) {
      if (sketchState.roughness === 0) return value;
      return value + (Math.random() - 0.5) * amount * sketchState.roughness;
    },

    line: function(ctx, x1, y1, x2, y2) {
      if (sketchState.roughness === 0) {
        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.lineTo(x2, y2);
        ctx.stroke();
        return;
      }

      // Sketchy line with slight wobble
      ctx.beginPath();
      var segments = Math.max(2, Math.floor(Math.hypot(x2 - x1, y2 - y1) / 20));
      ctx.moveTo(roughness.jitter(x1, 2), roughness.jitter(y1, 2));

      for (var i = 1; i <= segments; i++) {
        var t = i / segments;
        var x = x1 + (x2 - x1) * t;
        var y = y1 + (y2 - y1) * t;
        ctx.lineTo(roughness.jitter(x, 2), roughness.jitter(y, 2));
      }
      ctx.stroke();
    },

    rect: function(ctx, x, y, w, h, fill) {
      if (fill && fill !== 'transparent') {
        ctx.fillStyle = fill;
        ctx.fillRect(x, y, w, h);
      }

      roughness.line(ctx, x, y, x + w, y);
      roughness.line(ctx, x + w, y, x + w, y + h);
      roughness.line(ctx, x + w, y + h, x, y + h);
      roughness.line(ctx, x, y + h, x, y);
    },

    ellipse: function(ctx, cx, cy, rx, ry, fill) {
      if (fill && fill !== 'transparent') {
        ctx.fillStyle = fill;
        ctx.beginPath();
        ctx.ellipse(cx, cy, rx, ry, 0, 0, Math.PI * 2);
        ctx.fill();
      }

      if (sketchState.roughness === 0) {
        ctx.beginPath();
        ctx.ellipse(cx, cy, rx, ry, 0, 0, Math.PI * 2);
        ctx.stroke();
        return;
      }

      // Sketchy ellipse
      ctx.beginPath();
      var segments = 36;
      for (var i = 0; i <= segments; i++) {
        var angle = (i / segments) * Math.PI * 2;
        var x = cx + rx * Math.cos(angle);
        var y = cy + ry * Math.sin(angle);
        if (i === 0) {
          ctx.moveTo(roughness.jitter(x, 2), roughness.jitter(y, 2));
        } else {
          ctx.lineTo(roughness.jitter(x, 2), roughness.jitter(y, 2));
        }
      }
      ctx.stroke();
    },

    arrow: function(ctx, x1, y1, x2, y2) {
      roughness.line(ctx, x1, y1, x2, y2);

      // Arrowhead
      var angle = Math.atan2(y2 - y1, x2 - x1);
      var headLen = 15;
      var headAngle = Math.PI / 6;

      roughness.line(ctx, x2, y2,
        x2 - headLen * Math.cos(angle - headAngle),
        y2 - headLen * Math.sin(angle - headAngle));
      roughness.line(ctx, x2, y2,
        x2 - headLen * Math.cos(angle + headAngle),
        y2 - headLen * Math.sin(angle + headAngle));
    }
  };

  // Element class
  function createElement(type, props) {
    return {
      id: 'el_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9),
      type: type,
      x: props.x || 0,
      y: props.y || 0,
      width: props.width || 0,
      height: props.height || 0,
      strokeColor: props.strokeColor || sketchState.strokeColor,
      fillColor: props.fillColor || sketchState.fillColor,
      strokeWidth: props.strokeWidth || sketchState.strokeWidth,
      roughness: props.roughness !== undefined ? props.roughness : sketchState.roughness,
      text: props.text || '',
      fontSize: props.fontSize || sketchState.fontSize,
      fontFamily: props.fontFamily || sketchState.fontFamily,
      points: props.points || [], // For freedraw
      rotation: props.rotation || 0,
      locked: false,
      opacity: props.opacity || 1
    };
  }

  // CSS Styles
  var STYLES = {
    container: [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'z-index: 2147483645',
      'background: transparent',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif'
    ].join(';'),

    canvas: [
      'position: absolute',
      'top: 0',
      'left: 0',
      'cursor: crosshair'
    ].join(';'),

    toolbar: [
      'position: absolute',
      'top: 16px',
      'left: 50%',
      'transform: translateX(-50%)',
      'display: flex',
      'gap: 4px',
      'padding: 8px',
      'background: white',
      'border-radius: 8px',
      'box-shadow: 0 2px 10px rgba(0, 0, 0, 0.1)'
    ].join(';'),

    toolButton: [
      'width: 36px',
      'height: 36px',
      'border: none',
      'border-radius: 6px',
      'background: transparent',
      'cursor: pointer',
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'transition: background 0.2s ease'
    ].join(';'),

    toolButtonActive: [
      'background: #667eea',
      'color: white'
    ].join(';'),

    separator: [
      'width: 1px',
      'height: 24px',
      'background: #e0e0e0',
      'margin: 6px 4px'
    ].join(';'),

    sidebar: [
      'position: absolute',
      'top: 80px',
      'left: 16px',
      'width: 200px',
      'background: white',
      'border-radius: 8px',
      'box-shadow: 0 2px 10px rgba(0, 0, 0, 0.1)',
      'padding: 16px'
    ].join(';'),

    actionBar: [
      'position: absolute',
      'bottom: 16px',
      'left: 50%',
      'transform: translateX(-50%)',
      'display: flex',
      'gap: 8px',
      'padding: 8px 12px',
      'background: white',
      'border-radius: 8px',
      'box-shadow: 0 2px 10px rgba(0, 0, 0, 0.1)'
    ].join(';'),

    actionButton: [
      'padding: 8px 16px',
      'border: none',
      'border-radius: 6px',
      'font-size: 13px',
      'font-weight: 500',
      'cursor: pointer',
      'transition: background 0.2s ease, transform 0.1s ease'
    ].join(';'),

    primaryAction: [
      'background: linear-gradient(135deg, #667eea 0%, #764ba2 100%)',
      'color: white'
    ].join(';'),

    secondaryAction: [
      'background: #f0f0f0',
      'color: #333'
    ].join(';'),

    closeButton: [
      'position: absolute',
      'top: 16px',
      'right: 16px',
      'width: 40px',
      'height: 40px',
      'border: none',
      'border-radius: 50%',
      'background: white',
      'box-shadow: 0 2px 10px rgba(0, 0, 0, 0.1)',
      'cursor: pointer',
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'font-size: 20px'
    ].join(';'),

    colorPicker: [
      'width: 28px',
      'height: 28px',
      'border: 2px solid #e0e0e0',
      'border-radius: 50%',
      'cursor: pointer',
      'padding: 0'
    ].join(';'),

    inputField: [
      'width: 100%',
      'padding: 6px 10px',
      'border: 1px solid #e0e0e0',
      'border-radius: 4px',
      'font-size: 13px',
      'outline: none'
    ].join(';'),

    label: [
      'display: block',
      'font-size: 11px',
      'color: #666',
      'margin-bottom: 4px',
      'text-transform: uppercase',
      'letter-spacing: 0.5px'
    ].join(';')
  };

  // Initialize sketch mode
  function init() {
    if (sketchState.container) return;

    // Capture page background first (before adding overlay)
    if (typeof html2canvas !== 'undefined') {
      console.log('[DevTool] Capturing page background...');
      html2canvas(document.body, {
        scale: 1,
        useCORS: true,
        allowTaint: true,
        logging: false,
        width: window.innerWidth,
        height: window.innerHeight,
        x: window.scrollX,
        y: window.scrollY
      }).then(function(bgCanvas) {
        sketchState.backgroundImage = bgCanvas;
        console.log('[DevTool] Background captured');
        initSketchUI();
      }).catch(function(err) {
        console.warn('[DevTool] Background capture failed:', err);
        initSketchUI();
      });
    } else {
      console.warn('[DevTool] html2canvas not available, no background capture');
      initSketchUI();
    }
  }

  function initSketchUI() {
    // Create container
    var container = document.createElement('div');
    container.id = '__devtool-sketch';
    container.style.cssText = STYLES.container;
    sketchState.container = container;

    // Create canvas
    var canvas = document.createElement('canvas');
    canvas.id = '__devtool-sketch-canvas';
    canvas.style.cssText = STYLES.canvas;
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
    sketchState.canvas = canvas;
    sketchState.ctx = canvas.getContext('2d');
    container.appendChild(canvas);

    // Create toolbar
    createToolbar(container);

    // Create sidebar
    createSidebar(container);

    // Create action bar
    createActionBar(container);

    // Create close button
    var closeBtn = document.createElement('button');
    closeBtn.style.cssText = STYLES.closeButton;
    closeBtn.innerHTML = '&times;';
    closeBtn.onclick = close;
    container.appendChild(closeBtn);

    // Setup event listeners
    setupEventListeners();

    // Add to document
    document.body.appendChild(container);
    sketchState.isActive = true;

    // Initial render
    render();

    console.log('[DevTool] Sketch mode activated');
  }

  function createToolbar(container) {
    var toolbar = document.createElement('div');
    toolbar.id = '__devtool-sketch-toolbar';
    toolbar.style.cssText = STYLES.toolbar;

    var toolGroups = [
      ['select', 'marker'],
      ['rectangle', 'ellipse', 'arrow'],
      ['freedraw', 'text'],
      ['eraser']
    ];

    toolGroups.forEach(function(group, groupIndex) {
      if (groupIndex > 0) {
        var sep = document.createElement('div');
        sep.style.cssText = STYLES.separator;
        toolbar.appendChild(sep);
      }

      group.forEach(function(toolId) {
        var tool = TOOLS[toolId];
        var btn = document.createElement('button');
        btn.id = '__devtool-tool-' + toolId;
        btn.style.cssText = STYLES.toolButton;
        btn.title = tool.name;
        btn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24"><path d="' + tool.icon + '" fill="currentColor"/></svg>';

        btn.onclick = function() {
          setTool(toolId);
        };

        if (toolId === sketchState.tool) {
          btn.style.cssText += ';' + STYLES.toolButtonActive;
        }

        toolbar.appendChild(btn);
      });
    });

    // Voice button (if speech recognition is available)
    var voice = window.__devtool_voice;
    if (voice && voice.isSupported && voice.isSupported().any) {
      var voiceSep = document.createElement('div');
      voiceSep.style.cssText = STYLES.separator;
      toolbar.appendChild(voiceSep);

      var voiceBtn = document.createElement('button');
      voiceBtn.id = '__devtool-voice-btn';
      voiceBtn.style.cssText = STYLES.toolButton;
      voiceBtn.title = 'Voice Annotation (click to speak)';
      // Microphone icon
      voiceBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24"><path d="M12,2A3,3 0 0,1 15,5V11A3,3 0 0,1 12,14A3,3 0 0,1 9,11V5A3,3 0 0,1 12,2M19,11C19,14.53 16.39,17.44 13,17.93V21H11V17.93C7.61,17.44 5,14.53 5,11H7A5,5 0 0,0 12,16A5,5 0 0,0 17,11H19Z" fill="currentColor"/></svg>';

      sketchState.voiceBtn = voiceBtn;

      voiceBtn.onclick = function() {
        toggleVoiceAnnotation();
      };

      toolbar.appendChild(voiceBtn);
    }

    sketchState.toolbar = toolbar;
    container.appendChild(toolbar);
  }

  function createSidebar(container) {
    var sidebar = document.createElement('div');
    sidebar.id = '__devtool-sketch-sidebar';
    sidebar.style.cssText = STYLES.sidebar;

    // Stroke color
    var strokeGroup = document.createElement('div');
    strokeGroup.style.marginBottom = '12px';

    var strokeLabel = document.createElement('label');
    strokeLabel.style.cssText = STYLES.label;
    strokeLabel.textContent = 'Stroke';
    strokeGroup.appendChild(strokeLabel);

    var strokeRow = document.createElement('div');
    strokeRow.style.display = 'flex';
    strokeRow.style.alignItems = 'center';
    strokeRow.style.gap = '8px';

    var strokePicker = document.createElement('input');
    strokePicker.type = 'color';
    strokePicker.value = sketchState.strokeColor;
    strokePicker.style.cssText = STYLES.colorPicker;
    strokePicker.onchange = function(e) {
      sketchState.strokeColor = e.target.value;
      updateSelectedElements({ strokeColor: e.target.value });
    };
    strokeRow.appendChild(strokePicker);

    var strokeWidthInput = document.createElement('input');
    strokeWidthInput.type = 'number';
    strokeWidthInput.value = sketchState.strokeWidth;
    strokeWidthInput.min = 1;
    strokeWidthInput.max = 20;
    strokeWidthInput.style.cssText = STYLES.inputField;
    strokeWidthInput.style.width = '60px';
    strokeWidthInput.onchange = function(e) {
      sketchState.strokeWidth = parseInt(e.target.value, 10);
      updateSelectedElements({ strokeWidth: sketchState.strokeWidth });
    };
    strokeRow.appendChild(strokeWidthInput);

    strokeGroup.appendChild(strokeRow);
    sidebar.appendChild(strokeGroup);

    // Fill color
    var fillGroup = document.createElement('div');
    fillGroup.style.marginBottom = '12px';

    var fillLabel = document.createElement('label');
    fillLabel.style.cssText = STYLES.label;
    fillLabel.textContent = 'Fill';
    fillGroup.appendChild(fillLabel);

    var fillPicker = document.createElement('input');
    fillPicker.type = 'color';
    fillPicker.value = '#ffffff';
    fillPicker.style.cssText = STYLES.colorPicker;
    fillPicker.onchange = function(e) {
      sketchState.fillColor = e.target.value;
      updateSelectedElements({ fillColor: e.target.value });
    };
    fillGroup.appendChild(fillPicker);

    var noFillBtn = document.createElement('button');
    noFillBtn.textContent = 'No Fill';
    noFillBtn.style.cssText = STYLES.inputField;
    noFillBtn.style.width = 'auto';
    noFillBtn.style.marginLeft = '8px';
    noFillBtn.style.cursor = 'pointer';
    noFillBtn.onclick = function() {
      sketchState.fillColor = 'transparent';
      updateSelectedElements({ fillColor: 'transparent' });
    };
    fillGroup.appendChild(noFillBtn);

    sidebar.appendChild(fillGroup);

    // Roughness slider
    var roughnessGroup = document.createElement('div');
    roughnessGroup.style.marginBottom = '12px';

    var roughnessLabel = document.createElement('label');
    roughnessLabel.style.cssText = STYLES.label;
    roughnessLabel.textContent = 'Sketch Style';
    roughnessGroup.appendChild(roughnessLabel);

    var roughnessSlider = document.createElement('input');
    roughnessSlider.type = 'range';
    roughnessSlider.min = 0;
    roughnessSlider.max = 2;
    roughnessSlider.step = 0.5;
    roughnessSlider.value = sketchState.roughness;
    roughnessSlider.style.width = '100%';
    roughnessSlider.onchange = function(e) {
      sketchState.roughness = parseFloat(e.target.value);
      render();
    };
    roughnessGroup.appendChild(roughnessSlider);

    sidebar.appendChild(roughnessGroup);

    // Font size (for text tool)
    var fontGroup = document.createElement('div');

    var fontLabel = document.createElement('label');
    fontLabel.style.cssText = STYLES.label;
    fontLabel.textContent = 'Font Size';
    fontGroup.appendChild(fontLabel);

    var fontSizeInput = document.createElement('input');
    fontSizeInput.type = 'number';
    fontSizeInput.value = sketchState.fontSize;
    fontSizeInput.min = 8;
    fontSizeInput.max = 72;
    fontSizeInput.style.cssText = STYLES.inputField;
    fontSizeInput.onchange = function(e) {
      sketchState.fontSize = parseInt(e.target.value, 10);
      updateSelectedElements({ fontSize: sketchState.fontSize });
    };
    fontGroup.appendChild(fontSizeInput);

    sidebar.appendChild(fontGroup);

    container.appendChild(sidebar);
  }

  function createActionBar(container) {
    var actionBar = document.createElement('div');
    actionBar.id = '__devtool-sketch-actions';
    actionBar.style.cssText = STYLES.actionBar;

    var undoBtn = createActionButton('Undo', 'secondary', undo);
    var redoBtn = createActionButton('Redo', 'secondary', redo);
    var clearBtn = createActionButton('Clear', 'secondary', clearAll);
    var saveBtn = createActionButton('Done', 'primary', saveAndClose);

    actionBar.appendChild(undoBtn);
    actionBar.appendChild(redoBtn);
    actionBar.appendChild(clearBtn);
    actionBar.appendChild(saveBtn);

    container.appendChild(actionBar);
  }

  function createActionButton(text, type, onClick) {
    var btn = document.createElement('button');
    btn.textContent = text;
    btn.style.cssText = STYLES.actionButton + ';' + (type === 'primary' ? STYLES.primaryAction : STYLES.secondaryAction);
    btn.onclick = onClick;
    return btn;
  }

  function setupEventListeners() {
    var canvas = sketchState.canvas;

    // Use pointer events for stylus/pen support (Surface Pen, Apple Pencil, etc.)
    // Pointer events provide pressure, tilt, and unified mouse/touch/pen handling
    canvas.addEventListener('pointerdown', handlePointerDown);
    canvas.addEventListener('pointermove', handlePointerMove);
    canvas.addEventListener('pointerup', handlePointerUp);
    canvas.addEventListener('pointercancel', handlePointerUp);
    canvas.addEventListener('dblclick', handleDoubleClick);

    // Prevent default touch behaviors to enable smooth stylus drawing
    canvas.style.touchAction = 'none';

    // Keyboard shortcuts
    document.addEventListener('keydown', handleKeyDown);

    // Resize handler
    window.addEventListener('resize', handleResize);
  }

  function handlePointerDown(e) {
    // Capture pointer for smooth tracking even if cursor leaves canvas
    if (e.target.setPointerCapture) {
      e.target.setPointerCapture(e.pointerId);
    }

    var pos = getCanvasPos(e);
    sketchState.isDrawing = true;
    sketchState.startPoint = pos;

    if (sketchState.tool === 'select') {
      var hit = hitTest(pos);
      if (hit) {
        if (!sketchState.selectedElements.includes(hit)) {
          sketchState.selectedElements = [hit];
        }
        sketchState.isDragging = true;
        sketchState.dragStartPos = pos;
      } else {
        sketchState.selectedElements = [];
        sketchState.selectionBox = { x: pos.x, y: pos.y, width: 0, height: 0 };
      }
    } else if (sketchState.tool === 'eraser') {
      var hit = hitTest(pos);
      if (hit) {
        deleteElement(hit.id);
      }
    } else if (sketchState.tool === 'marker') {
      // Drop a numbered annotation marker immediately
      var marker = createElement('marker', {
        x: pos.x,
        y: pos.y,
        width: 32,
        height: 40,
        text: String(sketchState.annotationCounter++)
      });
      addElement(marker);
      sketchState.isDrawing = false;
    } else if (sketchState.tool === 'freedraw') {
      sketchState.currentElement = createElement('freedraw', {
        x: pos.x,
        y: pos.y,
        // Store pressure/tilt data per point for stylus support
        points: [{ x: 0, y: 0, pressure: pos.pressure, tiltX: pos.tiltX, tiltY: pos.tiltY }]
      });
    } else {
      sketchState.currentElement = createElement(sketchState.tool, {
        x: pos.x,
        y: pos.y
      });
    }

    render();
  }

  function handlePointerMove(e) {
    var pos = getCanvasPos(e);

    if (!sketchState.isDrawing) {
      // Update hover state
      sketchState.hoveredElement = hitTest(pos);
      updateCursor(pos);
      return;
    }

    if (sketchState.tool === 'select') {
      if (sketchState.isDragging && sketchState.selectedElements.length > 0) {
        var dx = pos.x - sketchState.dragStartPos.x;
        var dy = pos.y - sketchState.dragStartPos.y;

        sketchState.selectedElements.forEach(function(el) {
          el.x += dx;
          el.y += dy;
        });

        sketchState.dragStartPos = pos;
      } else if (sketchState.selectionBox) {
        sketchState.selectionBox.width = pos.x - sketchState.selectionBox.x;
        sketchState.selectionBox.height = pos.y - sketchState.selectionBox.y;
      }
    } else if (sketchState.tool === 'freedraw' && sketchState.currentElement) {
      // Include pressure/tilt data for each point (stylus support)
      sketchState.currentElement.points.push({
        x: pos.x - sketchState.currentElement.x,
        y: pos.y - sketchState.currentElement.y,
        pressure: pos.pressure,
        tiltX: pos.tiltX,
        tiltY: pos.tiltY
      });
    } else if (sketchState.currentElement) {
      sketchState.currentElement.width = pos.x - sketchState.startPoint.x;
      sketchState.currentElement.height = pos.y - sketchState.startPoint.y;
    }

    render();
  }

  function handlePointerUp(e) {
    // Release pointer capture
    if (e.target.releasePointerCapture && e.pointerId !== undefined) {
      try {
        e.target.releasePointerCapture(e.pointerId);
      } catch (err) {
        // Ignore - pointer may not have been captured
      }
    }

    var pos = getCanvasPos(e);

    if (sketchState.tool === 'select') {
      if (sketchState.selectionBox) {
        // Select elements within box
        var box = normalizeRect(sketchState.selectionBox);
        sketchState.selectedElements = sketchState.elements.filter(function(el) {
          return isElementInRect(el, box);
        });
        sketchState.selectionBox = null;
      }
      sketchState.isDragging = false;
    } else if (sketchState.currentElement) {
      if (sketchState.tool === 'freedraw') {
        if (sketchState.currentElement.points.length > 2) {
          addElement(sketchState.currentElement);
        }
      } else if (sketchState.tool === 'text') {
        showTextInput(pos);
      } else {
        // Normalize negative dimensions
        normalizeElement(sketchState.currentElement);
        if (Math.abs(sketchState.currentElement.width) > 5 || Math.abs(sketchState.currentElement.height) > 5) {
          // Set default content for wireframe elements
          if (sketchState.tool === 'button') {
            sketchState.currentElement.text = 'Button';
          } else if (sketchState.tool === 'input') {
            sketchState.currentElement.text = 'Input field';
          } else if (sketchState.tool === 'note') {
            sketchState.currentElement.text = 'Note...';
            sketchState.currentElement.fillColor = '#fff9c4';
          } else if (sketchState.tool === 'image') {
            sketchState.currentElement.text = 'Image';
          }
          addElement(sketchState.currentElement);
        }
      }
      sketchState.currentElement = null;
    }

    sketchState.isDrawing = false;
    render();
  }

  function handleDoubleClick(e) {
    var pos = getCanvasPos(e);
    var hit = hitTest(pos);

    if (hit && (hit.type === 'text' || hit.type === 'note' || hit.type === 'button' || hit.type === 'input')) {
      editElementText(hit, pos);
    }
  }

  function handleKeyDown(e) {
    if (!sketchState.isActive) return;

    // Don't capture keys when editing text
    if (document.activeElement && document.activeElement.tagName === 'INPUT') return;

    if (e.key === 'Escape') {
      close();
    } else if (e.key === 'Delete' || e.key === 'Backspace') {
      if (sketchState.selectedElements.length > 0) {
        sketchState.selectedElements.forEach(function(el) {
          deleteElement(el.id);
        });
        sketchState.selectedElements = [];
        render();
      }
    } else if (e.key === 'z' && (e.ctrlKey || e.metaKey)) {
      if (e.shiftKey) {
        redo();
      } else {
        undo();
      }
      e.preventDefault();
    } else if (e.key === 'y' && (e.ctrlKey || e.metaKey)) {
      redo();
      e.preventDefault();
    } else if (e.key === 'c' && (e.ctrlKey || e.metaKey)) {
      copySelection();
    } else if (e.key === 'v' && (e.ctrlKey || e.metaKey)) {
      pasteClipboard();
    } else if (e.key === 'a' && (e.ctrlKey || e.metaKey)) {
      sketchState.selectedElements = sketchState.elements.slice();
      render();
      e.preventDefault();
    }
  }

  function handleResize() {
    if (!sketchState.canvas) return;
    sketchState.canvas.width = window.innerWidth;
    sketchState.canvas.height = window.innerHeight;
    render();
  }

  // Helper functions
  function getCanvasPos(e) {
    var rect = sketchState.canvas.getBoundingClientRect();
    return {
      x: (e.clientX - rect.left) / sketchState.scale - sketchState.offsetX,
      y: (e.clientY - rect.top) / sketchState.scale - sketchState.offsetY,
      // Pointer event properties for stylus support
      // pressure: 0-1 (0.5 default for mouse, varies for pen/touch)
      // tiltX/tiltY: -90 to 90 degrees (0 for mouse)
      pressure: typeof e.pressure === 'number' ? e.pressure : 0.5,
      tiltX: typeof e.tiltX === 'number' ? e.tiltX : 0,
      tiltY: typeof e.tiltY === 'number' ? e.tiltY : 0,
      pointerType: e.pointerType || 'mouse'
    };
  }

  function setTool(toolId) {
    sketchState.tool = toolId;

    // Update toolbar UI
    Object.keys(TOOLS).forEach(function(id) {
      var btn = document.getElementById('__devtool-tool-' + id);
      if (btn) {
        btn.style.cssText = STYLES.toolButton;
        if (id === toolId) {
          btn.style.cssText += ';' + STYLES.toolButtonActive;
        }
      }
    });

    updateCursor();
  }

  function updateCursor(pos) {
    var cursor = 'crosshair';
    if (sketchState.tool === 'select') {
      cursor = 'default';
      if (sketchState.hoveredElement) {
        cursor = 'move';
      }
    } else if (sketchState.tool === 'text') {
      cursor = 'text';
    } else if (sketchState.tool === 'eraser') {
      cursor = 'pointer';
    }
    sketchState.canvas.style.cursor = cursor;
  }

  function hitTest(pos) {
    for (var i = sketchState.elements.length - 1; i >= 0; i--) {
      var el = sketchState.elements[i];
      if (isPointInElement(pos, el)) {
        return el;
      }
    }
    return null;
  }

  function isPointInElement(pos, el) {
    var margin = 5;

    if (el.type === 'freedraw') {
      for (var i = 0; i < el.points.length; i++) {
        var p = el.points[i];
        var dist = Math.hypot(pos.x - (el.x + p.x), pos.y - (el.y + p.y));
        if (dist < 10) return true;
      }
      return false;
    }

    if (el.type === 'line' || el.type === 'arrow') {
      return distToSegment(pos, { x: el.x, y: el.y }, { x: el.x + el.width, y: el.y + el.height }) < 10;
    }

    if (el.type === 'ellipse') {
      var cx = el.x + el.width / 2;
      var cy = el.y + el.height / 2;
      var rx = Math.abs(el.width / 2) + margin;
      var ry = Math.abs(el.height / 2) + margin;
      return Math.pow(pos.x - cx, 2) / Math.pow(rx, 2) + Math.pow(pos.y - cy, 2) / Math.pow(ry, 2) <= 1;
    }

    return pos.x >= el.x - margin &&
           pos.x <= el.x + el.width + margin &&
           pos.y >= el.y - margin &&
           pos.y <= el.y + el.height + margin;
  }

  function distToSegment(p, v, w) {
    var l2 = Math.pow(w.x - v.x, 2) + Math.pow(w.y - v.y, 2);
    if (l2 === 0) return Math.hypot(p.x - v.x, p.y - v.y);
    var t = Math.max(0, Math.min(1, ((p.x - v.x) * (w.x - v.x) + (p.y - v.y) * (w.y - v.y)) / l2));
    return Math.hypot(p.x - (v.x + t * (w.x - v.x)), p.y - (v.y + t * (w.y - v.y)));
  }

  function isElementInRect(el, rect) {
    return el.x >= rect.x && el.x + el.width <= rect.x + rect.width &&
           el.y >= rect.y && el.y + el.height <= rect.y + rect.height;
  }

  function normalizeRect(rect) {
    return {
      x: rect.width < 0 ? rect.x + rect.width : rect.x,
      y: rect.height < 0 ? rect.y + rect.height : rect.y,
      width: Math.abs(rect.width),
      height: Math.abs(rect.height)
    };
  }

  function normalizeElement(el) {
    if (el.width < 0) {
      el.x += el.width;
      el.width = Math.abs(el.width);
    }
    if (el.height < 0) {
      el.y += el.height;
      el.height = Math.abs(el.height);
    }
  }

  function addElement(el) {
    saveHistory();
    sketchState.elements.push(el);
  }

  function deleteElement(id) {
    saveHistory();
    sketchState.elements = sketchState.elements.filter(function(el) {
      return el.id !== id;
    });
  }

  function updateSelectedElements(props) {
    if (sketchState.selectedElements.length === 0) return;

    saveHistory();
    sketchState.selectedElements.forEach(function(el) {
      Object.keys(props).forEach(function(key) {
        el[key] = props[key];
      });
    });
    render();
  }

  function showTextInput(pos) {
    var input = document.createElement('input');
    input.type = 'text';
    input.style.cssText = [
      'position: absolute',
      'left: ' + pos.x + 'px',
      'top: ' + pos.y + 'px',
      'font-size: ' + sketchState.fontSize + 'px',
      'font-family: ' + sketchState.fontFamily,
      'border: 2px solid #667eea',
      'outline: none',
      'background: white',
      'padding: 4px 8px',
      'border-radius: 4px',
      'min-width: 100px'
    ].join(';');
    input.placeholder = 'Type text...';

    input.onblur = function() {
      if (input.value.trim()) {
        var textEl = createElement('text', {
          x: pos.x,
          y: pos.y,
          text: input.value.trim()
        });

        // Measure text dimensions
        var ctx = sketchState.ctx;
        ctx.font = textEl.fontSize + 'px ' + textEl.fontFamily;
        var metrics = ctx.measureText(textEl.text);
        textEl.width = metrics.width;
        textEl.height = textEl.fontSize;

        addElement(textEl);
        render();
      }
      sketchState.container.removeChild(input);
    };

    input.onkeydown = function(e) {
      if (e.key === 'Enter') {
        input.blur();
      } else if (e.key === 'Escape') {
        input.value = '';
        input.blur();
      }
    };

    sketchState.container.appendChild(input);
    input.focus();
  }

  function editElementText(el, pos) {
    var input = document.createElement('input');
    input.type = 'text';
    input.value = el.text;
    input.style.cssText = [
      'position: absolute',
      'left: ' + el.x + 'px',
      'top: ' + el.y + 'px',
      'width: ' + Math.max(el.width, 100) + 'px',
      'font-size: ' + el.fontSize + 'px',
      'font-family: ' + el.fontFamily,
      'border: 2px solid #667eea',
      'outline: none',
      'background: white',
      'padding: 4px 8px',
      'border-radius: 4px'
    ].join(';');

    input.onblur = function() {
      saveHistory();
      el.text = input.value;
      sketchState.container.removeChild(input);
      render();
    };

    input.onkeydown = function(e) {
      if (e.key === 'Enter') {
        input.blur();
      } else if (e.key === 'Escape') {
        input.value = el.text;
        input.blur();
      }
    };

    sketchState.container.appendChild(input);
    input.select();
  }

  // ============================================================================
  // VOICE ANNOTATION
  // ============================================================================

  function toggleVoiceAnnotation() {
    var voice = window.__devtool_voice;
    if (!voice) return;

    if (sketchState.isVoiceListening) {
      stopVoiceAnnotation();
    } else {
      startVoiceAnnotation();
    }
  }

  function startVoiceAnnotation() {
    var voice = window.__devtool_voice;
    if (!voice) return;

    // Initialize voice with callbacks
    voice.init({
      onResult: handleVoiceResult,
      onError: handleVoiceError,
      onStateChange: handleVoiceStateChange
    });

    // Default annotation position (center of canvas)
    sketchState.voiceAnnotationPos = {
      x: sketchState.canvas.width / 2 - 50,
      y: sketchState.canvas.height / 2
    };

    // Start listening in annotate mode
    voice.start('annotate', sketchState.voiceAnnotationPos);
    sketchState.isVoiceListening = true;
    updateVoiceButtonState();

    // Show voice indicator
    showVoiceIndicator();
  }

  function stopVoiceAnnotation() {
    var voice = window.__devtool_voice;
    if (!voice) return;

    voice.stop();
    sketchState.isVoiceListening = false;
    updateVoiceButtonState();
    hideVoiceIndicator();
  }

  function handleVoiceResult(result) {
    if (!result.isFinal) {
      // Show interim transcript in indicator
      updateVoiceIndicator(result.transcript);
      return;
    }

    // Final transcript - create text element
    if (result.transcript && result.transcript.trim()) {
      var pos = sketchState.voiceAnnotationPos || { x: 100, y: 100 };

      var textEl = createElement('text', {
        x: pos.x,
        y: pos.y,
        text: result.transcript.trim()
      });

      // Measure text dimensions
      var ctx = sketchState.ctx;
      ctx.font = textEl.fontSize + 'px ' + textEl.fontFamily;
      var metrics = ctx.measureText(textEl.text);
      textEl.width = metrics.width;
      textEl.height = textEl.fontSize;

      addElement(textEl);
      render();

      // Move next annotation position down
      sketchState.voiceAnnotationPos.y += textEl.fontSize + 10;
    }

    // Stop listening after annotation (non-continuous mode)
    stopVoiceAnnotation();
  }

  function handleVoiceError(error) {
    console.error('[DevTool Sketch] Voice error:', error);
    stopVoiceAnnotation();
    hideVoiceIndicator();

    // Show error briefly
    showVoiceIndicator('Error: ' + (error.message || error.error));
    setTimeout(hideVoiceIndicator, 3000);
  }

  function handleVoiceStateChange(state) {
    sketchState.isVoiceListening = state.listening;
    updateVoiceButtonState();

    if (!state.listening) {
      hideVoiceIndicator();
    }
  }

  function updateVoiceButtonState() {
    if (!sketchState.voiceBtn) return;

    if (sketchState.isVoiceListening) {
      sketchState.voiceBtn.style.cssText = STYLES.toolButton + ';' + STYLES.toolButtonActive + ';color:#e53935';
      sketchState.voiceBtn.title = 'Stop voice (listening...)';
    } else {
      sketchState.voiceBtn.style.cssText = STYLES.toolButton;
      sketchState.voiceBtn.title = 'Voice Annotation (click to speak)';
    }
  }

  function showVoiceIndicator(text) {
    var existing = document.getElementById('__devtool-voice-indicator');
    if (existing) {
      if (text) existing.textContent = text;
      return;
    }

    var indicator = document.createElement('div');
    indicator.id = '__devtool-voice-indicator';
    indicator.style.cssText = [
      'position: fixed',
      'top: 80px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: rgba(0,0,0,0.8)',
      'color: white',
      'padding: 12px 24px',
      'border-radius: 24px',
      'font-size: 14px',
      'z-index: 2147483647',
      'display: flex',
      'align-items: center',
      'gap: 10px',
      'animation: devtool-voice-pulse 1.5s infinite'
    ].join(';');

    // Add pulse animation
    var style = document.createElement('style');
    style.textContent = '@keyframes devtool-voice-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.7; } }';
    document.head.appendChild(style);

    // Microphone icon
    var icon = document.createElement('span');
    icon.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24"><path d="M12,2A3,3 0 0,1 15,5V11A3,3 0 0,1 12,14A3,3 0 0,1 9,11V5A3,3 0 0,1 12,2M19,11C19,14.53 16.39,17.44 13,17.93V21H11V17.93C7.61,17.44 5,14.53 5,11H7A5,5 0 0,0 12,16A5,5 0 0,0 17,11H19Z" fill="#e53935"/></svg>';
    indicator.appendChild(icon);

    var label = document.createElement('span');
    label.textContent = text || 'Listening...';
    indicator.appendChild(label);

    sketchState.container.appendChild(indicator);
  }

  function updateVoiceIndicator(text) {
    var indicator = document.getElementById('__devtool-voice-indicator');
    if (indicator) {
      var label = indicator.querySelector('span:last-child');
      if (label) label.textContent = text || 'Listening...';
    }
  }

  function hideVoiceIndicator() {
    var indicator = document.getElementById('__devtool-voice-indicator');
    if (indicator && indicator.parentNode) {
      indicator.parentNode.removeChild(indicator);
    }
  }

  // Listen for voice commands from voice module
  document.addEventListener('devtool-voice-command', function(e) {
    if (!sketchState.isActive) return;

    var cmd = e.detail;
    if (cmd.tool) {
      setTool(cmd.tool);
    } else if (cmd.action) {
      switch (cmd.action) {
        case 'undo': undo(); break;
        case 'redo': redo(); break;
        case 'save': saveAndSend(); break;
        case 'clear': clearAll(); break;
        case 'close': close(); break;
        case 'selectAll': selectAll(); break;
        case 'delete': deleteSelected(); break;
      }
    } else if (cmd.color) {
      sketchState.strokeColor = cmd.color;
      updateSelectedElements({ strokeColor: cmd.color });
    } else if (cmd.strokeWidth) {
      sketchState.strokeWidth = cmd.strokeWidth;
      updateSelectedElements({ strokeWidth: cmd.strokeWidth });
    }
    render();
  });

  function selectAll() {
    sketchState.selectedElements = sketchState.elements.slice();
    render();
  }

  function deleteSelected() {
    sketchState.selectedElements.forEach(function(el) {
      deleteElement(el.id);
    });
    sketchState.selectedElements = [];
    render();
  }

  // History management
  function saveHistory() {
    // Remove future history if we're not at the end
    sketchState.history = sketchState.history.slice(0, sketchState.historyIndex + 1);

    // Save current state
    sketchState.history.push(JSON.stringify(sketchState.elements));
    sketchState.historyIndex = sketchState.history.length - 1;

    // Limit history size
    if (sketchState.history.length > 50) {
      sketchState.history.shift();
      sketchState.historyIndex--;
    }
  }

  function undo() {
    if (sketchState.historyIndex > 0) {
      sketchState.historyIndex--;
      sketchState.elements = JSON.parse(sketchState.history[sketchState.historyIndex]);
      sketchState.selectedElements = [];
      render();
    }
  }

  function redo() {
    if (sketchState.historyIndex < sketchState.history.length - 1) {
      sketchState.historyIndex++;
      sketchState.elements = JSON.parse(sketchState.history[sketchState.historyIndex]);
      sketchState.selectedElements = [];
      render();
    }
  }

  // Clipboard
  function copySelection() {
    if (sketchState.selectedElements.length > 0) {
      sketchState.clipboard = JSON.stringify(sketchState.selectedElements);
    }
  }

  function pasteClipboard() {
    if (!sketchState.clipboard) return;

    saveHistory();
    var elements = JSON.parse(sketchState.clipboard);
    elements.forEach(function(el) {
      el.id = 'el_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9);
      el.x += 20;
      el.y += 20;
      sketchState.elements.push(el);
    });

    sketchState.selectedElements = elements;
    render();
  }

  function clearAll() {
    if (sketchState.elements.length > 0) {
      saveHistory();
      sketchState.elements = [];
      sketchState.selectedElements = [];
      render();
    }
  }

  // Render
  function render() {
    var ctx = sketchState.ctx;
    var canvas = sketchState.canvas;

    // Clear canvas
    ctx.clearRect(0, 0, canvas.width, canvas.height);

    // Draw background image if available, otherwise white fallback
    if (sketchState.backgroundImage) {
      ctx.drawImage(sketchState.backgroundImage, 0, 0, canvas.width, canvas.height);
      // Semi-transparent overlay so sketch elements are visible
      ctx.fillStyle = 'rgba(255, 255, 255, 0.4)';
      ctx.fillRect(0, 0, canvas.width, canvas.height);
    } else {
      // Fallback to semi-transparent white
      ctx.fillStyle = 'rgba(255, 255, 255, 0.5)';
      ctx.fillRect(0, 0, canvas.width, canvas.height);
    }

    // Draw grid (dots)
    ctx.fillStyle = '#999999';
    for (var x = 0; x < canvas.width; x += 20) {
      for (var y = 0; y < canvas.height; y += 20) {
        ctx.beginPath();
        ctx.arc(x, y, 1.5, 0, Math.PI * 2);
        ctx.fill();
      }
    }

    // Draw elements
    sketchState.elements.forEach(function(el) {
      drawElement(ctx, el);
    });

    // Draw current element being created
    if (sketchState.currentElement) {
      ctx.globalAlpha = 0.7;
      drawElement(ctx, sketchState.currentElement);
      ctx.globalAlpha = 1;
    }

    // Draw selection box
    if (sketchState.selectionBox) {
      var box = normalizeRect(sketchState.selectionBox);
      ctx.strokeStyle = '#667eea';
      ctx.lineWidth = 1;
      ctx.setLineDash([5, 5]);
      ctx.strokeRect(box.x, box.y, box.width, box.height);
      ctx.fillStyle = 'rgba(102, 126, 234, 0.1)';
      ctx.fillRect(box.x, box.y, box.width, box.height);
      ctx.setLineDash([]);
    }

    // Draw selection handles
    sketchState.selectedElements.forEach(function(el) {
      drawSelectionHandles(ctx, el);
    });
  }

  function drawElement(ctx, el) {
    ctx.strokeStyle = el.strokeColor;
    ctx.fillStyle = el.fillColor;
    ctx.lineWidth = el.strokeWidth;

    var oldRoughness = sketchState.roughness;
    sketchState.roughness = el.roughness;

    switch (el.type) {
      case 'rectangle':
        roughness.rect(ctx, el.x, el.y, el.width, el.height, el.fillColor);
        break;

      case 'ellipse':
        roughness.ellipse(ctx, el.x + el.width / 2, el.y + el.height / 2,
          Math.abs(el.width / 2), Math.abs(el.height / 2), el.fillColor);
        break;

      case 'line':
        roughness.line(ctx, el.x, el.y, el.x + el.width, el.y + el.height);
        break;

      case 'arrow':
        roughness.arrow(ctx, el.x, el.y, el.x + el.width, el.y + el.height);
        break;

      case 'marker':
        // Numbered annotation marker (pin shape)
        var pinX = el.x;
        var pinY = el.y;
        var pinR = 14;
        var pinH = 8;

        // Pin body (circle + point)
        ctx.beginPath();
        ctx.arc(pinX, pinY - pinR, pinR, 0, Math.PI * 2);
        ctx.fillStyle = '#ef4444';
        ctx.fill();
        ctx.strokeStyle = '#b91c1c';
        ctx.lineWidth = 2;
        ctx.stroke();

        // Pin point (triangle)
        ctx.beginPath();
        ctx.moveTo(pinX - 6, pinY - 4);
        ctx.lineTo(pinX, pinY + pinH);
        ctx.lineTo(pinX + 6, pinY - 4);
        ctx.closePath();
        ctx.fillStyle = '#ef4444';
        ctx.fill();

        // Number in center
        ctx.fillStyle = '#fff';
        ctx.font = 'bold 12px sans-serif';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText(el.text, pinX, pinY - pinR);
        ctx.textAlign = 'left';
        ctx.textBaseline = 'alphabetic';
        break;

      case 'freedraw':
        if (el.points.length < 2) break;
        // Check if this stroke has pressure data (stylus input)
        var hasPressure = el.points[0].pressure !== undefined && el.points[0].pressure !== 0.5;

        if (hasPressure) {
          // Pressure-sensitive rendering: draw segment by segment with varying width
          ctx.lineCap = 'round';
          ctx.lineJoin = 'round';

          for (var i = 1; i < el.points.length; i++) {
            var p0 = el.points[i - 1];
            var p1 = el.points[i];

            // Pressure affects stroke width: 0.3x to 2x base width
            // pressure=0 -> 0.3x, pressure=0.5 -> 1x, pressure=1 -> 2x
            var pressureFactor = 0.3 + (p1.pressure || 0.5) * 1.7;
            ctx.lineWidth = el.strokeWidth * pressureFactor;

            ctx.beginPath();
            ctx.moveTo(el.x + p0.x, el.y + p0.y);
            ctx.lineTo(el.x + p1.x, el.y + p1.y);
            ctx.stroke();
          }
        } else {
          // Standard rendering for mouse input (uniform width)
          ctx.beginPath();
          ctx.moveTo(el.x + el.points[0].x, el.y + el.points[0].y);
          for (var i = 1; i < el.points.length; i++) {
            ctx.lineTo(el.x + el.points[i].x, el.y + el.points[i].y);
          }
          ctx.stroke();
        }
        break;

      case 'text':
        ctx.font = el.fontSize + 'px ' + el.fontFamily;
        ctx.fillStyle = el.strokeColor;
        ctx.fillText(el.text, el.x, el.y + el.fontSize);
        break;

      case 'note':
        // Sticky note style
        ctx.fillStyle = el.fillColor || '#fff9c4';
        ctx.fillRect(el.x, el.y, el.width, el.height);
        ctx.strokeStyle = '#e0c000';
        ctx.lineWidth = 2;
        roughness.rect(ctx, el.x, el.y, el.width, el.height);
        // Text
        ctx.fillStyle = '#333';
        ctx.font = el.fontSize + 'px ' + el.fontFamily;
        wrapText(ctx, el.text, el.x + 10, el.y + 20, el.width - 20, el.fontSize + 4);
        break;

      case 'button':
        // Button wireframe
        ctx.fillStyle = '#f0f0f0';
        ctx.fillRect(el.x, el.y, el.width, el.height);
        roughness.rect(ctx, el.x, el.y, el.width, el.height);
        // Text centered
        ctx.fillStyle = '#333';
        ctx.font = 'bold ' + el.fontSize + 'px ' + el.fontFamily;
        ctx.textAlign = 'center';
        ctx.fillText(el.text, el.x + el.width / 2, el.y + el.height / 2 + el.fontSize / 3);
        ctx.textAlign = 'left';
        break;

      case 'input':
        // Input field wireframe
        ctx.fillStyle = '#fff';
        ctx.fillRect(el.x, el.y, el.width, el.height);
        roughness.rect(ctx, el.x, el.y, el.width, el.height);
        // Placeholder text
        ctx.fillStyle = '#999';
        ctx.font = el.fontSize + 'px ' + el.fontFamily;
        ctx.fillText(el.text, el.x + 10, el.y + el.height / 2 + el.fontSize / 3);
        break;

      case 'image':
        // Image placeholder
        ctx.fillStyle = '#f5f5f5';
        ctx.fillRect(el.x, el.y, el.width, el.height);
        roughness.rect(ctx, el.x, el.y, el.width, el.height);
        // X pattern
        ctx.strokeStyle = '#ccc';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(el.x, el.y);
        ctx.lineTo(el.x + el.width, el.y + el.height);
        ctx.moveTo(el.x + el.width, el.y);
        ctx.lineTo(el.x, el.y + el.height);
        ctx.stroke();
        // Icon/text
        ctx.fillStyle = '#999';
        ctx.font = el.fontSize + 'px ' + el.fontFamily;
        ctx.textAlign = 'center';
        ctx.fillText(el.text || 'Image', el.x + el.width / 2, el.y + el.height / 2);
        ctx.textAlign = 'left';
        break;
    }

    sketchState.roughness = oldRoughness;
  }

  function wrapText(ctx, text, x, y, maxWidth, lineHeight) {
    var words = text.split(' ');
    var line = '';

    for (var n = 0; n < words.length; n++) {
      var testLine = line + words[n] + ' ';
      var metrics = ctx.measureText(testLine);
      if (metrics.width > maxWidth && n > 0) {
        ctx.fillText(line, x, y);
        line = words[n] + ' ';
        y += lineHeight;
      } else {
        line = testLine;
      }
    }
    ctx.fillText(line, x, y);
  }

  function drawSelectionHandles(ctx, el) {
    ctx.strokeStyle = '#667eea';
    ctx.lineWidth = 2;
    ctx.setLineDash([]);

    // Bounding box
    ctx.strokeRect(el.x - 4, el.y - 4, el.width + 8, el.height + 8);

    // Handles
    ctx.fillStyle = '#fff';
    var handles = [
      { x: el.x - 4, y: el.y - 4 },
      { x: el.x + el.width, y: el.y - 4 },
      { x: el.x - 4, y: el.y + el.height },
      { x: el.x + el.width, y: el.y + el.height }
    ];

    handles.forEach(function(h) {
      ctx.fillRect(h.x - 4, h.y - 4, 8, 8);
      ctx.strokeRect(h.x - 4, h.y - 4, 8, 8);
    });
  }

  // Export/Save
  function toJSON() {
    return {
      version: 1,
      timestamp: Date.now(),
      elements: sketchState.elements,
      settings: {
        strokeColor: sketchState.strokeColor,
        fillColor: sketchState.fillColor,
        strokeWidth: sketchState.strokeWidth,
        roughness: sketchState.roughness,
        fontSize: sketchState.fontSize
      }
    };
  }

  function fromJSON(data) {
    if (data.version !== 1) {
      console.warn('[DevTool Sketch] Unknown version:', data.version);
    }
    sketchState.elements = data.elements || [];
    if (data.settings) {
      sketchState.strokeColor = data.settings.strokeColor || sketchState.strokeColor;
      sketchState.fillColor = data.settings.fillColor || sketchState.fillColor;
      sketchState.strokeWidth = data.settings.strokeWidth || sketchState.strokeWidth;
      sketchState.roughness = data.settings.roughness !== undefined ? data.settings.roughness : sketchState.roughness;
      sketchState.fontSize = data.settings.fontSize || sketchState.fontSize;
    }
    render();
  }

  function toDataURL() {
    return sketchState.canvas.toDataURL('image/png');
  }

  // Called when "Done" button is pressed - calls onSave callback if set
  function saveAndClose() {
    if (sketchState.elements.length === 0) {
      close();
      return;
    }

    var sketchData = toJSON();
    var imageData = toDataURL();

    // If onSave callback is set (by indicator), call it
    if (window.__devtool_sketch.onSave) {
      window.__devtool_sketch.onSave({
        sketch: sketchData,
        image: imageData,
        elementCount: sketchState.elements.length
      });
      window.__devtool_sketch.onSave = null; // Clear callback
    }

    close();
  }

  // Legacy: send directly to server (kept for backwards compatibility)
  function saveAndSend() {
    var sketchData = toJSON();
    var imageData = toDataURL();

    core.send('sketch', {
      timestamp: Date.now(),
      sketch: sketchData,
      image: imageData,
      element_count: sketchState.elements.length
    });

    close();
  }

  function close() {
    if (sketchState.container && sketchState.container.parentNode) {
      sketchState.container.parentNode.removeChild(sketchState.container);
    }

    document.removeEventListener('keydown', handleKeyDown);
    window.removeEventListener('resize', handleResize);

    sketchState.container = null;
    sketchState.canvas = null;
    sketchState.ctx = null;
    sketchState.backgroundImage = null;
    sketchState.toolbar = null;
    sketchState.isActive = false;

    console.log('[DevTool] Sketch mode deactivated');
  }

  function toggle() {
    if (sketchState.isActive) {
      close();
    } else {
      init();
    }
  }

  // Export sketch functions
  window.__devtool_sketch = {
    init: init,
    close: close,
    toggle: toggle,
    toJSON: toJSON,
    fromJSON: fromJSON,
    toDataURL: toDataURL,
    saveAndSend: saveAndSend,
    saveAndClose: saveAndClose,
    state: sketchState,
    setTool: setTool,
    undo: undo,
    redo: redo,
    clearAll: clearAll,
    onSave: null // Callback set by indicator when opening sketch
  };
})();
