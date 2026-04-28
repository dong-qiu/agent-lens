import { useEffect, useState } from "react";
import { Timeline } from "./components/Timeline";
import { SessionList } from "./components/SessionList";
import { CausalGraph } from "./components/CausalGraph";

type View = "timeline" | "graph";

function getInitialSession(): string {
  return new URLSearchParams(window.location.search).get("session") ?? "";
}

function getInitialView(): View {
  const v = new URLSearchParams(window.location.search).get("view");
  return v === "graph" ? "graph" : "timeline";
}

function getInitialLinked(): boolean {
  return new URLSearchParams(window.location.search).get("linked") === "1";
}

export default function App() {
  const [sessionId, setSessionId] = useState<string>(getInitialSession);
  const [view, setView] = useState<View>(getInitialView);
  const [linked, setLinked] = useState<boolean>(getInitialLinked);

  // Browser back/forward replays whatever URL was last pushed; mirror
  // it back into component state.
  useEffect(() => {
    const onPop = () => {
      setSessionId(getInitialSession());
      setView(getInitialView());
      setLinked(getInitialLinked());
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // pushState so each list↔timeline↔graph(↔linked) transition gets its
  // own history entry — otherwise back skips out of the app entirely.
  const navigate = (
    nextSession: string,
    nextView: View = "timeline",
    nextLinked: boolean = false,
  ) => {
    if (
      nextSession === sessionId &&
      nextView === view &&
      nextLinked === linked
    ) {
      return;
    }
    const params = new URLSearchParams(window.location.search);
    if (nextSession) params.set("session", nextSession);
    else params.delete("session");
    if (nextSession && nextView !== "timeline") params.set("view", nextView);
    else params.delete("view");
    // `linked` only meaningful in graph view.
    if (nextSession && nextView === "graph" && nextLinked) {
      params.set("linked", "1");
    } else {
      params.delete("linked");
    }
    const qs = params.toString();
    const url = `${window.location.pathname}${qs ? `?${qs}` : ""}`;
    window.history.pushState(null, "", url);
    setSessionId(nextSession);
    setView(nextView);
    setLinked(nextLinked);
  };

  const subtitle = sessionId
    ? view === "graph"
      ? linked
        ? "M2 cross-session graph"
        : "M2 causal graph"
      : "M1 timeline"
    : "M2 sessions";

  // Graph view needs more horizontal room (dagre lays a wide DAG; the
  // minimap also looks cramped at 4xl). Timeline and SessionList stay
  // at 4xl, where they were designed.
  const wrapperMaxW = sessionId && view === "graph" ? "max-w-6xl" : "max-w-4xl";

  return (
    <div className="min-h-screen bg-zinc-50 text-zinc-900">
      <header className="border-b border-zinc-200 bg-white">
        <div className={`mx-auto flex ${wrapperMaxW} items-center justify-between px-6 py-4`}>
          <div className="flex items-baseline gap-3">
            <button
              type="button"
              onClick={() => navigate("")}
              className="text-xl font-semibold hover:text-zinc-700"
            >
              Agent Lens
            </button>
            <p className="text-xs text-zinc-500">{subtitle}</p>
          </div>
          {sessionId && (
            <div className="flex items-center gap-4">
              {view === "graph" && (
                <label className="flex items-center gap-1.5 text-xs text-zinc-700 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={linked}
                    onChange={(e) => navigate(sessionId, "graph", e.target.checked)}
                    className="h-3.5 w-3.5 rounded border-zinc-300 accent-purple-600"
                  />
                  <span>Include linked sessions</span>
                </label>
              )}
              <ViewToggle
                view={view}
                onSelect={(v) => navigate(sessionId, v, v === "graph" ? linked : false)}
              />
              <button
                type="button"
                onClick={() => navigate("")}
                className="text-xs text-zinc-500 underline-offset-2 hover:underline"
              >
                ← all sessions
              </button>
            </div>
          )}
        </div>
      </header>
      <main className={`mx-auto ${wrapperMaxW} px-6 py-6`}>
        {sessionId ? (
          view === "graph" ? (
            <CausalGraph sessionId={sessionId} linked={linked} />
          ) : (
            <Timeline sessionId={sessionId} />
          )
        ) : (
          <SessionList onSelect={(id) => navigate(id, "timeline")} />
        )}
      </main>
    </div>
  );
}

function ViewToggle({
  view,
  onSelect,
}: {
  view: View;
  onSelect: (v: View) => void;
}) {
  return (
    <div
      className="inline-flex overflow-hidden rounded border border-zinc-300 bg-white text-xs"
      role="tablist"
    >
      <ToggleButton
        active={view === "timeline"}
        onClick={() => onSelect("timeline")}
      >
        Timeline
      </ToggleButton>
      <ToggleButton
        active={view === "graph"}
        onClick={() => onSelect("graph")}
      >
        Graph
      </ToggleButton>
    </div>
  );
}

function ToggleButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`px-3 py-1 transition ${
        active
          ? "bg-zinc-900 text-white"
          : "text-zinc-600 hover:bg-zinc-50"
      }`}
    >
      {children}
    </button>
  );
}
