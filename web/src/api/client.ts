type GraphQLError = { message: string };

type GraphQLResponse<T> = {
  data?: T;
  errors?: GraphQLError[];
};

export async function gql<T>(
  query: string,
  variables?: Record<string, unknown>,
): Promise<T> {
  const res = await fetch("/v1/graphql", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ query, variables }),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const json = (await res.json()) as GraphQLResponse<T>;
  if (json.errors?.length) {
    throw new Error(json.errors.map((e) => e.message).join("; "));
  }
  if (json.data === undefined) throw new Error("empty data");
  return json.data;
}

export const sessionsQuery = `
  query Sessions($limit: Int, $since: Time) {
    sessions(limit: $limit, since: $since) {
      id
      firstEventAt
      lastEventAt
      eventCount
      totalUsage {
        vendor model serviceTier
        inputTokens outputTokens
        cacheReadTokens cacheWrite5mTokens cacheWrite1hTokens
        webSearchCalls webFetchCalls
      }
    }
  }
`;

export const eventsQuery = `
  query Events($sessionId: String!, $limit: Int) {
    events(sessionId: $sessionId, limit: $limit) {
      id
      ts
      sessionId
      turnId
      actor { type id model }
      kind
      payload
      parents
      refs
      hash
      prevHash
      links { fromEvent toEvent relation inferredBy }
      usage {
        vendor model serviceTier
        inputTokens outputTokens
        cacheReadTokens cacheWrite5mTokens cacheWrite1hTokens
        webSearchCalls webFetchCalls
      }
      stopReason
    }
    sessionHead(sessionId: $sessionId)
  }
`;

// linkedEventsQuery returns events from sessionId plus events from
// every session reachable via the linker's emitted links, up to depth
// hops. Used by the cross-session causal graph view.
export const linkedEventsQuery = `
  query LinkedEvents($sessionId: String!, $depth: Int, $perSessionLimit: Int) {
    linkedEvents(sessionId: $sessionId, depth: $depth, perSessionLimit: $perSessionLimit) {
      id
      ts
      sessionId
      turnId
      actor { type id model }
      kind
      payload
      parents
      refs
      hash
      prevHash
      links { fromEvent toEvent relation inferredBy }
      usage {
        vendor model serviceTier
        inputTokens outputTokens
        cacheReadTokens cacheWrite5mTokens cacheWrite1hTokens
        webSearchCalls webFetchCalls
      }
      stopReason
    }
  }
`;
