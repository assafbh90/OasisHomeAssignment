import { useEffect, useState } from "react";
import { api } from "../api";
import { type ApiToken, type IssuedToken } from "../types";

export function TokensPanel() {
  const [tokens, setTokens] = useState<ApiToken[]>([]);
  const [name, setName] = useState("");
  const [writeScope, setWriteScope] = useState(true);
  const [plaintext, setPlaintext] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  async function load() {
    const res = await api.get<{ tokens: ApiToken[] }>("/v1/tokens");
    setTokens(res.tokens ?? []);
  }

  useEffect(() => {
    load();
  }, []);

  async function issue(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    const scopes = writeScope ? ["integrations:write"] : ["integrations:read"];
    const t = await api.post<IssuedToken>("/v1/tokens", { name, scopes });
    setPlaintext(t.token);
    setName("");
    await load();
  }

  async function revoke(id: string) {
    await api.del(`/v1/tokens/${id}`);
    await load();
  }

  return (
    <section className="card span2">
      <div className="row spread">
        <h2>API keys</h2>
        <button className="link" onClick={() => setOpen((o) => !o)}>
          {open ? "Hide" : "Manage"}
        </button>
      </div>
      <p className="muted">For scanners / CI to create findings via the REST API.</p>

      {open && (
        <>
          <form className="row gap wrap" onSubmit={issue}>
            <input placeholder="Key name (e.g. ci-scanner)" value={name} onChange={(e) => setName(e.target.value)} />
            <label className="checkbox">
              <input type="checkbox" checked={writeScope} onChange={(e) => setWriteScope(e.target.checked)} />
              can create tickets (write)
            </label>
            <button className="primary" type="submit">
              Generate key
            </button>
          </form>

          {plaintext && (
            <div className="alert warn">
              Copy this key now — it won't be shown again:
              <code className="token">{plaintext}</code>
            </div>
          )}

          {tokens.length === 0 ? (
            <p className="muted">No API keys yet.</p>
          ) : (
            <table className="tokens">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Scopes</th>
                  <th>Created</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {tokens.map((t) => (
                  <tr key={t.id} className={t.revoked_at ? "revoked" : ""}>
                    <td>{t.name}</td>
                    <td>{t.scopes.join(", ") || "—"}</td>
                    <td>{new Date(t.created_at).toLocaleDateString()}</td>
                    <td>
                      {t.revoked_at ? (
                        <span className="muted">revoked</span>
                      ) : (
                        <button className="link danger" onClick={() => revoke(t.id)}>
                          Revoke
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </section>
  );
}
