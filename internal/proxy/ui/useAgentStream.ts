/**
 * useAgentStream Hook
 *
 * A React hook for integrating with the agent streaming system.
 * Provides easy access to stream state and control functions.
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { StreamEventData, dispatchAgentEvent } from './agentStreamIntegration';

// ============================================================================
// TYPES
// ============================================================================

export interface UseAgentStreamOptions {
  /** Auto-connect on mount */
  autoConnect?: boolean;
  /** Maximum content length */
  maxContentLength?: number;
  /** Enable typing indicator */
  showIndicator?: boolean;
  /** Callback when streaming starts */
  onStart?: () => void;
  /** Callback when streaming ends */
  onEnd?: () => void;
  /** Callback when content updates */
  onContent?: (content: string) => void;
  /** Callback when error occurs */
  onError?: (error: string) => void;
}

export interface UseAgentStreamReturn {
  /** Current streaming state */
  isStreaming: boolean;
  /** Current content being streamed */
  content: string;
  /** Complete messages received */
  messages: string[];
  /** Current status */
  status: 'idle' | 'thinking' | 'responding' | 'complete' | 'error';
  /** Error message if any */
  error: string | null;
  /** Start a new stream */
  start: (sessionId?: string) => void;
  /** Add content to stream */
  addContent: (content: string, isComplete?: boolean) => void;
  /** Add a chunk to stream */
  addChunk: (chunk: string, isComplete?: boolean) => void;
  /** End the stream */
  end: () => void;
  /** Add a system message */
  addSystemMessage: (message: string) => void;
  /** Report an error */
  reportError: (error: string) => void;
  /** Clear all state */
  clear: () => void;
}

// ============================================================================
// HOOK IMPLEMENTATION
// ============================================================================

export function useAgentStream(options: UseAgentStreamOptions = {}): UseAgentStreamReturn {
  const {
    autoConnect = true,
    maxContentLength = 10000,
    onStart,
    onEnd,
    onContent,
    onError,
  } = options;

  const [isStreaming, setIsStreaming] = useState(false);
  const [content, setContent] = useState('');
  const [messages, setMessages] = useState<string[]>([]);
  const [status, setStatus] = useState<'idle' | 'thinking' | 'responding' | 'complete' | 'error'>('idle');
  const [error, setError] = useState<string | null>(null);
  const contentRef = useRef('');

  // Handle stream events
  const handleEvent = useCallback((event: StreamEventData) => {
    switch (event.type) {
      case 'start':
        setIsStreaming(true);
        setStatus('thinking');
        setContent('');
        contentRef.current = '';
        onStart?.();
        break;

      case 'content':
        contentRef.current = (event.content || '').slice(-maxContentLength);
        setContent(contentRef.current);
        setStatus(event.complete ? 'complete' : 'responding');
        onContent?.(contentRef.current);
        if (event.complete) {
          setMessages(prev => [...prev, contentRef.current]);
          setIsStreaming(false);
          setStatus('idle');
          onEnd?.();
        }
        break;

      case 'chunk':
        if (event.chunk) {
          contentRef.current = (contentRef.current + event.chunk).slice(-maxContentLength);
          setContent(contentRef.current);
          setStatus(event.complete ? 'complete' : 'responding');
          onContent?.(contentRef.current);
          if (event.complete) {
            setMessages(prev => [...prev, contentRef.current]);
            setIsStreaming(false);
            setStatus('idle');
            onEnd?.();
          }
        }
        break;

      case 'end':
        if (contentRef.current) {
          setMessages(prev => [...prev, contentRef.current]);
        }
        setIsStreaming(false);
        setStatus('idle');
        onEnd?.();
        break;

      case 'system':
        // System messages don't affect main content
        break;

      case 'error':
        setError(event.content || 'Unknown error');
        setStatus('error');
        setIsStreaming(false);
        onError?.(event.content || 'Unknown error');
        break;
    }
  }, [maxContentLength, onStart, onEnd, onContent, onError]);

  // Subscribe to events
  useEffect(() => {
    if (typeof window === 'undefined') return;

    const handleCustomEvent = (event: CustomEvent<StreamEventData>) => {
      handleEvent(event.detail);
    };

    window.addEventListener('__devtool:agent:message', handleCustomEvent as EventListener);

    // Also poll for __devtool.stream state
    let interval: NodeJS.Timeout;
    if (autoConnect) {
      interval = setInterval(() => {
        if (window.__devtool?.stream?.getState) {
          const state = window.__devtool.stream.getState();
          if (state.isActive !== isStreaming) {
            setIsStreaming(state.isActive);
          }
          if (state.status !== status) {
            setStatus(state.status);
          }
          if (state.content !== content) {
            setContent(state.content);
            contentRef.current = state.content;
          }
        }
      }, 100);
    }

    return () => {
      window.removeEventListener('__devtool:agent:message', handleCustomEvent as EventListener);
      clearInterval(interval);
    };
  }, [autoConnect, isStreaming, status, content, handleEvent]);

  // Control functions
  const start = useCallback((sessionId?: string) => {
    dispatchAgentEvent({ type: 'start', sessionId });
  }, []);

  const addContent = useCallback((newContent: string, isComplete = false) => {
    dispatchAgentEvent({ type: 'content', content: newContent, complete: isComplete });
  }, []);

  const addChunk = useCallback((chunk: string, isComplete = false) => {
    dispatchAgentEvent({ type: 'chunk', chunk, complete: isComplete });
  }, []);

  const end = useCallback(() => {
    dispatchAgentEvent({ type: 'end' });
  }, []);

  const addSystemMessage = useCallback((message: string) => {
    dispatchAgentEvent({ type: 'system', content: message });
  }, []);

  const reportError = useCallback((err: string) => {
    dispatchAgentEvent({ type: 'error', content: err });
  }, []);

  const clear = useCallback(() => {
    setContent('');
    setMessages([]);
    setError(null);
    setStatus('idle');
    contentRef.current = '';
    if (window.__devtool?.stream?.clear) {
      window.__devtool.stream.clear();
    }
  }, []);

  return {
    isStreaming,
    content,
    messages,
    status,
    error,
    start,
    addContent,
    addChunk,
    end,
    addSystemMessage,
    reportError,
    clear,
  };
}

// ============================================================================
// USE STREAM PANEL HOOK
// ============================================================================

export interface UseStreamPanelOptions extends UseAgentStreamOptions {
  /** Initial expanded state */
  defaultExpanded?: boolean;
  /** Panel position */
  position?: 'bottom-left' | 'bottom-right' | 'top-left' | 'top-right';
  /** Show line numbers */
  showLineNumbers?: boolean;
  /** Use typewriter effect */
  typewriterEffect?: boolean;
  /** Animation style */
  animationStyle?: 'dots' | 'pulse' | 'waveform';
}

export interface UseStreamPanelReturn extends UseAgentStreamReturn {
  /** Panel expanded state */
  isExpanded: boolean;
  /** Toggle panel expansion */
  toggleExpanded: () => void;
  /** Set panel expansion */
  setExpanded: (expanded: boolean) => void;
  /** Panel visible state */
  isVisible: boolean;
  /** Hide panel */
  hide: () => void;
  /** Show panel */
  show: () => void;
  /** Toggle visibility */
  toggleVisible: () => void;
}

export function useStreamPanel(options: UseStreamPanelOptions = {}): UseStreamPanelReturn {
  const {
    defaultExpanded = true,
    position = 'bottom-right',
    showLineNumbers = false,
    typewriterEffect = true,
    animationStyle = 'waveform',
    ...streamOptions
  } = options;

  const stream = useAgentStream(streamOptions);
  const [isExpanded, setIsExpanded] = useState(defaultExpanded);
  const [isVisible, setIsVisible] = useState(true);

  const toggleExpanded = useCallback(() => {
    setIsExpanded(prev => !prev);
  }, []);

  const hide = useCallback(() => {
    setIsVisible(false);
  }, []);

  const show = useCallback(() => {
    setIsVisible(true);
  }, []);

  const toggleVisible = useCallback(() => {
    setIsVisible(prev => !prev);
  }, []);

  return {
    ...stream,
    isExpanded,
    toggleExpanded,
    setExpanded: setIsExpanded,
    isVisible,
    hide,
    show,
    toggleVisible,
  };
}

// ============================================================================
// EXPORTS
// ============================================================================

export default useAgentStream;
