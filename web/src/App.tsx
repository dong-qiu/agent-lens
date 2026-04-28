import { useEffect, useState } from "react";
import { Timeline } from "./components/Timeline";
import { SessionList } from "./components/SessionList";

function getInitialSession(): string {
  return new URLSearchParams(window.location.search).get("session") ?? "";
}

export default function App() {
  const [sessionId, setSessionId] = useState<string>(getInitialSession);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (sessionId) params.set("session", sessionId);
    else params.delete("session");
    const qs = params.toString();
    const url = `${window.location.pathname}${qs ? `?${qs}` : ""}`;
    window.history.replaceState(null, "", url);
  }, [sessionId]);

  // Keep view in sync with browser back/forward.
  useEffect(() => {
    const onPop = () => setSessionId(getInitialSession());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const subtitle = sessionId ? "M1 timeline" : "M2 sessions";

  return (
    <div className="min-h-screen bg-zinc-50 text-zinc-900">
      <header className="border-b border-zinc-200 bg-white">
        <div className="mx-auto flex max-w-4xl items-center justify-between px-6 py-4">
          <div className="flex items-baseline gap-3">
            <button
              type="button"
              onClick={() => setSessionId("")}
              className="text-xl font-semibold hover:text-zinc-700"
            >
              Agent Lens
            </button>
            <p className="text-xs text-zinc-500">{subtitle}</p>
          </div>
          {sessionId && (
            <button
              type="button"
              onClick={() => setSessionId("")}
              className="text-xs text-zinc-500 underline-offset-2 hover:underline"
            >
              ← all sessions
            </button>
          )}
        </div>
      </header>
      <main className="mx-auto max-w-4xl px-6 py-6">
        {sessionId ? (
          <Timeline sessionId={sessionId} />
        ) : (
          <SessionList onSelect={setSessionId} />
        )}
      </main>
    </div>
  );
}
