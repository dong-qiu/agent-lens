export type ActorType = "HUMAN" | "AGENT" | "SYSTEM";

export type EventKind =
  | "PROMPT"
  | "THOUGHT"
  | "TOOL_CALL"
  | "TOOL_RESULT"
  | "CODE_CHANGE"
  | "COMMIT"
  | "PR"
  | "TEST_RUN"
  | "BUILD"
  | "DEPLOY"
  | "REVIEW"
  | "DECISION"
  | "PUSH";

export type Actor = {
  type: ActorType;
  id: string;
  model?: string | null;
};

export type Link = {
  fromEvent: string;
  toEvent: string;
  relation: string;
  confidence: number;
  inferredBy: string;
};

// Vendor-neutral token-counting shape. Mirrors the GraphQL `TokenUsage`
// type — see ADR 0002. Optional fields use null over absence because
// graphql-js renders missing/zero counters as null, not undefined.
export type TokenUsage = {
  vendor: string;
  model: string;
  serviceTier?: string | null;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number | null;
  cacheWrite5mTokens?: number | null;
  cacheWrite1hTokens?: number | null;
  webSearchCalls?: number | null;
  webFetchCalls?: number | null;
};

export type Event = {
  id: string;
  ts: string;
  sessionId: string;
  turnId?: string | null;
  actor: Actor;
  kind: EventKind;
  payload?: Record<string, unknown> | null;
  parents: string[];
  refs: string[];
  hash: string;
  prevHash?: string | null;
  links: Link[];
  usage?: TokenUsage | null;
  stopReason?: string | null;
};

export type EventsResponse = {
  events: Event[];
  sessionHead: string;
};

export type LinkedEventsResponse = {
  linkedEvents: Event[];
};

export type Session = {
  id: string;
  firstEventAt: string;
  lastEventAt: string;
  eventCount: number;
  totalUsage?: TokenUsage | null;
};

export type SessionsResponse = {
  sessions: Session[];
};
