import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { ApiError, type Automation, type Project } from "../types";

export function AutomationPanel({ projects, hasJira }: { projects: Project[]; hasJira: boolean }) {
  const [items, setItems] = useState<Automation[]>([]);
  const [name, setName] = useState("");
  const [siteUrl, setSiteUrl] = useState("");
  const [projectKey, setProjectKey] = useState(projects[0]?.key ?? "");
  const [manual, setManual] = useState(projects.length === 0);
  const [intervalMin, setIntervalMin] = useState(60);
  const [msg, setMsg] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await api.get<{ automations: Automation[] }>("/v1/automations");
      setItems(res.automations ?? []);
    } catch {
      setItems([]);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setMsg(null);
    if (!name.trim() || !siteUrl.trim() || !projectKey.trim()) {
      setMsg({ kind: "error", text: "Name, site URL, and project are required." });
      return;
    }
    setBusy(true);
    try {
      await api.post<Automation>("/v1/automations", {
        name,
        site_url: siteUrl,
        project_key: projectKey,
        interval_seconds: intervalMin * 60,
      });
      setName("");
      setSiteUrl("");
      setMsg({ kind: "ok", text: "Automation created." });
      await load();
    } catch (err) {
      setMsg({ kind: "error", text: err instanceof ApiError ? err.message : "Failed to create automation." });
    } finally {
      setBusy(false);
    }
  }

  async function toggle(a: Automation) {
    await api.put(`/v1/automations/${a.id}`, {
      name: a.name,
      site_url: a.site_url,
      project_key: a.project_key,
      interval_seconds: a.interval_seconds,
      enabled: !a.enabled,
    });
    await load();
  }

  async function runNow(a: Automation) {
    await api.post(`/v1/automations/${a.id}/run`);
    await load();
  }

  async function remove(a: Automation) {
    await api.del(`/v1/automations/${a.id}`);
    await load();
  }

  return (
    <>
      <section className="card span2">
        <h2>New automation</h2>
        {!hasJira && (
          <div className="alert warn">
            Connect Jira first — automations file tickets using your Jira connection.
          </div>
        )}
        <form onSubmit={create}>
          <label>
            Name
            <input placeholder="Atlassian blog watcher" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label>
            Site URL
            <input placeholder="https://www.atlassian.com/blog" value={siteUrl} onChange={(e) => setSiteUrl(e.target.value)} />
          </label>
          <label>
            Project
            {manual || projects.length === 0 ? (
              <input placeholder="e.g. NHI" value={projectKey} onChange={(e) => setProjectKey(e.target.value.toUpperCase())} />
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
            Scan every (minutes)
            <input type="number" min={1} value={intervalMin} onChange={(e) => setIntervalMin(Number(e.target.value))} />
          </label>
          {msg && <div className={`alert ${msg.kind === "ok" ? "info" : "error"}`}>{msg.text}</div>}
          <button className="primary" type="submit" disabled={busy}>
            {busy ? "Creating…" : "Create automation"}
          </button>
        </form>
      </section>

      <section className="card span2">
        <h2>Automations</h2>
        {items.length === 0 ? (
          <p className="muted">No automations yet. Add one above to watch a blog and file a ticket per new post.</p>
        ) : (
          <ul className="auto-list">
            {items.map((a) => (
              <li key={a.id} className="auto-item">
                <div className="auto-main">
                  <div className="row gap wrap">
                    <span className="auto-name">{a.name}</span>
                    <AutomationStatus a={a} />
                    <span className="chip">{a.project_key}</span>
                  </div>
                  <a className="auto-url" href={a.site_url} target="_blank" rel="noreferrer">
                    {a.site_url} ↗
                  </a>
                  <div className="auto-meta muted small">
                    every {formatInterval(a.interval_seconds)}
                    {a.last_run_at ? ` · last run ${new Date(a.last_run_at).toLocaleString()}` : " · never run"}
                  </div>
                  {a.last_error && <div className="alert error small auto-error">⚠ {a.last_error}</div>}
                </div>
                <div className="auto-actions row gap">
                  <button className="link small" onClick={() => runNow(a)}>
                    Run now
                  </button>
                  <button className="link small" onClick={() => toggle(a)}>
                    {a.enabled ? "Disable" : "Enable"}
                  </button>
                  <button className="link small danger" onClick={() => remove(a)}>
                    Delete
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

// AutomationStatus renders the run state as a coloured badge.
function AutomationStatus({ a }: { a: Automation }) {
  if (!a.enabled) return <span className="badge muted">Disabled</span>;
  if (a.status === "running") return <span className="badge run">Running</span>;
  return <span className="badge ok">Active</span>;
}

// formatInterval renders seconds as the largest whole unit (h/m/s).
function formatInterval(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}
