import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { type Connection, type Project } from "../types";
import { TicketsPanel } from "./TicketsPanel";
import { TokensPanel } from "./TokensPanel";

export function Dashboard() {
  const [conn, setConn] = useState<Connection | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);

  const loadStatus = useCallback(async () => {
    const c = await api.get<Connection>("/v1/integrations/jira/status");
    setConn(c);
    if (c.connected) {
      try {
        const res = await api.get<{ projects: Project[] }>("/v1/integrations/jira/projects");
        setProjects(res.projects ?? []);
      } catch {
        setProjects([]);
      }
    }
  }, []);

  useEffect(() => {
    // Surface the OAuth callback outcome carried back in the URL.
    const params = new URLSearchParams(window.location.search);
    if (params.get("connected")) setNotice("Jira connected successfully.");
    if (params.get("connect_error")) setNotice("Jira connection failed. Please try again.");
    if (params.toString()) window.history.replaceState({}, "", window.location.pathname);

    loadStatus().finally(() => setLoading(false));
  }, [loadStatus]);

  async function connect() {
    const res = await api.get<{ auth_url: string }>("/v1/integrations/jira/connect");
    window.location.href = res.auth_url;
  }

  async function disconnect() {
    await api.del("/v1/integrations/jira");
    setProjects([]);
    await loadStatus();
    setNotice("Jira disconnected.");
  }

  if (loading) return <div className="muted">Loading…</div>;

  return (
    <div className="grid">
      {notice && <div className="alert info span2">{notice}</div>}

      <section className="card span2">
        <div className="row spread">
          <div>
            <h2>Jira integration</h2>
            <ConnectionBadge conn={conn} />
          </div>
          <div className="row gap">
            {conn?.connected ? (
              <button className="ghost" onClick={disconnect}>
                Disconnect
              </button>
            ) : (
              <button className="primary" onClick={connect}>
                Connect Jira
              </button>
            )}
            {conn && !conn.connected && conn.status === "needs_reauth" && (
              <button className="primary" onClick={connect}>
                Reconnect
              </button>
            )}
          </div>
        </div>
      </section>

      {conn?.connected ? (
        <TicketsPanel projects={projects} onReconnect={connect} />
      ) : (
        <section className="card span2 muted">
          Connect your Jira workspace to start reporting NHI findings.
        </section>
      )}

      <TokensPanel />
    </div>
  );
}

function ConnectionBadge({ conn }: { conn: Connection | null }) {
  if (!conn || !conn.connected) {
    const reauth = conn?.status === "needs_reauth";
    return <span className={`badge ${reauth ? "warn" : "muted"}`}>{reauth ? "Needs reconnect" : "Not connected"}</span>;
  }
  return <span className="badge ok">Connected</span>;
}
