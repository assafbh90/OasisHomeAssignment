import { ApiError } from "./types";

// readCookie returns a browser cookie value by name.
function readCookie(name: string): string | undefined {
  const match = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]*)"));
  return match ? decodeURIComponent(match[1]) : undefined;
}

interface RequestOptions {
  method?: string;
  body?: unknown;
}

// request is the shared fetch wrapper. It sends cookies, attaches the CSRF token
// (double-submit) on unsafe methods, and normalizes errors into ApiError.
async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = {};
  let body: string | undefined;

  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  if (method !== "GET" && method !== "HEAD") {
    const csrf = readCookie("ih_csrf");
    if (csrf) headers["X-CSRF-Token"] = csrf;
  }

  const res = await fetch(path, { method, headers, body, credentials: "same-origin" });

  if (res.status === 204) return undefined as T;

  let payload: any = null;
  const text = await res.text();
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { message: text };
    }
  }

  if (!res.ok) {
    throw new ApiError(
      res.status,
      payload?.error ?? "error",
      payload?.message ?? res.statusText,
      payload?.reconnect_url,
      payload?.pending_action_id,
    );
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: "POST", body }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: "PUT", body }),
  del: <T>(path: string) => request<T>(path, { method: "DELETE" }),
};
