import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { ApiError, type CreatedTicket, type Project, type Ticket } from "../types";

export function TicketsPanel({ projects, onReconnect }: { projects: Project[]; onReconnect: () => void }) {
  const [projectKey, setProjectKey] = useState(projects[0]?.key ?? "");
  const [manual, setManual] = useState(projects.length === 0);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [recent, setRecent] = useState<Ticket[]>([]);
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [msg, setMsg] = useState<{ kind: "ok" | "error" | "warn"; text: string; url?: string } | null>(null);

  const loadRecent = useCallback(async (key: string) => {
    if (!key) {
      setRecent([]);
      return;
    }
    try {
      const res = await api.get<{ tickets: Ticket[] }>(`/v1/integrations/jira/tickets?project=${encodeURIComponent(key)}`);
      setRecent(res.tickets ?? []);
    } catch {
      setRecent([]);
    }
  }, []);

  useEffect(() => {
    loadRecent(projectKey);
  }, [projectKey, loadRecent]);

  // refresh reconciles the tenant's cache against Jira (drift), then reloads.
  async function refresh() {
    setMsg(null);
    setRefreshing(true);
    try {
      await api.post("/v1/integrations/jira/reconcile");
      await loadRecent(projectKey);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.code === "reauth_required") {
        setMsg({ kind: "warn", text: "Your Jira connection expired. Reconnect to continue." });
        onReconnect();
      } else {
        setMsg({ kind: "error", text: "Failed to refresh from Jira." });
      }
    } finally {
      setRefreshing(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMsg(null);
    if (!projectKey.trim() || !title.trim()) {
      setMsg({ kind: "error", text: "Project and title are required." });
      return;
    }
    setBusy(true);
    try {
      const ref = await api.post<CreatedTicket>("/v1/integrations/jira/tickets", {
        project_key: projectKey,
        title,
        description,
      });
      setMsg({ kind: "ok", text: `Created ${ref.issue_key}`, url: ref.url });
      setTitle("");
      setDescription("");
      await loadRecent(projectKey);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.code === "reauth_required") {
        setMsg({ kind: "warn", text: "Your Jira connection expired. Reconnect and try again." });
        onReconnect();
      } else if (err instanceof ApiError) {
        setMsg({ kind: "error", text: err.message });
      } else {
        setMsg({ kind: "error", text: "Failed to create ticket." });
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <section className="card">
        <h2>Report NHI finding</h2>
        <form onSubmit={submit}>
          <label>
            Project
            {manual || projects.length === 0 ? (
              <input
                placeholder="e.g. NHI"
                value={projectKey}
                onChange={(e) => setProjectKey(e.target.value.toUpperCase())}
              />
            ) : (
              <select value={projectKey} onChange={(e) => setProjectKey(e.target.value)}>
                {projects.map((p) => (
                  <option key={p.key} value={p.key}>
                    {p.name} ({p.key})
                  </option>
                ))}
              </select>
            )}
          </label>
          {projects.length > 0 && (
            <button type="button" className="link small" onClick={() => setManual((m) => !m)}>
              {manual ? "Pick from list" : "Enter key manually"}
            </button>
          )}
          <label>
            Title (summary)
            <input
              placeholder="Stale Service Account: svc-deploy-prod"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          <label>
            Description
            <textarea
              rows={5}
              placeholder="Details about the finding…"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          {msg && (
            <div className={`alert ${msg.kind === "ok" ? "info" : msg.kind}`}>
              {msg.text}{" "}
              {msg.url && (
                <a href={msg.url} target="_blank" rel="noreferrer">
                  Open in Jira ↗
                </a>
              )}
            </div>
          )}
          <button className="primary" disabled={busy} type="submit">
            {busy ? "Creating…" : "Create finding ticket"}
          </button>
        </form>
      </section>

      <section className="card">
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
          <h2>Recent tickets {projectKey && <span className="muted">· {projectKey}</span>}</h2>
          <button type="button" className="link small" onClick={refresh} disabled={refreshing || !projectKey}>
            {refreshing ? "Refreshing…" : "Refresh from Jira"}
          </button>
        </div>
        {recent.length === 0 ? (
          <p className="muted">No IdentityHub tickets for this project yet. Try “Refresh from Jira”.</p>
        ) : (
          <ul className="ticket-list">
            {recent.map((t) => (
              <li key={t.issue_key}>
                <a href={t.url} target="_blank" rel="noreferrer">
                  <span className="key">{t.issue_key}</span>
                  <span className="title">{t.title}</span>
                </a>
                <time>{new Date(t.created_at).toLocaleString()}</time>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
