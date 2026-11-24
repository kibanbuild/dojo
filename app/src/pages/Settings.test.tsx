// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { analytics } from '../lib/measure';
import { usernameProblem } from '../lib/settings';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'irene-gh', email: null, language: 'en', usage_stats: true, analytics_consent: null };

type Call = { url: string; method: string; headers: Headers; body: string | undefined };
let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let calls: Call[];
let pending: ((r: Response) => void) | null;

/** GET /v1/me serves `meBody`; PATCH goes to `onPatch` (a status, a body, or 'hold' to resolve later). */
function stubApi(
  onPatch: (body: Record<string, string>) => number | [number, string] | object | 'hold',
  meBody: object | number = me,
) {
  calls = [];
  pending = null;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = (init.method ?? 'GET').toUpperCase();
      calls.push({ url, method, headers: new Headers(init.headers), body: init.body as string | undefined });
      if (method === 'GET') return typeof meBody === 'number' ? new Response(null, { status: meBody }) : Response.json(meBody);
      const out = onPatch(JSON.parse(init.body as string));
      if (out === 'hold') return new Promise<Response>((r) => (pending = r));
      if (Array.isArray(out)) return Response.json({ error: out[1] }, { status: out[0] });
      return typeof out === 'number' ? Response.json({ error: 'x' }, { status: out }) : Response.json(out);
    }),
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

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

beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

const input = () => container.querySelector<HTMLInputElement>('input[name=username]')!;
const saveBtn = () => [...container.querySelectorAll('button')].find((b) => /^Sav/.test(b.textContent ?? ''))!;
const toggle = () => container.querySelector<HTMLInputElement>('input[role=switch]:not([name=analytics_consent])')!;
const notes = () => [...container.querySelectorAll('[role=status]')].map((n) => n.textContent).join(' | ');

async function type(value: string) {
  // React tracks the value; set through the native setter so onChange fires.
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(input(), value);
    input().dispatchEvent(new Event('input', { bubbles: true }));
  });
}
const click = (el: HTMLElement) => act(async () => el.click());

describe('usernameProblem', () => {
  it.each([
    ['', 'Pick'],
    ['ab', 'at least 3'],
    ['a'.repeat(31), 'at most 30'],
    ['bad_name', 'lowercase'],
    ['Has Space', 'lowercase'],
  ])('rejects %j', (value, text) => expect(usernameProblem(value)).toContain(text));

  it.each(['abc', 'irene-2', 'a'.repeat(30)])('accepts %j', (value) => expect(usernameProblem(value)).toBe(''));
});

describe('/settings route', () => {
  it('sends a signed-out visitor to the sign-in screen', async () => {
    stubApi(() => 200, 401);
    await mount();
    expect(router.state.location.pathname).toBe('/signin');
  });

  it('renders the three cards and the nav for a signed-in learner (deep link)', async () => {
    stubApi(() => me);
    await mount();
    expect(router.state.location.pathname).toBe('/settings');
    expect(container.querySelector('h1')?.textContent).toBe('Settings');
    expect(container.querySelector('nav[aria-label="Settings sections"] [aria-current=page]')?.textContent).toBe('Account');
    expect(container.textContent).toContain('@irene-gh · signed in with GitHub');
    expect(container.textContent).not.toMatch(/2FA/);
    expect(input().value).toBe('irene');
    expect(toggle().checked).toBe(false);
  });

  it('links to Settings from the header and back to Today', async () => {
    stubApi(() => me);
    await mount('/');
    const link = container.querySelector<HTMLAnchorElement>('header a[href="/settings"]')!;
    await click(link);
    await settle();
    expect(router.state.location.pathname).toBe('/settings');
    const back = [...container.querySelectorAll('a')].find((a) => a.textContent?.includes('Back to Today'))!;
    await click(back);
    expect(router.state.location.pathname).toBe('/today');
  });
});

describe('username card', () => {
  it('starts with Save disabled (unchanged)', async () => {
    stubApi(() => me);
    await mount();
    expect(saveBtn().disabled).toBe(true);
  });

  it('lowercases as you type and shows an inline error for an invalid value', async () => {
    stubApi(() => me);
    await mount();
    await type('Bad_Name');
    expect(input().value).toBe('bad_name');
    expect(saveBtn().disabled).toBe(true);
    expect(input().getAttribute('aria-invalid')).toBe('true');
    const note = document.getElementById(input().getAttribute('aria-describedby')!)!;
    expect(note.textContent).toContain('lowercase letters');
    expect(note.getAttribute('aria-live')).toBe('polite');
  });

  it('saves with PATCH + CSRF, shows busy, then success, and updates the cache', async () => {
    stubApi(() => 'hold');
    await mount();
    await type('irene-new');
    expect(saveBtn().disabled).toBe(false);
    await click(saveBtn());
    expect(saveBtn().disabled).toBe(true);
    expect(saveBtn().textContent).toBe('Saving...');
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.url).toBe('/v1/me');
    expect(patch.body).toBe('{"username":"irene-new"}');
    expect(patch.headers.get('X-Dojo-Csrf')).toBeTruthy();
    expect(patch.headers.get('Content-Type')).toBe('application/json');
    await act(async () => pending!(Response.json({ ...me, username: 'irene-new' })));
    await settle();
    expect(notes()).toContain('Saved.');
    expect(saveBtn().textContent).toBe('Save');
    expect(saveBtn().disabled).toBe(true);
    expect(container.querySelector('header a[href="/settings"]')?.textContent).toBe('irene-new');
  });

  it('shows the 409 inline and keeps the draft', async () => {
    stubApi(() => 409);
    await mount();
    await type('taken-name');
    await click(saveBtn());
    await settle();
    expect(notes()).toContain('taken');
    expect(input().value).toBe('taken-name');
    expect(input().getAttribute('aria-invalid')).toBe('true');
  });

  it('words a reserved username as unavailable, not as a generic error', async () => {
    stubApi(() => [400, 'invalid_username']);
    await mount();
    await type('admin');
    await click(saveBtn());
    await settle();
    expect(notes()).toContain('not available');
  });

  it('does not call a CSRF rejection a suspended account', async () => {
    stubApi(() => [403, 'csrf_rejected']);
    await mount();
    await type('fresh-name');
    await click(saveBtn());
    await settle();
    expect(notes()).not.toContain('suspended');
    expect(notes()).toContain('Could not save');
  });

  it.each([404, 405])('handles a missing endpoint (%i) without crashing', async (status) => {
    stubApi(() => status);
    await mount();
    await type('another');
    await click(saveBtn());
    await settle();
    expect(notes()).toContain('not available yet');
    expect(router.state.location.pathname).toBe('/settings');
  });

  it('goes to sign-in when the session ended (401)', async () => {
    stubApi(() => 401);
    await mount();
    await type('another');
    await click(saveBtn());
    await settle();
    expect(router.state.location.pathname).toBe('/signin');
  });
});

describe('language switch', () => {
  it('is a real checkbox with role=switch and toggles optimistically, then keeps the server body', async () => {
    stubApi(() => 'hold');
    await mount();
    expect(toggle().type).toBe('checkbox');
    await click(toggle());
    // The PATCH is held, so this is the optimistic state; settle lets the query notify its observers.
    await settle();
    expect(toggle().checked).toBe(true);
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.body).toBe('{"language":"es"}');
    expect(patch.headers.get('X-Dojo-Csrf')).toBeTruthy();
    await act(async () => pending!(Response.json({ ...me, language: 'es' })));
    await settle();
    expect(toggle().checked).toBe(true);
  });

  it('rolls back and says so when the save fails', async () => {
    stubApi(() => 500);
    await mount();
    await click(toggle());
    await settle();
    expect(toggle().checked).toBe(false);
    expect(notes()).toContain('Could not save');
  });

  it('rolls back on a missing endpoint', async () => {
    stubApi(() => 404);
    await mount();
    await click(toggle());
    await settle();
    expect(toggle().checked).toBe(false);
    expect(notes()).toContain('not available yet');
  });

  it('switches back to English with language "en"', async () => {
    stubApi((b) => ({ ...me, language: b.language }), { ...me, language: 'es' });
    await mount();
    expect(toggle().checked).toBe(true);
    await click(toggle());
    await settle();
    expect(calls.find((c) => c.method === 'PATCH')!.body).toBe('{"language":"en"}');
    expect(toggle().checked).toBe(false);
  });
});

describe('analytics switch', () => {
  beforeEach(() => {
    document.cookie = 'dojo_analytics_consent=; Path=/; Max-Age=0';
    analytics.setAccountConsent(null);
  });

  const sw = () => container.querySelector<HTMLInputElement>('input[name=analytics_consent]')!;

  it('is a real switch bound to analytics_consent', async () => {
    stubApi(() => me, { ...me, analytics_consent: true });
    await mount();
    expect(sw().type).toBe('checkbox');
    expect(sw().getAttribute('role')).toBe('switch');
    expect(sw().checked).toBe(true);
  });

  it('is off when undecided or denied', async () => {
    stubApi(() => me);
    await mount();
    expect(sw().checked).toBe(false);
  });

  it('toggles optimistically and sends analytics_consent', async () => {
    stubApi(() => 'hold');
    await mount();
    await click(sw());
    await settle();
    expect(sw().checked).toBe(true);
    expect(calls.find((c) => c.method === 'PATCH')!.body).toBe('{"analytics_consent":true}');
    await act(async () => pending!(Response.json({ ...me, analytics_consent: true })));
    await settle();
    expect(sw().checked).toBe(true);
  });

  it('rolls back and says so when the save fails', async () => {
    stubApi(() => 500, { ...me, analytics_consent: false });
    await mount();
    await click(sw());
    await settle();
    expect(sw().checked).toBe(false);
    expect(notes()).toContain('Could not save');
  });

  it('reads on for a null account with a granted cookie', async () => {
    document.cookie = 'dojo_analytics_consent=granted; Path=/';
    try {
      stubApi(() => me);
      await mount();
      expect(sw().checked).toBe(true);
    } finally {
      document.cookie = 'dojo_analytics_consent=; Path=/; Max-Age=0';
    }
  });
});
