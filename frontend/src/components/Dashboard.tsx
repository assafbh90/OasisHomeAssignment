import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { type Connection, type Project } from "../types";
import { AutomationPanel } from "./AutomationPanel";
import { IntegrationsTab } from "./IntegrationsTab";
import { TokensPanel } from "./TokensPanel";

type Tab = "integration" | "automations" | "tokens";

export function Dashboard() {
  const [conn, setConn] = useState<Connection | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState<Tab>("integration");

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
    } else {
      setProjects([]);
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
    await loadStatus();
    setNotice("Jira disconnected.");
  }

  if (loading) return <div className="muted">Loading…</div>;

  return (
    <div className="grid">
      {notice && <div className="alert info span2">{notice}</div>}

      <nav className="tabs span2">
        <button className={tab === "integration" ? "tab active" : "tab"} onClick={() => setTab("integration")}>
          Integration
        </button>
        <button className={tab === "automations" ? "tab active" : "tab"} onClick={() => setTab("automations")}>
          Automations
        </button>
        <button className={tab === "tokens" ? "tab active" : "tab"} onClick={() => setTab("tokens")}>
          Auth tokens
        </button>
      </nav>

      {tab === "integration" && (
        <IntegrationsTab conn={conn} projects={projects} onConnect={connect} onDisconnect={disconnect} />
      )}
      {tab === "automations" && <AutomationPanel projects={projects} hasJira={!!conn?.connected} />}
      {tab === "tokens" && <TokensPanel />}
    </div>
  );
}
