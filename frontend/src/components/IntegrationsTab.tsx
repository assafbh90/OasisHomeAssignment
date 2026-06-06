import { useState } from "react";
import { type Connection, type Project } from "../types";
import { TicketsPanel } from "./TicketsPanel";

// Supported integrations. Only Jira today, but the tab is structured so adding
// another provider is just another entry here.
const SUPPORTED = [
  { id: "jira", name: "Jira", blurb: "File NHI findings into your Jira Cloud projects." },
] as const;

// IntegrationsTab shows each supported integration as a connect card until it's
// connected; once connected, the integration becomes a sub-tab whose content is
// the "Report NHI finding" form + recent tickets.
export function IntegrationsTab({
  conn,
  projects,
  onConnect,
  onDisconnect,
}: {
  conn: Connection | null;
  projects: Project[];
  onConnect: () => void;
  onDisconnect: () => void;
}) {
  const connected = !!conn?.connected;
  const needsReauth = conn?.status === "needs_reauth";
  const [active, setActive] = useState<string>(SUPPORTED[0].id);

  // Not connected yet → present each integration as a connect card.
  if (!connected) {
    return (
      <>
        {SUPPORTED.map((it) => (
          <section key={it.id} className="card span2">
            <div className="row spread">
              <div>
                <h2>{it.name}</h2>
                <p className="muted">{it.blurb}</p>
              </div>
              <button className="primary" onClick={onConnect}>
                {needsReauth ? `Reconnect ${it.name}` : `Connect ${it.name}`}
              </button>
            </div>
            {needsReauth && (
              <div className="alert warn">Your {it.name} connection expired. Reconnect to continue.</div>
            )}
          </section>
        ))}
      </>
    );
  }

  // Connected → a sub-tab per connected integration; the active one shows its
  // finding form. (Only Jira can be connected today, so there is one sub-tab.)
  const connectedItems = SUPPORTED; // all connected providers; Jira-only for now
  const current = connectedItems.find((it) => it.id === active) ?? connectedItems[0];

  return (
    <>
      <nav className="tabs span2">
        {connectedItems.map((it) => (
          <button
            key={it.id}
            className={current.id === it.id ? "tab active" : "tab"}
            onClick={() => setActive(it.id)}
          >
            {it.name}
          </button>
        ))}
      </nav>

      <section className="card span2">
        <div className="row spread">
          <div>
            <h2>{current.name}</h2>
            <span className="badge ok">Connected</span>
          </div>
          <button className="ghost" onClick={onDisconnect}>
            Disconnect
          </button>
        </div>
      </section>

      {current.id === "jira" && <TicketsPanel projects={projects} onReconnect={onConnect} />}
    </>
  );
}
