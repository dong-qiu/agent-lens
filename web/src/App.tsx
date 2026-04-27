import { useEffect, useState } from "react";
import { Timeline } from "./components/Timeline";

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

  return (
    <div className="min-h-screen bg-zinc-50 text-zinc-900">
      <header className="border-b border-zinc-200 bg-white">
        <div className="mx-auto flex max-w-4xl items-center justify-between px-6 py-4">
          <div>
            <h1 className="text-xl font-semibold">Agent Lens</h1>
            <p className="text-xs text-zinc-500">M1 timeline</p>
          </div>
          <input
            type="text"
            placeholder="session id"
            className="w-72 rounded border border-zinc-300 bg-white px-3 py-1.5 font-mono text-sm focus:border-zinc-500 focus:outline-none"
            value={sessionId}
            onChange={(e) => setSessionId(e.target.value)}
          />
        </div>
      </header>
      <main className="mx-auto max-w-4xl px-6 py-6">
        <Timeline sessionId={sessionId} />
      </main>
    </div>
  );
}
