/**
 * Response Stream Panel
 *
 * A distinctive floating panel that displays real-time streaming responses
 * from the coding agent. Designed with a refined technical aesthetic.
 *
 * Integrates with __devtool API for streaming content and session tracking.
 */

import React, { useState, useEffect, useRef, useCallback } from 'react';

// ============================================================================
// TYPES
// ============================================================================

interface StreamMessage {
  content: string;
  timestamp: number;
  type: 'partial' | 'complete' | 'error' | 'system';
}

interface StreamConfig {
  maxMessages: number;
  maxContentLength: number;
  showTimestamps: boolean;
  autoScroll: boolean;
  animationSpeed: number;
}

// ============================================================================
// DEFAULT CONFIG
// ============================================================================

const DEFAULT_CONFIG: StreamConfig = {
  maxMessages: 50,
  maxContentLength: 10000,
  showTimestamps: true,
  autoScroll: true,
  animationSpeed: 30,
};

// ============================================================================
// HOOK: USE AGENT STREAM
// ============================================================================

function useAgentStream(config: StreamConfig = DEFAULT_CONFIG) {
  const [messages, setMessages] = useState<StreamMessage[]>([]);
  const [isStreaming, setIsStreaming] = useState(false);
  const [currentContent, setCurrentContent] = useState('');
  const [agentStatus, setAgentStatus] = useState<'idle' | 'thinking' | 'responding' | 'complete' | 'error'>('idle');
  const messageIdRef = useRef(0);
  const cleanupRef = useRef<(() => void) | null>(null);

  const clearStream = useCallback(() => {
    setMessages([]);
    setCurrentContent('');
    setIsStreaming(false);
    setAgentStatus('idle');
  }, []);

  const startStream = useCallback(() => {
    setIsStreaming(true);
    setAgentStatus('thinking');
    messageIdRef.current = 0;
  }, []);

  const updateContent = useCallback((content: string, isComplete = false) => {
    setCurrentContent(content);
    setAgentStatus(isComplete ? 'complete' : 'responding');
  }, []);

  const appendContent = useCallback((chunk: string, isComplete = false) => {
    setCurrentContent(prev => {
      const newContent = prev + chunk;
      return newContent.slice(-config.maxContentLength);
    });
    setAgentStatus(isComplete ? 'complete' : 'responding');
  }, [config.maxContentLength]);

  const endStream = useCallback(() => {
    setIsStreaming(false);
    if (currentContent) {
      const newMessage: StreamMessage = {
        content: currentContent,
        timestamp: Date.now(),
        type: 'complete',
      };
      setMessages(prev => [...prev.slice(-config.maxMessages + 1), newMessage]);
    }
    setAgentStatus('idle');
    setCurrentContent('');
  }, [currentContent, config.maxMessages]);

  const addSystemMessage = useCallback((content: string) => {
    const newMessage: StreamMessage = {
      content,
      timestamp: Date.now(),
      type: 'system',
    };
    setMessages(prev => [...prev.slice(-config.maxMessages + 1), newMessage]);
  }, [config.maxMessages]);

  const addErrorMessage = useCallback((content: string) => {
    const newMessage: StreamMessage = {
      content,
      timestamp: Date.now(),
      type: 'error',
    };
    setMessages(prev => [...prev.slice(-config.maxMessages + 1), newMessage]);
  }, [config.maxMessages]);

  useEffect(() => {
    if (typeof window === 'undefined') return;

    // Subscribe to agent messages via __devtool API
    const handleAgentMessage = (event: CustomEvent) => {
      const { type, content, chunk, complete } = event.detail || {};

      switch (type) {
        case 'start':
          startStream();
          break;
        case 'content':
          if (chunk) {
            appendContent(chunk, complete);
          } else if (content) {
            updateContent(content, complete);
          }
          break;
        case 'end':
          endStream();
          break;
        case 'system':
          addSystemMessage(content);
          break;
        case 'error':
          addErrorMessage(content);
          setAgentStatus('error');
          setIsStreaming(false);
          break;
      }
    };

    // Listen for events from __devtool
    window.addEventListener('__devtool:agent:message', handleAgentMessage as EventListener);

    // Also poll for stream state if __devtool API is available
    const pollInterval = setInterval(() => {
      if (window.__devtool?.streamState) {
        const state = window.__devtool.streamState();
        if (state.isActive !== isStreaming) {
          setIsStreaming(state.isActive);
        }
        if (state.status !== agentStatus) {
          setAgentStatus(state.status);
        }
        if (state.content !== currentContent) {
          setCurrentContent(state.content);
        }
      }
    }, 100);

    cleanupRef.current = () => {
      window.removeEventListener('__devtool:agent:message', handleAgentMessage as EventListener);
      clearInterval(pollInterval);
    };

    return () => {
      cleanupRef.current?.();
    };
  }, [isStreaming, agentStatus, currentContent, startStream, appendContent, updateContent, endStream, addSystemMessage, addErrorMessage]);

  return {
    messages,
    currentContent,
    isStreaming,
    agentStatus,
    clearStream,
    startStream,
    updateContent,
    appendContent,
    endStream,
    addSystemMessage,
    addErrorMessage,
  };
}

// ============================================================================
// COMPONENT: TYPING INDICATOR
// ============================================================================

interface TypingIndicatorProps {
  status: StreamPanelProps['status'];
  animationStyle: 'dots' | 'pulse' | 'waveform';
}

function TypingIndicator({ status, animationStyle }: TypingIndicatorProps) {
  const getStatusColor = () => {
    switch (status) {
      case 'thinking': return 'var(--agent-thinking)';
      case 'responding': return 'var(--agent-responding)';
      case 'error': return 'var(--agent-error)';
      default: return 'var(--agent-idle)';
    }
  };

  const getStatusText = () => {
    switch (status) {
      case 'thinking': return 'Thinking...';
      case 'responding': return 'Streaming...';
      case 'error': return 'Error';
      default: return 'Ready';
    }
  };

  if (animationStyle === 'waveform') {
    return (
      <div className="typing-indicator waveform">
        <div className="waveform-bar" style={{ animationDelay: '0ms' }} />
        <div className="waveform-bar" style={{ animationDelay: '120ms' }} />
        <div className="waveform-bar" style={{ animationDelay: '240ms' }} />
        <div className="waveform-bar" style={{ animationDelay: '160ms' }} />
        <div className="waveform-bar" style={{ animationDelay: '80ms' }} />
        <span className="status-text">{getStatusText()}</span>
      </div>
    );
  }

  if (animationStyle === 'pulse') {
    return (
      <div className="typing-indicator pulse">
        <div className="pulse-ring" />
        <div className="pulse-core" style={{ backgroundColor: getStatusColor() }} />
        <span className="status-text">{getStatusText()}</span>
      </div>
    );
  }

  // Default dots animation
  return (
    <div className="typing-indicator dots">
      <span className="dot" style={{ animationDelay: '0ms' }} />
      <span className="dot" style={{ animationDelay: '200ms' }} />
      <span className="dot" style={{ animationDelay: '400ms' }} />
      <span className="status-text">{getStatusText()}</span>
    </div>
  );
}

// ============================================================================
// COMPONENT: STREAM CONTENT
// ============================================================================

interface StreamContentProps {
  content: string;
  isStreaming: boolean;
  showLineNumbers: boolean;
  typewriterEffect: boolean;
  animationSpeed: number;
}

function StreamContent({ content, isStreaming, showLineNumbers, typewriterEffect, animationSpeed }: StreamContentProps) {
  const contentRef = useRef<HTMLDivElement>(null);
  const [displayedContent, setDisplayedContent] = useState('');

  useEffect(() => {
    if (!typewriterEffect) {
      setDisplayedContent(content);
      return;
    }

    if (isStreaming && content) {
      const charsToAdd = content.slice(displayedContent.length);
      const charsPerFrame = Math.max(1, Math.floor(animationSpeed / 10));
      let index = 0;

      const animate = () => {
        if (index < charsToAdd.length) {
          setDisplayedContent(content.slice(0, displayedContent.length + charsPerFrame));
          index += charsPerFrame;
          requestAnimationFrame(animate);
        } else {
          setDisplayedContent(content);
        }
      };

      const timeout = setTimeout(animate, 50);
      return () => clearTimeout(timeout);
    } else {
      setDisplayedContent(content);
    }
  }, [content, isStreaming, typewriterEffect, animationSpeed, displayedContent.length]);

  const lines = displayedContent.split('\n');

  return (
    <div className="stream-content" ref={contentRef}>
      {showLineNumbers && (
        <div className="line-numbers">
          {lines.map((_, i) => (
            <span key={i} className="line-number">{i + 1}</span>
          ))}
        </div>
      )}
      <div className="content-text">
        {lines.map((line, i) => (
          <div key={i} className="content-line">
            {line || <br />}
            {isStreaming && i === lines.length - 1 && (
              <span className="cursor-blink">_</span>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

// ============================================================================
// COMPONENT: COLLAPSE HANDLE
// ============================================================================

interface CollapseHandleProps {
  isExpanded: boolean;
  onToggle: () => void;
  position: 'top' | 'bottom';
}

function CollapseHandle({ isExpanded, onToggle, position }: CollapseHandleProps) {
  return (
    <button
      className={`collapse-handle ${position}`}
      onClick={onToggle}
      aria-label={isExpanded ? 'Collapse panel' : 'Expand panel'}
      title={isExpanded ? 'Collapse' : 'Expand'}
    >
      <svg
        className={`chevron ${isExpanded ? 'expanded' : ''}`}
        width="12"
        height="12"
        viewBox="0 0 12 12"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        <path
          d={position === 'top' ? 'M3 8L6 5L9 8' : 'M3 4L6 7L9 4'}
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </button>
  );
}

// ============================================================================
// COMPONENT: CLOSE BUTTON
// ============================================================================

interface CloseButtonProps {
  onClick: () => void;
  variant: 'icon' | 'text' | 'both';
}

function CloseButton({ onClick, variant }: CloseButtonProps) {
  if (variant === 'text' || variant === 'both') {
    return (
      <button className="close-button text" onClick={onClick}>
        {variant === 'both' && (
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
            <path
              d="M2 2L10 10M10 2L2 10"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
            />
          </svg>
        )}
        <span>Close</span>
      </button>
    );
  }

  return (
    <button className="close-button icon" onClick={onClick} aria-label="Close panel">
      <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
        <path
          d="M2 2L12 12M12 2L2 12"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
        />
      </svg>
    </button>
  );
}

// ============================================================================
// COMPONENT: PREVIOUS RESPONSES LIST
// ============================================================================

interface PreviousResponsesProps {
  messages: StreamMessage[];
  onSelect: (message: StreamMessage) => void;
  maxDisplayLength: number;
}

function PreviousResponses({ messages, onSelect, maxDisplayLength }: PreviousResponsesProps) {
  if (messages.length === 0) {
    return (
      <div className="previous-responses empty">
        <p className="empty-message">No previous responses</p>
      </div>
    );
  }

  return (
    <div className="previous-responses">
      <div className="responses-header">
        <span className="responses-count">{messages.length} response{messages.length !== 1 ? 's' : ''}</span>
      </div>
      <div className="responses-list">
        {messages.map((msg, index) => (
          <button
            key={index}
            className={`response-item ${msg.type}`}
            onClick={() => onSelect(msg)}
          >
            <div className="response-meta">
              <span className="response-time">
                {new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
              </span>
              <span className="response-type">{msg.type}</span>
            </div>
            <div className="response-preview">
              {msg.content.slice(0, maxDisplayLength).replace(/[#*`]/g, '')}
              {msg.content.length > maxDisplayLength && '...'}
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}

// ============================================================================
// MAIN COMPONENT: RESPONSE STREAM PANEL
// ============================================================================

export interface StreamPanelProps {
  /** Initial expansion state */
  defaultExpanded?: boolean;
  /** Panel position */
  position?: 'bottom-left' | 'bottom-right' | 'top-left' | 'top-right';
  /** Custom width */
  width?: number | string;
  /** Custom height when expanded */
  height?: number | string;
  /** Whether to show line numbers */
  showLineNumbers?: boolean;
  /** Whether to use typewriter effect */
  typewriterEffect?: boolean;
  /** Animation style for typing indicator */
  typingAnimationStyle?: 'dots' | 'pulse' | 'waveform';
  /** Maximum characters to show in current content */
  maxContentLength?: number;
  /** Maximum characters to show in previous response previews */
  maxPreviewLength?: number;
  /** Whether to auto-scroll to bottom */
  autoScroll?: boolean;
  /** Animation speed (chars per frame multiplier) */
  animationSpeed?: number;
  /** Callback when panel is opened */
  onOpen?: () => void;
  /** Callback when panel is closed */
  onClose?: () => void;
  /** Callback when streaming starts */
  onStreamStart?: () => void;
  /** Callback when streaming ends */
  onStreamEnd?: () => void;
  /** Custom class name */
  className?: string;
  /** Current agent status (controlled) */
  status?: 'idle' | 'thinking' | 'responding' | 'complete' | 'error';
  /** Whether panel is visible (controlled) */
  visible?: boolean;
}

export function ResponseStreamPanel({
  defaultExpanded = true,
  position = 'bottom-right',
  width = 400,
  height = 320,
  showLineNumbers = false,
  typewriterEffect = true,
  typingAnimationStyle = 'waveform',
  maxContentLength = 8000,
  maxPreviewLength = 150,
  autoScroll = true,
  animationSpeed = 30,
  onOpen,
  onClose,
  onStreamStart,
  onStreamEnd,
  className = '',
  status: externalStatus,
  visible: externalVisible,
}: StreamPanelProps) {
  const [isExpanded, setIsExpanded] = useState(defaultExpanded);
  const [isVisible, setIsVisible] = useState(true);
  const [showHistory, setShowHistory] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);

  const {
    messages,
    currentContent,
    isStreaming,
    agentStatus: internalStatus,
    clearStream,
  } = useAgentStream({
    maxMessages: 20,
    maxContentLength,
    showTimestamps: true,
    autoScroll,
    animationSpeed,
  });

  const status = externalStatus ?? internalStatus;
  const isActive = externalVisible ?? (isVisible || isStreaming);
  const canShow = isActive && !showHistory;

  // Handle expand/collapse
  const handleToggle = useCallback(() => {
    setIsExpanded(prev => !prev);
    if (isExpanded) {
      onClose?.();
    } else {
      onOpen?.();
    }
  }, [isExpanded, onOpen, onClose]);

  // Handle close
  const handleClose = useCallback(() => {
    setIsVisible(false);
    onClose?.();
  }, [onClose]);

  // Handle show history
  const handleShowHistory = useCallback(() => {
    setShowHistory(prev => !prev);
  }, []);

  // Handle streaming callbacks
  useEffect(() => {
    if (isStreaming) {
      onStreamStart?.();
    } else {
      onStreamEnd?.();
    }
  }, [isStreaming, onStreamStart, onStreamEnd]);

  // Click outside to close (when expanded)
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (panelRef.current && !panelRef.current.contains(event.target as Node)) {
        if (isExpanded && isStreaming) {
          // Don't auto-collapse while streaming
          return;
        }
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isExpanded, isStreaming]);

  // Keyboard shortcuts
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (showHistory) {
          setShowHistory(false);
        } else if (isExpanded) {
          handleClose();
        }
      }
      if (event.key === 'ArrowDown' && event.altKey && !isExpanded) {
        setIsExpanded(true);
        onOpen?.();
      }
      if (event.key === 'ArrowUp' && event.altKey && isExpanded) {
        setIsExpanded(false);
        onClose?.();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [isExpanded, showHistory, handleClose, onOpen, onClose]);

  if (!isActive) {
    return null;
  }

  const positionClasses = {
    'bottom-right': 'position-bottom-right',
    'bottom-left': 'position-bottom-left',
    'top-right': 'position-top-right',
    'top-left': 'position-top-left',
  };

  return (
    <div
      ref={panelRef}
      className={`response-stream-panel ${positionClasses[position]} ${isExpanded ? 'expanded' : 'collapsed'} ${className}`}
      style={{
        '--panel-width': typeof width === 'number' ? `${width}px` : width,
        '--panel-height': typeof height === 'number' ? `${height}px` : height,
      } as React.CSSProperties}
    >
      {/* Header */}
      <div className="panel-header">
        <div className="header-left">
          <div className={`status-indicator ${status}`}>
            <div className="status-dot" />
          </div>
          <span className="panel-title">Agent Response</span>
          {messages.length > 0 && (
            <button className="history-button" onClick={handleShowHistory}>
              <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
                <path
                  d="M7 13C10.3137 13 13 10.3137 13 7C13 3.68629 10.3137 1 7 1C3.68629 1 1 3.68629 1 7"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                />
                <path
                  d="M12 12L9 9"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
              <span>{messages.length}</span>
            </button>
          )}
        </div>
        <div className="header-right">
          <CloseButton onClick={handleClose} variant="icon" />
        </div>
      </div>

      {/* Content */}
      <div className="panel-content">
        {showHistory ? (
          <PreviousResponses
            messages={messages}
            onSelect={(msg) => {
              // Could implement full content view here
              setShowHistory(false);
            }}
            maxDisplayLength={maxPreviewLength}
          />
        ) : canShow ? (
          <>
            {/* Typing Indicator */}
            {(status === 'thinking' || status === 'responding') && (
              <div className="streaming-indicator">
                <TypingIndicator status={status} animationStyle={typingAnimationStyle} />
              </div>
            )}

            {/* Current Content */}
            <div className="content-wrapper">
              <StreamContent
                content={currentContent}
                isStreaming={isStreaming}
                showLineNumbers={showLineNumbers}
                typewriterEffect={typewriterEffect}
                animationSpeed={animationSpeed}
              />
            </div>

            {/* Streaming Controls */}
            {isStreaming && (
              <div className="streaming-controls">
                <button className="control-button stop" onClick={clearStream}>
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="2" y="2" width="8" height="8" rx="1" />
                  </svg>
                  Stop
                </button>
                <div className="content-length">
                  {currentContent.length}/{maxContentLength}
                </div>
              </div>
            )}
          </>
        ) : (
          <div className="idle-state">
            <div className="idle-icon">
              <svg width="24" height="24" viewBox="0 0 24 24" fill="none">
                <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="1.5" />
                <path
                  d="M8 12L11 15L16 9"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
            </div>
            <p className="idle-text">Waiting for agent response...</p>
          </div>
        )}
      </div>

      {/* Collapse Handle */}
      <CollapseHandle
        isExpanded={isExpanded}
        onToggle={handleToggle}
        position="top"
      />

      {/* Resize Handle (optional) */}
      <div className="resize-handle" />
    </div>
  );
}

// ============================================================================
// DEFAULT EXPORT
// ============================================================================

export default ResponseStreamPanel;
