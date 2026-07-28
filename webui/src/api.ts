const TOKEN_KEY = "autotaggerr_token";

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string | null) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}

/**
 * Picks up a session token handed back by the OIDC callback in the URL fragment.
 * Fragments never reach the server, so the token stays out of access logs and the
 * Referer header; it is stripped from the address bar immediately so it does not
 * linger in history or get shared by copying the URL.
 */
export function consumeTokenFromUrl(): boolean {
  const match = /[#&]token=([^&]+)/.exec(window.location.hash);
  if (!match) return false;
  setToken(decodeURIComponent(match[1]));
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  return true;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (res.status === 401) {
    setToken(null);
    // Let callers/route guards react; surface as an error too.
  }

  if (!res.ok) {
    let message = res.statusText;
    try {
      const j = await res.json();
      if (j && typeof j.error === "string") message = j.error;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, message);
  }

  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: (path: string) => request<void>("DELETE", path),
};

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
