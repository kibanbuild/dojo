import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiFetch } from './api';

function stubFetch() {
  const spy = vi.fn(async () => new Response(null, { status: 204 }));
  vi.stubGlobal('fetch', spy);
  return spy;
}

function sent(spy: ReturnType<typeof stubFetch>): { headers: Headers; init: RequestInit } {
  const init = (spy.mock.calls[0] as unknown as [string, RequestInit])[1];
  return { headers: new Headers(init.headers), init };
}

afterEach(() => vi.unstubAllGlobals());

describe('apiFetch', () => {
  it('adds the CSRF header and JSON content type to a POST', async () => {
    const spy = stubFetch();
    await apiFetch('/v1/auth/logout', { method: 'POST', body: '{}' });
    const { headers, init } = sent(spy);
    expect(headers.get('X-Dojo-Csrf')).toBeTruthy();
    expect(headers.get('Content-Type')).toBe('application/json');
    expect(init.credentials).toBe('same-origin');
  });

  it.each(['PUT', 'PATCH', 'DELETE', 'post'])('adds the header to %s', async (method) => {
    const spy = stubFetch();
    await apiFetch('/v1/x', { method });
    expect(sent(spy).headers.get('X-Dojo-Csrf')).toBeTruthy();
  });

  it.each([undefined, 'GET', 'HEAD', 'OPTIONS'])('leaves %s requests alone', async (method) => {
    const spy = stubFetch();
    await apiFetch('/v1/me', { method });
    const { headers, init } = sent(spy);
    expect(headers.has('X-Dojo-Csrf')).toBe(false);
    expect(headers.has('Content-Type')).toBe(false);
    expect(init.credentials).toBe('same-origin');
  });

  it('keeps a caller content type', async () => {
    const spy = stubFetch();
    await apiFetch('/v1/x', { method: 'POST', headers: { 'Content-Type': 'application/json; charset=utf-8' } });
    expect(sent(spy).headers.get('Content-Type')).toBe('application/json; charset=utf-8');
  });
});
