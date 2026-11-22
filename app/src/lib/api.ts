// The API expects this header on every state-changing request that carries the session cookie
// (internal/server/auth.go CSRF): any non-empty value, plus a JSON content type.
export const CSRF_HEADER = 'X-Dojo-Csrf';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/** fetch with the session cookie, plus the CSRF header and a JSON content type on mutating requests. */
export function apiFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const method = (init.method ?? 'GET').toUpperCase();
  const headers = new Headers(init.headers);
  if (!SAFE_METHODS.has(method)) {
    headers.set(CSRF_HEADER, '1');
    if (!headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  }
  return fetch(path, { ...init, method, headers, credentials: 'same-origin' });
}
