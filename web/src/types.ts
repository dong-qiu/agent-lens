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
  | "DECISION";

export type Actor = {
  type: ActorType;
  id: string;
  model?: string | null;
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
};

export type EventsResponse = {
  events: Event[];
  sessionHead: string;
};
