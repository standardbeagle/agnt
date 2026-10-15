// Voice Transcription Module for DevTool
// Uses proxy-side Deepgram (primary) and Web Speech API (fallback)
// Audio is streamed to proxy via WebSocket, proxy forwards to Deepgram
// API key stays server-side, never exposed to browser

(function() {
  'use strict';

  var core = window.__devtool_core;
  var startSeq = 0;

  // Voice module state
  var voiceState = {
    isInitialized: false,
    isListening: false,
    provider: null,        // 'proxy' | 'webspeech' | null
    mode: 'annotate',      // 'annotate' | 'command' | 'describe'
    transcript: '',
    interimTranscript: '',
    targetPosition: null,  // Canvas position for annotation placement
    onResult: null,        // Callback for transcription results
    onError: null,         // Callback for errors
    onStateChange: null,   // Callback for listening state changes

    // Proxy-based voice (Deepgram via proxy)
    proxy: {
      pendingStart: 0,     // token of the start awaiting getUserMedia, 0 = none
      audioStream: null,
      audioContext: null,
      processor: null,
      source: null
    },

    // Web Speech API fallback
    webspeech: {
      recognition: null
    },

    // Configuration
    config: {
      language: 'en-US',
      interimResults: true,
      continuous: false,
      model: 'nova-3'
    }
  };

  // ============================================================================
  // PROVIDER DETECTION AND INITIALIZATION
  // ============================================================================

  function detectProvider() {
    // Primary: Use proxy WebSocket if core is available (Deepgram server-side)
    if (core && core.isConnected && core.isConnected()) {
      return 'proxy';
    }

    // Fallback: Web Speech API (Chrome/Edge only)
    var SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (SpeechRecognition) {
      return 'webspeech';
    }

    return null;
  }

  function init(options) {
    if (voiceState.isInitialized) {
      return { success: true, provider: voiceState.provider };
    }

    // Merge options
    if (options) {
      if (options.language) voiceState.config.language = options.language;
      if (options.model) voiceState.config.model = options.model;
      if (options.onResult) voiceState.onResult = options.onResult;
      if (options.onError) voiceState.onError = options.onError;
      if (options.onStateChange) voiceState.onStateChange = options.onStateChange;
    }

    voiceState.provider = detectProvider();

    if (!voiceState.provider) {
      var err = { error: 'No speech recognition available. Proxy not connected and Web Speech API unavailable.' };
      if (voiceState.onError) voiceState.onError(err);
      return err;
    }

    if (voiceState.provider === 'webspeech') {
      initWebSpeech();
    } else if (voiceState.provider === 'proxy') {
      initProxyVoice();
    }

    voiceState.isInitialized = true;
    console.log('[DevTool Voice] Initialized with provider:', voiceState.provider);

    return { success: true, provider: voiceState.provider };
  }

  // ============================================================================
  // PROXY-BASED VOICE (Deepgram via proxy WebSocket)
  // ============================================================================

  function initProxyVoice() {
    // Listen for voice messages from proxy
    if (core && core.onMessage) {
      core.onMessage(handleProxyMessage);
    }
  }

  function handleProxyMessage(msg) {
    if (!msg || !msg.type) return;

    switch (msg.type) {
      case 'voice_ready':
        console.log('[DevTool Voice] Proxy voice ready');
        voiceState.isListening = true;
        if (voiceState.onStateChange) voiceState.onStateChange({ listening: true });
        break;

      case 'voice_transcript':
        var transcript = msg.transcript;
        var isFinal = msg.is_final;

        if (isFinal) {
          voiceState.transcript = transcript;
          voiceState.interimTranscript = '';
        } else {
          voiceState.interimTranscript = transcript;
        }

        handleTranscript(transcript, isFinal, msg.confidence);
        break;

      case 'voice_stopped':
        voiceState.isListening = false;
        if (voiceState.onStateChange) voiceState.onStateChange({ listening: false });
        cleanupProxyVoice();
        break;

      case 'voice_error':
        console.error('[DevTool Voice] Proxy error:', msg.error);
        voiceState.isListening = false;
        if (voiceState.onStateChange) voiceState.onStateChange({ listening: false });
        if (voiceState.onError) voiceState.onError({ error: msg.error, message: msg.error });
        cleanupProxyVoice();
        break;

      case 'voice_speech_started':
        // Speech detected - could show visual feedback
        break;

      case 'voice_utterance_end':
        // Utterance ended
        break;
    }
  }

  function stopTracks(stream) {
    try {
      stream.getTracks().forEach(function(track) { track.stop(); });
    } catch (e) { /* already gone */ }
  }

  // Returns false when no capture was started (the caller reports why).
  // getUserMedia can wait on a permission prompt for as long as the user
  // likes, so a start is "pending" until it resolves. pendingStart holds the
  // token of the one start allowed to complete: stop() (via
  // cleanupProxyVoice) clears it, and a second start() while one is pending
  // is a no-op. A stream that resolves for a cancelled start has its tracks
  // stopped at once, so the microphone is never left open behind a stop.
  function startProxyVoice() {
    if (voiceState.proxy.pendingStart || voiceState.proxy.audioStream) {
      return true; // already starting or streaming
    }
    if (!navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== 'function') {
      // No mediaDevices outside a secure context (plain http on a non-loopback host).
      if (voiceState.onError) voiceState.onError({ error: 'mic-unavailable', message: 'Microphone capture is not available on this page (it needs https or localhost)' });
      return false;
    }
    var token = ++startSeq;
    var granted = null; // this start's stream, once it has one
    voiceState.proxy.pendingStart = token;
    navigator.mediaDevices.getUserMedia({ audio: true })
      .then(function(stream) {
        if (voiceState.proxy.pendingStart !== token) {
          stopTracks(stream); // stopped (or superseded) while the prompt was open
          return;
        }
        voiceState.proxy.pendingStart = 0;
        voiceState.proxy.audioStream = stream;
        granted = stream;

        // Tell proxy to start voice session
        if (core && core.send) {
          core.send('voice_start', {
            language: voiceState.config.language.split('-')[0],
            model: voiceState.config.model
          });
        }

        // Start audio processing and streaming
        startAudioStreaming(stream);
      })
      .catch(function(err) {
        var live = granted !== null && voiceState.proxy.audioStream === granted;
        if (voiceState.proxy.pendingStart !== token && !live) return; // cancelled: nothing to report
        if (live) {
          // Failed after the stream was granted and voice_start went out
          // (AudioContext setup): end the session and release the microphone.
          stopProxyVoice();
        } else {
          voiceState.proxy.pendingStart = 0;
        }
        var denied = err && (err.name === 'NotAllowedError' || err.name === 'SecurityError');
        console.error('[DevTool Voice] Voice capture failed:', err);
        if (voiceState.onError) {
          voiceState.onError(denied ?
            { error: 'mic-denied', message: 'Microphone access denied' } :
            { error: 'voice-start-failed', message: 'Voice capture failed: ' + ((err && err.message) || err) });
        }
      });
    return true;
  }

  function startAudioStreaming(stream) {
    // Create AudioContext to resample to 16kHz mono
    var audioContext = new (window.AudioContext || window.webkitAudioContext)({ sampleRate: 16000 });
    var source = audioContext.createMediaStreamSource(stream);
    var processor = audioContext.createScriptProcessor(4096, 1, 1);

    processor.onaudioprocess = function(e) {
      if (!voiceState.isListening) return;

      var inputData = e.inputBuffer.getChannelData(0);
      // Convert float32 to int16
      var output = new Int16Array(inputData.length);
      for (var i = 0; i < inputData.length; i++) {
        var s = Math.max(-1, Math.min(1, inputData[i]));
        output[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
      }

      // Send binary audio data via WebSocket
      if (core && core.sendBinary) {
        core.sendBinary(output.buffer);
      }
    };

    source.connect(processor);
    processor.connect(audioContext.destination);

    voiceState.proxy.audioContext = audioContext;
    voiceState.proxy.processor = processor;
    voiceState.proxy.source = source;

    // Set listening state (proxy will confirm with voice_ready)
    voiceState.isListening = true;
    if (voiceState.onStateChange) voiceState.onStateChange({ listening: true });
  }

  function stopProxyVoice() {
    // Tell proxy to stop voice session
    if (core && core.send) {
      core.send('voice_stop', {});
    }
    cleanupProxyVoice();
  }

  function cleanupProxyVoice() {
    // Cancels a start still waiting on getUserMedia (see startProxyVoice).
    voiceState.proxy.pendingStart = 0;

    if (voiceState.proxy.processor) {
      voiceState.proxy.processor.disconnect();
      voiceState.proxy.processor = null;
    }

    if (voiceState.proxy.source) {
      voiceState.proxy.source.disconnect();
      voiceState.proxy.source = null;
    }

    if (voiceState.proxy.audioContext) {
      voiceState.proxy.audioContext.close();
      voiceState.proxy.audioContext = null;
    }

    if (voiceState.proxy.audioStream) {
      voiceState.proxy.audioStream.getTracks().forEach(function(track) {
        track.stop();
      });
      voiceState.proxy.audioStream = null;
    }
  }

  // ============================================================================
  // WEB SPEECH API IMPLEMENTATION (Fallback)
  // ============================================================================

  function initWebSpeech() {
    var SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
    var recognition = new SpeechRecognition();

    recognition.continuous = voiceState.config.continuous;
    recognition.interimResults = voiceState.config.interimResults;
    recognition.lang = voiceState.config.language;
    recognition.maxAlternatives = 1;

    recognition.onstart = function() {
      voiceState.isListening = true;
      if (voiceState.onStateChange) voiceState.onStateChange({ listening: true });
    };

    recognition.onend = function() {
      voiceState.isListening = false;
      if (voiceState.onStateChange) voiceState.onStateChange({ listening: false });
    };

    recognition.onresult = function(event) {
      var result = event.results[event.results.length - 1];
      var transcript = result[0].transcript;
      var isFinal = result.isFinal;

      if (isFinal) {
        voiceState.transcript = transcript;
        voiceState.interimTranscript = '';
      } else {
        voiceState.interimTranscript = transcript;
      }

      handleTranscript(transcript, isFinal, result[0].confidence);
    };

    recognition.onerror = function(event) {
      console.error('[DevTool Voice] Web Speech error:', event.error);
      voiceState.isListening = false;
      if (voiceState.onStateChange) voiceState.onStateChange({ listening: false });
      if (voiceState.onError) voiceState.onError({ error: event.error, message: getWebSpeechErrorMessage(event.error) });
    };

    voiceState.webspeech.recognition = recognition;
  }

  function getWebSpeechErrorMessage(error) {
    var messages = {
      'no-speech': 'No speech detected. Please try again.',
      'aborted': 'Speech recognition aborted.',
      'audio-capture': 'Microphone not available. Check permissions.',
      'network': 'Network error. Check your connection.',
      'not-allowed': 'Microphone permission denied.',
      'service-not-allowed': 'Speech recognition service not allowed.',
      'bad-grammar': 'Grammar error in speech recognition.',
      'language-not-supported': 'Language not supported.'
    };
    return messages[error] || 'Unknown error: ' + error;
  }

  function startWebSpeech() {
    if (!voiceState.webspeech.recognition) {
      initWebSpeech();
    }
    try {
      voiceState.webspeech.recognition.start();
    } catch (e) {
      if (e.name !== 'InvalidStateError') {
        throw e;
      }
    }
  }

  function stopWebSpeech() {
    if (voiceState.webspeech.recognition) {
      voiceState.webspeech.recognition.stop();
    }
  }

  // ============================================================================
  // TRANSCRIPT HANDLING
  // ============================================================================

  function handleTranscript(transcript, isFinal, confidence) {
    if (voiceState.onResult) {
      voiceState.onResult({
        transcript: transcript,
        isFinal: isFinal,
        confidence: confidence,
        mode: voiceState.mode,
        position: voiceState.targetPosition
      });
    }

    // Only process final transcripts for actions
    if (!isFinal) return;

    if (voiceState.mode === 'command') {
      executeVoiceCommand(transcript);
    }

    // Log to proxy
    if (core && core.send) {
      core.send('custom_log', {
        level: 'info',
        message: '[Voice] ' + (voiceState.mode === 'command' ? 'Command: ' : 'Transcript: ') + transcript,
        data: { mode: voiceState.mode, provider: voiceState.provider, confidence: confidence }
      });
    }
  }

  // ============================================================================
  // VOICE COMMANDS
  // ============================================================================

  var commandPatterns = [
    // Tool selection
    { pattern: /\b(select|selection|pointer)\b/i, action: function() { return { tool: 'select' }; } },
    { pattern: /\b(rectangle|rect|box)\b/i, action: function() { return { tool: 'rectangle' }; } },
    { pattern: /\b(circle|ellipse|oval)\b/i, action: function() { return { tool: 'ellipse' }; } },
    { pattern: /\b(line)\b/i, action: function() { return { tool: 'line' }; } },
    { pattern: /\b(arrow)\b/i, action: function() { return { tool: 'arrow' }; } },
    { pattern: /\b(pen|draw|freedraw|freehand)\b/i, action: function() { return { tool: 'freedraw' }; } },
    { pattern: /\b(text|label)\b/i, action: function() { return { tool: 'text' }; } },
    { pattern: /\b(note|sticky|sticky note)\b/i, action: function() { return { tool: 'note' }; } },
    { pattern: /\b(button)\b/i, action: function() { return { tool: 'button' }; } },
    { pattern: /\b(input|input field|text field)\b/i, action: function() { return { tool: 'input' }; } },
    { pattern: /\b(image|picture|placeholder)\b/i, action: function() { return { tool: 'image' }; } },
    { pattern: /\b(eraser|erase|delete tool)\b/i, action: function() { return { tool: 'eraser' }; } },

    // Actions
    { pattern: /\b(undo)\b/i, action: function() { return { action: 'undo' }; } },
    { pattern: /\b(redo)\b/i, action: function() { return { action: 'redo' }; } },
    { pattern: /\b(save|send|done|finish)\b/i, action: function() { return { action: 'save' }; } },
    { pattern: /\b(clear|clear all|delete all)\b/i, action: function() { return { action: 'clear' }; } },
    { pattern: /\b(close|exit|cancel)\b/i, action: function() { return { action: 'close' }; } },
    { pattern: /\b(select all)\b/i, action: function() { return { action: 'selectAll' }; } },
    { pattern: /\b(delete|remove)\b/i, action: function() { return { action: 'delete' }; } },

    // Colors
    { pattern: /\bcolor\s+(red)\b/i, action: function() { return { color: '#e53935' }; } },
    { pattern: /\bcolor\s+(blue)\b/i, action: function() { return { color: '#1e88e5' }; } },
    { pattern: /\bcolor\s+(green)\b/i, action: function() { return { color: '#43a047' }; } },
    { pattern: /\bcolor\s+(black)\b/i, action: function() { return { color: '#1e1e1e' }; } },
    { pattern: /\bcolor\s+(white)\b/i, action: function() { return { color: '#ffffff' }; } },
    { pattern: /\bcolor\s+(yellow)\b/i, action: function() { return { color: '#fdd835' }; } },
    { pattern: /\bcolor\s+(orange)\b/i, action: function() { return { color: '#fb8c00' }; } },
    { pattern: /\bcolor\s+(purple)\b/i, action: function() { return { color: '#8e24aa' }; } },

    // Stroke width
    { pattern: /\b(thin|fine)\s*(stroke|line)?\b/i, action: function() { return { strokeWidth: 1 }; } },
    { pattern: /\b(medium)\s*(stroke|line)?\b/i, action: function() { return { strokeWidth: 2 }; } },
    { pattern: /\b(thick|bold)\s*(stroke|line)?\b/i, action: function() { return { strokeWidth: 4 }; } }
  ];

  function executeVoiceCommand(transcript) {
    var normalized = transcript.toLowerCase().trim();

    for (var i = 0; i < commandPatterns.length; i++) {
      var cmd = commandPatterns[i];
      if (cmd.pattern.test(normalized)) {
        var result = cmd.action();
        console.log('[DevTool Voice] Command recognized:', result);

        // Dispatch command event for sketch mode to handle
        var event = new CustomEvent('devtool-voice-command', { detail: result });
        document.dispatchEvent(event);

        return result;
      }
    }

    console.log('[DevTool Voice] No command recognized in:', transcript);
    return null;
  }

  // ============================================================================
  // PUBLIC API
  // ============================================================================

  function start(mode, position) {
    if (!voiceState.isInitialized) {
      var initResult = init();
      if (initResult.error) return initResult;
    }

    voiceState.mode = mode || 'annotate';
    voiceState.targetPosition = position || null;
    voiceState.transcript = '';
    voiceState.interimTranscript = '';

    if (voiceState.provider === 'proxy') {
      if (!startProxyVoice()) {
        return { error: 'Microphone capture is not available on this page (it needs https or localhost)' };
      }
    } else if (voiceState.provider === 'webspeech') {
      startWebSpeech();
    }

    return { success: true, provider: voiceState.provider, mode: voiceState.mode };
  }

  function stop() {
    if (voiceState.provider === 'proxy') {
      stopProxyVoice();
    } else if (voiceState.provider === 'webspeech') {
      stopWebSpeech();
    }

    voiceState.isListening = false;
    if (voiceState.onStateChange) voiceState.onStateChange({ listening: false });

    return { success: true };
  }

  function toggle(mode, position) {
    // A start waiting on the microphone prompt counts as on: the second
    // press cancels it rather than starting a second capture.
    if (voiceState.isListening || voiceState.proxy.pendingStart) {
      return stop();
    } else {
      return start(mode, position);
    }
  }

  function setMode(mode) {
    voiceState.mode = mode;
    return { mode: mode };
  }

  function getStatus() {
    return {
      isInitialized: voiceState.isInitialized,
      isListening: voiceState.isListening,
      provider: voiceState.provider,
      mode: voiceState.mode,
      transcript: voiceState.transcript,
      interimTranscript: voiceState.interimTranscript
    };
  }

  function configure(options) {
    if (options.language) voiceState.config.language = options.language;
    if (options.model) voiceState.config.model = options.model;
    if (options.continuous !== undefined) voiceState.config.continuous = options.continuous;
    if (options.interimResults !== undefined) voiceState.config.interimResults = options.interimResults;
    if (options.onResult) voiceState.onResult = options.onResult;
    if (options.onError) voiceState.onError = options.onError;
    if (options.onStateChange) voiceState.onStateChange = options.onStateChange;
    return { success: true, config: voiceState.config };
  }

  function isSupported() {
    var hasProxy = core && core.isConnected && core.isConnected();
    var SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
    return {
      proxy: hasProxy,
      webspeech: !!SpeechRecognition,
      any: hasProxy || !!SpeechRecognition
    };
  }

  // ============================================================================
  // EXPOSE MODULE
  // ============================================================================

  window.__devtool_voice = {
    init: init,
    start: start,
    stop: stop,
    toggle: toggle,
    setMode: setMode,
    getStatus: getStatus,
    configure: configure,
    isSupported: isSupported,
    // For programmatic access
    executeCommand: executeVoiceCommand
  };

})();
