// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { analytics, CONSENT_COOKIE } from '../lib/measure';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'irene-gh', email: null, language: 'en', usage_stats: true, analytics_consent: null as boolean | null };

type Call = { method: string; body: string | undefined };
let container: HTMLDivElement;
let root: Root;
let calls: Call[];
let pending: ((r: Response) => void) | null;

/** GET /v1/me serves `meBody` (401 when null); PATCH goes to `onPatch`: a status, a body, or 'hold'. */
function stubApi(meBody: object | null, onPatch: (body: { analytics_consent: boolean }) => number | object | 'hold' = (b) => ({ ...me, ...b })) {
  calls = [];
  pending = null;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init: RequestInit = {}) => {
      const method = (init.method ?? 'GET').toUpperCase();
      calls.push({ method, body: init.body as string | undefined });
      if (method === 'GET') return meBody ? Response.json(meBody) : new Response(null, { status: 401 });
      const out = onPatch(JSON.parse(init.body as string));
      if (out === 'hold') return new Promise<Response>((r) => (pending = r));
      return typeof out === 'number' ? Response.json({ error: 'x' }, { status: out }) : Response.json(out);
    }),
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

let router: ReturnType<typeof createMemoryRouter>;
async function mount(path = '/settings') {
  router = createMemoryRouter(routes, { initialEntries: [path] });
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await settle();
}

const setCookie = (v: string) => (document.cookie = `${CONSENT_COOKIE}=${v}; Path=/`);
const clearCookie = () => (document.cookie = `${CONSENT_COOKIE}=; Path=/; Max-Age=0`);
const banner = () => container.querySelector<HTMLElement>('[role=region][aria-label="Analytics consent"]');
const btn = (label: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === label)!;
const click = async (el: HTMLElement) => {
  await act(async () => el.click());
  await settle();
};
const cookieValue = () => document.cookie.split(';').map((p) => p.trim()).find((p) => p.startsWith(`${CONSENT_COOKIE}=`))?.split('=')[1];

beforeEach(() => {
  clearCookie();
  analytics.setAccountConsent(null);
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  clearCookie();
});

describe('consent banner visibility', () => {
  it.each([
    ['signed out, cookie unknown', null, undefined, true],
    ['signed out, cookie granted', null, 'granted', false],
    ['signed out, cookie denied', null, 'denied', false],
    ['signed in, account null, cookie unknown', null, undefined, true],
    ['signed in, account true', true, undefined, false],
    ['signed in, account false', false, undefined, false],
  ] as const)('%s -> shown: %s', async (name, account, cookie, shown) => {
    if (cookie) setCookie(cookie);
    const signedOut = name.startsWith('signed out');
    stubApi(signedOut ? null : { ...me, analytics_consent: account }, () => 200);
    await mount(signedOut ? '/signin' : '/settings');
    expect(banner() !== null).toBe(shown);
  });

  it('is a labelled region with two equal buttons and no pre-checked input', async () => {
    stubApi(null);
    await mount('/signin');
    const region = banner()!;
    expect(region.querySelector('h2')?.textContent).toBe('Help us improve the Dojo?');
    const buttons = [...region.querySelectorAll('button')];
    expect(buttons.map((b) => b.textContent)).toEqual(['Allow', 'No thanks']);
    expect(buttons[0]!.className).toBe(buttons[1]!.className);
    expect(region.querySelector('input')).toBeNull();
    expect(document.activeElement).toBe(document.body);
  });
});

describe('signed out', () => {
  it.each([
    ['Allow', 'granted'],
    ['No thanks', 'denied'],
  ] as const)('%s writes the cookie and closes without calling the API', async (label, value) => {
    stubApi(null);
    await mount('/signin');
    await click(btn(label));
    expect(cookieValue()).toBe(value);
    expect(analytics.consent()).toBe(value);
    expect(banner()).toBeNull();
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });
});

describe('signed in', () => {
  it.each([
    ['Allow', true, 'granted'],
    ['No thanks', false, 'denied'],
  ] as const)('%s patches the account, closes and writes the cookie', async (label, value, cookie) => {
    stubApi(me);
    await mount();
    await click(btn(label));
    expect(calls.find((c) => c.method === 'PATCH')!.body).toBe(JSON.stringify({ analytics_consent: value }));
    expect(banner()).toBeNull();
    expect(analytics.consent()).toBe(cookie);
    expect(cookieValue()).toBe(cookie);
  });

  it('does not grant while the save is in flight, only once it succeeds', async () => {
    stubApi(me, () => 'hold');
    await mount();
    await click(btn('Allow'));
    expect(banner()).not.toBeNull();
    expect(btn('Allow').disabled).toBe(true);
    expect(analytics.consent()).toBe('unknown');
    await act(async () => pending!(Response.json({ ...me, analytics_consent: true })));
    await settle();
    expect(banner()).toBeNull();
    expect(analytics.consent()).toBe('granted');
  });

  it.each([
    ['500', () => 500 as number | object],
    ['401', () => 401 as number | object],
  ])('sends nothing to the sink when the Allow save fails (%s)', async (_n, out) => {
    const info = vi.spyOn(console, 'info').mockImplementation(() => {});
    stubApi(me, out);
    await mount();
    await click(btn('Allow'));
    expect(analytics.consent()).toBe('unknown');
    expect(cookieValue()).toBeUndefined();
    expect(info.mock.calls.filter((c) => String(c[0]).startsWith('[analytics]'))).toEqual([]);
  });

  it('drops a stale error once consent is decided elsewhere', async () => {
    stubApi(me, () => 500);
    await mount();
    await click(btn('Allow'));
    expect(banner()!.textContent).toContain('Could not save');
    analytics.setConsent('denied');
    await act(async () => {
      await router.navigate('/settings/machines');
    });
    await settle();
    expect(banner()).toBeNull();
  });

  it('applies nothing and shows the error when the save fails', async () => {
    stubApi(me, () => 500);
    await mount();
    await click(btn('Allow'));
    expect(banner()!.textContent).toContain('Could not save');
    expect(analytics.consent()).toBe('unknown');
    expect(cookieValue()).toBeUndefined();
    expect(btn('Allow').disabled).toBe(false);
  });

  it('goes to sign-in when the session ended', async () => {
    stubApi(me, () => 401);
    await mount();
    await click(btn('No thanks'));
    expect(container.textContent).not.toContain('Account');
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  });
});
