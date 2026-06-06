import { useEffect, useState } from "react";
import { api } from "./api";
import { ApiError, type Identity } from "./types";
import { Login } from "./components/Login";
import { Dashboard } from "./components/Dashboard";

export function App() {
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api
      .get<Identity>("/v1/auth/me")
      .then(setIdentity)
      .catch((err) => {
        if (!(err instanceof ApiError) || err.status !== 401) {
          console.error(err);
        }
      })
      .finally(() => setLoading(false));
  }, []);

  async function logout() {
    try {
      await api.post("/v1/auth/logout");
    } catch {
      /* ignore */
    }
    setIdentity(null);
  }

  if (loading) return <div className="center muted">Loading…</div>;

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo">◆</span> IdentityHub
          <span className="tag">NHI Findings</span>
        </div>
        <nav className="row gap">
          <a className="link" href="/api_docs/index.html" target="_blank" rel="noreferrer">
            /api_docs ↗
          </a>
          {identity && (
            <button className="link" onClick={logout}>
              Sign out
            </button>
          )}
        </nav>
      </header>
      <main className="container">
        {identity ? <Dashboard /> : <Login onSuccess={setIdentity} />}
      </main>
    </div>
  );
}
