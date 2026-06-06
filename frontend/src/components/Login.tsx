import { useState } from "react";
import { api } from "../api";
import { ApiError, type Identity, type LoginResponse } from "../types";

export function Login({ onSuccess }: { onSuccess: (id: Identity) => void }) {
  const [email, setEmail] = useState("admin@acme.test");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      const res = await api.post<LoginResponse>("/v1/auth/login", { email, password });
      onSuccess(res);
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        setError("Too many attempts. Please wait a moment and try again.");
      } else {
        setError("Invalid email or password.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card narrow">
      <h1>Sign in</h1>
      <p className="muted">Report NHI findings to your Jira workspace.</p>
      <form onSubmit={submit}>
        <label>
          Email
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" />
        </label>
        <label>
          Password
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
        </label>
        {error && <div className="alert error">{error}</div>}
        <button className="primary" disabled={busy} type="submit">
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
