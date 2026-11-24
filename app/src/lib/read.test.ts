import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchCatalog, fetchPath, fetchTicket, fetchToday, readBlockedUntil, ReadError, resetReadBlock } from './read';

function stub(res: Response) {
  const spy = vi.fn<typeof fetch>(async () => res);
  vi.stubGlobal('fetch', spy);
  return spy;
}

async function failure(p: Promise<unknown>): Promise<ReadError> {
  try {
    await p;
  } catch (e) {
    return e as ReadError;
  }
  throw new Error('expected a rejection');
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetReadBlock();
});

describe('read routes', () => {
  it.each([
    ['/v1/catalog', fetchCatalog],
    ['/v1/path', fetchPath],
    ['/v1/today', fetchToday],
  ])('GET %s with the cookie and no CSRF header', async (url, fn) => {
    const spy = stub(Response.json({ ok: 1 }));
    expect(await fn()).toEqual({ ok: 1 });
    const [path, init] = spy.mock.calls[0] as [string, RequestInit];
    expect(path).toBe(url);
    expect(init.method).toBe('GET');
    expect(init.credentials).toBe('same-origin');
    expect(new Headers(init.headers).has('X-Dojo-Csrf')).toBe(false);
  });

  it('keeps the slash of a ticket id and escapes the rest', async () => {
    const spy = stub(Response.json({}));
    await fetchTicket('support-l1/perms 03');
    expect(spy.mock.calls[0]?.[0]).toBe('/v1/tickets/support-l1/perms%2003');
  });

  it.each([
    [401, {}, 'signed_out'],
    [403, { error: 'account_suspended' }, 'suspended'],
    [403, { error: 'forbidden' }, 'failed'],
    [404, { error: 'not_found' }, 'not_found'],
    [500, { error: 'server_error' }, 'failed'],
  ])('maps %i %j to %s', async (status, body, reason) => {
    stub(Response.json(body, { status }));
    expect((await failure(fetchToday())).reason).toBe(reason);
  });

  it('reads Retry-After on a 429 and blocks live refetches until then', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
    stub(Response.json({ error: 'rate_limited' }, { status: 429, headers: { 'Retry-After': '7' } }));
    const err = await failure(fetchPath());
    expect(err.reason).toBe('rate_limited');
    expect(err.retryAfterMs).toBe(7000);
    expect(readBlockedUntil()).toBe(1_007_000);
  });

  it('holds every read until the Retry-After deadline has passed', async () => {
    vi.useFakeTimers();
    stub(Response.json({ error: 'rate_limited' }, { status: 429, headers: { 'Retry-After': '3' } }));
    await failure(fetchPath());
    const spy = stub(Response.json({ ok: 1 }));
    const pending = fetchToday();
    await vi.advanceTimersByTimeAsync(2999);
    expect(spy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(await pending).toEqual({ ok: 1 });
    expect(spy).toHaveBeenCalledOnce();
  });

  it('turns a network error and a bad body into failed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('offline'))));
    expect((await failure(fetchPath())).reason).toBe('failed');
    stub(new Response('<html>', { status: 200 }));
    expect((await failure(fetchPath())).reason).toBe('failed');
  });
});
