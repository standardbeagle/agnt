/**
 * Agent Stream Integration
 *
 * Provides utilities for integrating the Response Stream Panel with
 * the __devtool API and the currentpage/session tracking system.
 */

// ============================================================================
// TYPES
// ============================================================================

export interface AgentStreamState {
  isActive: boolean;
  status: 'idle' | 'thinking' | 'responding' | 'complete' | 'error';
  content: string;
  sessionId?: string;
  timestamp: number;
}

export interface StreamEventData {
  type: 'start' | 'content' | 'chunk' | 'end' | 'system' | 'error';
  content?: string;
  chunk?: string;
  complete?: boolean;
  sessionId?: string;
  timestamp?: number;
}

// ============================================================================
// EXTEND __DEVTOOL API
// ============================================================================

declare global {
  interface Window {
    __devtool?: {
      // Existing API
      log: (message: string, level?: string, data?: unknown) => void;
      screenshot: (name?: string) => void;
      isConnected: () => boolean;

      // Existing interaction APIs
      interactions: {
        getHistory: () => unknown[];
        getLastClick: () => unknown;
        getLastClickContext: () => unknown;
      };

      // Existing mutation APIs
      mutations: {
        getHistory: () => unknown[];
        highlightRecent: (ms?: number) => void;
      };

      // Existing indicator/mode APIs
      indicator: {
        show: () => void;
        hide: () => void;
        toggle: () => void;
        togglePanel: () => void;
      };

      sketch: {
        open: () => void;
        close: () => void;
        toggle: () => void;
        save: () => void;
        toJSON: () => string;
        fromJSON: (json: string) => void;
      };

      design: {
        start: () => void;
        stop: () => void;
        selectElement: () => void;
        next: () => void;
        previous: () => void;
        addAlternative: () => void;
        chat: (message: string) => void;
      };

      // New Stream APIs
      stream: {
        /** Get current stream state */
        getState: () => AgentStreamState;

        /** Check if streaming is active */
        isActive: () => boolean;

        /** Get current content */
        getContent: () => string;

        /** Emit a stream event from the browser */
        emit: (event: StreamEventData) => void;

        /** Subscribe to stream events */
        on: (callback: (event: StreamEventData) => void) => () => void;

        /** Subscribe to state changes */
        onStateChange: (callback: (state: AgentStreamState) => void) => () => void;

        /** Clear current stream */
        clear: () => void;

        /** Start a new stream session */
        start: (sessionId?: string) => void;

        /** End current stream */
        end: () => void;

        /** Add content chunk */
        addChunk: (chunk: string, isComplete?: boolean) => void;

        /** Update full content */
        updateContent: (content: string, isComplete?: boolean) => void;

        /** Add system message */
        addSystemMessage: (message: string) => void;

        /** Add error message */
        addError: (error: string) => void;
      };

      /** Legacy stream state accessor (for backwards compatibility) */
      streamState?: () => Partial<AgentStreamState>;
    };
  }
}

// ============================================================================
// STREAM MANAGER (Singleton)
// ============================================================================

class StreamManager {
  private state: AgentStreamState = {
    isActive: false,
    status: 'idle',
    content: '',
    timestamp: 0,
  };

  private listeners: Set<(event: StreamEventData) => void> = new Set();
  private stateListeners: Set<(state: AgentStreamState) => void> = new Set();
  private contentBuffer: string = '';
  private sessionId: string | undefined;

  constructor() {
    this.registerWithDevtool();
  }

  private registerWithDevtool(): void {
    if (typeof window === 'undefined') return;

    // Initialize __devtool.stream if not already present
    if (window.__devtool) {
      window.__devtool.stream = {
        getState: () => ({ ...this.state }),
        isActive: () => this.state.isActive,
        getContent: () => this.state.content,
        emit: (event) => this.handleEvent(event),
        on: (callback) => {
          this.listeners.add(callback);
          return () => this.listeners.delete(callback);
        },
        onStateChange: (callback) => {
          this.stateListeners.add(callback);
          return () => this.stateListeners.delete(callback);
        },
        clear: () => this.clear(),
        start: (sessionId) => this.start(sessionId),
        end: () => this.end(),
        addChunk: (chunk, isComplete) => this.addChunk(chunk, isComplete),
        updateContent: (content, isComplete) => this.updateContent(content, isComplete),
        addSystemMessage: (message) => this.addSystemMessage(message),
        addError: (error) => this.addError(error),
      };
    }
  }

  private updateState(updates: Partial<AgentStreamState>): void {
    this.state = { ...this.state, ...updates, timestamp: Date.now() };
    this.stateListeners.forEach(callback => callback(this.state));
  }

  private notifyListeners(event: StreamEventData): void {
    this.listeners.forEach(callback => callback(event));
  }

  // Public API
  start(sessionId?: string): void {
    this.sessionId = sessionId;
    this.contentBuffer = '';
    this.updateState({
      isActive: true,
      status: 'thinking',
      content: '',
    });
    this.notifyListeners({ type: 'start', sessionId });
  }

  addChunk(chunk: string, isComplete = false): void {
    if (!this.state.isActive) return;

    this.contentBuffer += chunk;
    this.updateState({
      status: isComplete ? 'complete' : 'responding',
      content: this.contentBuffer,
    });
    this.notifyListeners({
      type: 'chunk',
      chunk,
      complete: isComplete,
      sessionId: this.sessionId,
    });
  }

  updateContent(content: string, isComplete = false): void {
    if (!this.state.isActive) return;

    this.contentBuffer = content;
    this.updateState({
      status: isComplete ? 'complete' : 'responding',
      content,
    });
    this.notifyListeners({
      type: 'content',
      content,
      complete: isComplete,
      sessionId: this.sessionId,
    });
  }

  end(): void {
    if (!this.state.isActive) return;

    this.updateState({
      isActive: false,
      status: 'complete',
    });
    this.notifyListeners({ type: 'end', sessionId: this.sessionId });
    this.sessionId = undefined;
  }

  addSystemMessage(message: string): void {
    this.notifyListeners({ type: 'system', content: message, sessionId: this.sessionId });
  }

  addError(error: string): void {
    this.updateState({
      isActive: false,
      status: 'error',
    });
    this.notifyListeners({ type: 'error', content: error, sessionId: this.sessionId });
  }

  clear(): void {
    this.contentBuffer = '';
    this.updateState({
      isActive: false,
      status: 'idle',
      content: '',
    });
  }

  handleEvent(event: StreamEventData): void {
    switch (event.type) {
      case 'start':
        this.start(event.sessionId);
        break;
      case 'content':
        this.updateContent(event.content || '', event.complete);
        break;
      case 'chunk':
        this.addChunk(event.chunk || '', event.complete);
        break;
      case 'end':
        this.end();
        break;
      case 'system':
        this.addSystemMessage(event.content || '');
        break;
      case 'error':
        this.addError(event.content || 'Unknown error');
        break;
    }
  }

  getState(): AgentStreamState {
    return { ...this.state };
  }
}

// Singleton instance
let streamManager: StreamManager | null = null;

export function getStreamManager(): StreamManager {
  if (!streamManager) {
    streamManager = new StreamManager();
  }
  return streamManager;
}

// ============================================================================
// SESSION TRACKING INTEGRATION
// ============================================================================

export interface SessionInfo {
  id: string;
  startTime: number;
  url: string;
  lastActivity: number;
}

class SessionTracker {
  private currentSession: SessionInfo | null = null;
  private sessions: Map<string, SessionInfo> = new Map();
  private sessionListeners: Set<(session: SessionInfo | null) => void> = new Set();

  constructor() {
    this.setupPageTracking();
  }

  private setupPageTracking(): void {
    if (typeof window === 'undefined') return;

    // Track page visibility
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') {
        this.updateActivity();
      }
    });

    // Track user activity
    let activityTimeout: NodeJS.Timeout;
    const resetActivity = () => {
      this.updateActivity();
      clearTimeout(activityTimeout);
      activityTimeout = setTimeout(() => {
        // Session considered idle after 5 minutes
      }, 5 * 60 * 1000);
    };

    ['mousedown', 'keydown', 'scroll', 'touchstart'].forEach(event => {
      document.addEventListener(event, resetActivity, { passive: true });
    });

    // Initialize session on load
    this.startSession();
  }

  startSession(): SessionInfo {
    const session: SessionInfo = {
      id: this.generateSessionId(),
      startTime: Date.now(),
      url: window.location.href,
      lastActivity: Date.now(),
    };

    this.currentSession = session;
    this.sessions.set(session.id, session);
    this.notifySessionChange(session);

    return session;
  }

  private updateActivity(): void {
    if (this.currentSession) {
      this.currentSession.lastActivity = Date.now();
      this.sessions.set(this.currentSession.id, this.currentSession);
    }
  }

  private generateSessionId(): string {
    return `session-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
  }

  private notifySessionChange(session: SessionInfo | null): void {
    this.sessionListeners.forEach(callback => callback(session));
  }

  onSessionChange(callback: (session: SessionInfo | null) => void): () => void {
    this.sessionListeners.add(callback);
    return () => this.sessionListeners.delete(callback);
  }

  getCurrentSession(): SessionInfo | null {
    return this.currentSession;
  }

  getSessionHistory(): SessionInfo[] {
    return Array.from(this.sessions.values()).sort(
      (a, b) => b.startTime - a.startTime
    );
  }

  endSession(): void {
    this.currentSession = null;
    this.notifySessionChange(null);
  }
}

let sessionTracker: SessionTracker | null = null;

export function getSessionTracker(): SessionTracker {
  if (!sessionTracker) {
    sessionTracker = new SessionTracker();
  }
  return sessionTracker;
}

// ============================================================================
// EVENT DISPATCH UTILITIES
// ============================================================================

export function dispatchAgentEvent(event: StreamEventData): void {
  if (typeof window === 'undefined') return;

  const customEvent = new CustomEvent('__devtool:agent:message', {
    detail: event,
    bubbles: true,
  });

  window.dispatchEvent(customEvent);

  // Also notify stream manager
  getStreamManager().handleEvent(event);
}

export function dispatchStreamStart(sessionId?: string): void {
  dispatchAgentEvent({ type: 'start', sessionId });
}

export function dispatchStreamContent(content: string, isComplete = false): void {
  dispatchAgentEvent({ type: 'content', content, complete: isComplete });
}

export function dispatchStreamChunk(chunk: string, isComplete = false): void {
  dispatchAgentEvent({ type: 'chunk', chunk, complete: isComplete });
}

export function dispatchStreamEnd(): void {
  dispatchAgentEvent({ type: 'end' });
}

export function dispatchSystemMessage(message: string): void {
  dispatchAgentEvent({ type: 'system', content: message });
}

export function dispatchError(error: string): void {
  dispatchAgentEvent({ type: 'error', content: error });
}

// ============================================================================
// CONVENIENCE HOOKS
// ============================================================================

export { useAgentStream } from './ResponseStreamPanel';

// ============================================================================
// EXPORTS
// ============================================================================

export {
  StreamManager,
  SessionTracker,
};
