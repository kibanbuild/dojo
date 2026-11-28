import { readFileSync } from 'node:fs';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const source = readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8');
const ORIGIN = 'https://dojo.test';
const HTML = '<script src="/app/assets/index-a1.js"></script><link href="/app/assets/index-a1.css">';

type Listener = (event: Record<string, unknown>) => void;

function keyOf(req: string | Request) {
  return typeof req === 'string' ? new URL(req, ORIGIN).href : req.url;
}

function fakeCaches() {
  const stores = new Map<string, Map<string, Response>>();
  const open = (name: string) => {
    const store = stores.get(name) ?? new Map<string, Response>();
    stores.set(name, store);
    return {
      match: async (req: string | Request) => store.get(keyOf(req))?.clone(),
      put: async (req: string | Request, res: Response) => void store.set(keyOf(req), res),
      addAll: async (urls: string[]) => {
        for (const u of urls) store.set(keyOf(u), await fetchStub(u));
      },
    };
  };
  return {
    stores,
    api: {
      open: async (name: string) => open(name),
      keys: async () => [...stores.keys()],
      delete: async (name: string) => stores.delete(name),
    },
  };
}

let fetchStub: (req: string | Request, init?: RequestInit) => Promise<Response>;
let listeners: Record<string, Listener>;
let store: ReturnType<typeof fakeCaches>;

function load() {
  listeners = {};
  store = fakeCaches();
  const self = {
    location: new URL(ORIGIN),
    addEventListener: (type: string, fn: Listener) => void (listeners[type] = fn),
  };
  new Function('self', 'caches', 'fetch', source)(self, store.api, (r: string | Request, i?: RequestInit) => fetchStub(r, i));
}

async function lifecycle(type: 'install' | 'activate') {
  let pending: Promise<unknown> = Promise.resolve();
  listeners[type]?.({ waitUntil: (p: Promise<unknown>) => (pending = p) });
  await pending;
}

function dispatch(url: string, init: { method?: string; mode?: string } = {}) {
  const request = { url: new URL(url, ORIGIN).href, method: init.method ?? 'GET', mode: init.mode ?? 'cors' };
  let answer: Promise<Response> | undefined;
  listeners.fetch?.({ request, respondWith: (p: Promise<Response>) => (answer = p) });
  return answer;
}

const html = (body = HTML) => new Response(body, { headers: { 'content-type': 'text/html' } });

beforeEach(() => {
  fetchStub = vi.fn(async (req) => {
    const path = new URL(keyOf(req)).pathname;
    return path.endsWith('.html') ? html() : new Response('asset');
  });
  load();
});

describe('install', () => {
  it('caches the shell, its assets and the offline page under a versioned name', async () => {
    await lifecycle('install');
    expect([...store.stores.keys()]).toEqual(['dojo-shell-v1']);
    const urls = [...(store.stores.get('dojo-shell-v1')?.keys() ?? [])].map((u) => new URL(u).pathname).sort();
    expect(urls).toEqual(['/app/assets/index-a1.css', '/app/assets/index-a1.js', '/app/index.html', '/app/offline.html']);
  });

  it('fails the install when the shell cannot be fetched', async () => {
    fetchStub = async () => new Response('no', { status: 502 });
    await expect(lifecycle('install')).rejects.toThrow();
  });
});

describe('activate', () => {
  it('deletes older shell caches and leaves other caches alone', async () => {
    store.stores.set('dojo-shell-v0', new Map());
    store.stores.set('dojo-shell-v1', new Map());
    store.stores.set('other', new Map());
    await lifecycle('activate');
    expect([...store.stores.keys()].sort()).toEqual(['dojo-shell-v1', 'other']);
  });
});

describe('fetch', () => {
  it('never handles the API, whatever the method or mode', async () => {
    for (const path of ['/v1/me', '/v1/today', '/v1/stream', '/v1']) {
      expect(dispatch(path)).toBeUndefined();
      expect(dispatch(path, { mode: 'navigate' })).toBeUndefined();
    }
    expect(fetchStub).not.toHaveBeenCalled();
  });

  it('ignores writes and other origins', () => {
    expect(dispatch('/app/assets/index-a1.js', { method: 'POST' })).toBeUndefined();
    expect(dispatch('https://elsewhere.test/app/assets/x.js')).toBeUndefined();
    expect(dispatch('/app/offline.html', { mode: 'navigate' })).toBeUndefined();
  });

  it('serves navigations network first and refreshes the cached shell', async () => {
    await lifecycle('install');
    fetchStub = async () => html('<p>new</p>');
    const res = await dispatch('/app/today', { mode: 'navigate' });
    expect(await res?.text()).toBe('<p>new</p>');
    const cached = store.stores.get('dojo-shell-v1')?.get(`${ORIGIN}/app/index.html`);
    expect(await cached?.text()).toBe('<p>new</p>');
  });

  it('falls back to the cached shell, then to the offline page', async () => {
    fetchStub = async () => {
      throw new TypeError('offline');
    };
    store.stores.set('dojo-shell-v1', new Map([[`${ORIGIN}/app/offline.html`, new Response('offline page')]]));
    expect(await (await dispatch('/app/today', { mode: 'navigate' }))?.text()).toBe('offline page');
    store.stores.get('dojo-shell-v1')?.set(`${ORIGIN}/app/index.html`, new Response('shell'));
    expect(await (await dispatch('/app/today', { mode: 'navigate' }))?.text()).toBe('shell');
  });

  it('does not replace the shell with an error page', async () => {
    await lifecycle('install');
    fetchStub = async () => new Response('boom', { status: 500, headers: { 'content-type': 'text/html' } });
    await dispatch('/app/today', { mode: 'navigate' });
    const cached = store.stores.get('dojo-shell-v1')?.get(`${ORIGIN}/app/index.html`);
    expect(await cached?.text()).toBe(HTML);
  });

  it('serves hashed assets cache first and stores misses', async () => {
    await lifecycle('install');
    fetchStub = async () => new Response('network');
    expect(await (await dispatch('/app/assets/index-a1.js'))?.text()).toBe('asset');
    expect(await (await dispatch('/app/assets/lazy-b2.js'))?.text()).toBe('network');
    expect(store.stores.get('dojo-shell-v1')?.has(`${ORIGIN}/app/assets/lazy-b2.js`)).toBe(true);
  });

  it('does not store or replay failed asset responses', async () => {
    await lifecycle('install');
    for (const status of [404, 500]) {
      fetchStub = async () => new Response('bad', { status });
      const res = await dispatch('/app/assets/gone-c3.js');
      expect(res?.status).toBe(status);
      expect(store.stores.get('dojo-shell-v1')?.has(`${ORIGIN}/app/assets/gone-c3.js`)).toBe(false);
    }
    fetchStub = async () => new Response('fresh');
    expect(await (await dispatch('/app/assets/gone-c3.js'))?.text()).toBe('fresh');
  });

  it('does not replace the shell with a non-HTML navigation response', async () => {
    await lifecycle('install');
    fetchStub = async () => new Response('{}', { headers: { 'content-type': 'application/json' } });
    await dispatch('/app/today', { mode: 'navigate' });
    const cached = store.stores.get('dojo-shell-v1')?.get(`${ORIGIN}/app/index.html`);
    expect(await cached?.text()).toBe(HTML);
  });

  it('leaves no cached shell when an install asset fails', async () => {
    fetchStub = async (req) => {
      const path = new URL(keyOf(req)).pathname;
      if (path === '/app/assets/index-a1.js') throw new TypeError('offline');
      return path.endsWith('.html') ? html() : new Response('asset');
    };
    await expect(lifecycle('install')).rejects.toThrow();
    expect(store.stores.get('dojo-shell-v1')?.has(`${ORIGIN}/app/index.html`) ?? false).toBe(false);
  });

  it('never stores anything under /v1', async () => {
    await lifecycle('install');
    dispatch('/v1/me');
    await dispatch('/app/today', { mode: 'navigate' });
    const keys = [...store.stores.values()].flatMap((s) => [...s.keys()]);
    expect(keys.some((k) => new URL(k).pathname.startsWith('/v1'))).toBe(false);
  });
});
