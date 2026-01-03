/**
 * Response Stream Panel - Index
 *
 * Main export file for the Response Stream Panel component and utilities.
 */

// Component
export { ResponseStreamPanel, default } from './ResponseStreamPanel';
export type { StreamPanelProps } from './ResponseStreamPanel';

// Integration
export {
  getStreamManager,
  getSessionTracker,
  dispatchAgentEvent,
  dispatchStreamStart,
  dispatchStreamContent,
  dispatchStreamChunk,
  dispatchStreamEnd,
  dispatchSystemMessage,
  dispatchError,
  type AgentStreamState,
  type StreamEventData,
} from './agentStreamIntegration';

// Hooks
export { useAgentStream, useStreamPanel } from './useAgentStream';
export type {
  UseAgentStreamOptions,
  UseAgentStreamReturn,
  UseStreamPanelOptions,
  UseStreamPanelReturn,
} from './useAgentStream';
