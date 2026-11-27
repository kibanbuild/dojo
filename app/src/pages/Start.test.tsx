// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock } from '../lib/read';
import { emailProblem } from '../lib/email';
import { onboardingRedirect, type NextStep, type Onboarding } from '../lib/onboarding';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const learner = { id: '1', username: 'irene', github_login: 'irene', email: 'irene@example.com', language: 'en', usage_stats: true, analytics_consent: null };

interface Call {
  method: string;
  url: string;
  body: unknown;
}

interface Stub {
  me?: object | null;
  onboarding?: NextStep | number;
  /** The `waitlist` of GET /v1/onboarding; undefined leaves the field out. */
  waitlist?: string[];
}

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let calls: Call[];

function state(next: NextStep): Onboarding {
  return { next_step: next, path: { slug: null, chosen: false }, cli_connected: false, first_ticket: null };
}

function stub({ me = learner, onboarding = 'done', waitlist }: Stub = {}) {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      calls.push({ method, url, body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined });
      if (url === '/v1/me' && method === 'GET') return me ? Response.json(me) : new Response(null, { status: 401 });
      if (url === '/v1/me' && method === 'PATCH') {
        me = { ...learner, email: 'saved@example.com' };
        return Response.json(me);
      }
      if (url === '/v1/onboarding') return typeof onboarding === 'number' ? new Response(null, { status: onboarding }) : Response.json({ ...state(onboarding), ...(waitlist ? { waitlist } : {}) });
      if (url.startsWith('/v1/onboarding/')) return new Response(null, { status: 204 });
      return new Response(null, { status: 404 });
    }),
  );
}

/** Polls until the condition holds, so no assertion runs before TanStack Query has notified. */
async function until(check: () => boolean) {
  for (let i = 0; i < 100 && !check(); i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
  expect(check()).toBe(true);
}

async function mount(path: string) {
  router = createMemoryRouter(routes, { initialEntries: [path] });
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
}

const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent?.startsWith(name));
const click = (el: Element | undefined) => act(async () => (el as HTMLElement).click());
const writes = () => calls.filter((c) => c.method !== 'GET');

beforeEach(() => {
  resetReadBlock();
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

describe('routing decisions', () => {
  it('maps each unfinished step to its route and finished or unknown to nothing', () => {
    expect(onboardingRedirect(state('o0'))).toBe('/start');
    expect(onboardingRedirect(state('o2'))).toBe('/start/install');
    expect(onboardingRedirect(state('o3'))).toBe('/start/doctor');
    expect(onboardingRedirect(state('o4'))).toBe('/start/key');
    expect(onboardingRedirect(state('o5'))).toBe('/start/first');
    expect(onboardingRedirect(state('done'))).toBeNull();
    expect(onboardingRedirect(null)).toBeNull();
  });

  it.each([
    ['o0', '/start'],
    ['o2', '/start/install'],
    ['o5', '/start/first'],
  ] as const)('sends a signed-in learner at step %s from /today to %s', async (step, to) => {
    stub({ onboarding: step });
    await mount('/today');
    await until(() => router.state.location.pathname === to);
  });

  it('sends the app root to the first unfinished step too', async () => {
    stub({ onboarding: 'o2' });
    await mount('/');
    await until(() => router.state.location.pathname === '/start/install');
  });

  it('keeps a learner who is done on Today', async () => {
    stub({ onboarding: 'done' });
    await mount('/today');
    await until(() => calls.some((c) => c.url === '/v1/today'));
    expect(router.state.location.pathname).toBe('/today');
  });

  it('does not trap a learner when the onboarding read fails', async () => {
    stub({ onboarding: 500 });
    await mount('/today');
    await until(() => calls.some((c) => c.url === '/v1/today'));
    expect(router.state.location.pathname).toBe('/today');
  });

  it('sends a signed-out visitor from a later step to sign-in', async () => {
    stub({ me: null });
    await mount('/start/install');
    await until(() => router.state.location.pathname === '/signin');
  });
});

describe('O0 Pick your path', () => {
  it('is reachable signed out and draws the four paths with the open one selected', async () => {
    stub({ me: null });
    await mount('/start');
    await until(() => container.querySelector('h1') !== null);
    expect(router.state.location.pathname).toBe('/start');
    expect(container.querySelector<HTMLInputElement>('input[name=path]')?.checked).toBe(true);
    expect(container.textContent).toContain('Security');
    expect(container.textContent).toContain('Backend');
    expect(container.textContent).toContain('Frontend');
    expect([...container.querySelectorAll('button')].filter((b) => b.textContent?.startsWith('Tell me when')).length).toBe(3);
    expect(container.querySelector('[aria-current=step]')?.textContent).toContain('Path');
  });

  it('signed out, Continue posts the choice for the cookie and goes to sign-in', async () => {
    stub({ me: null });
    await mount('/start');
    await until(() => button('Continue') !== undefined);
    await click(button('Continue'));
    await until(() => router.state.location.pathname === '/signin');
    expect(writes()).toEqual([{ method: 'POST', url: '/v1/onboarding/path', body: { path: 'systems-devops' } }]);
  });

  it('signed in, Continue stores the choice and follows the onboarding step', async () => {
    stub({ onboarding: 'o2' });
    await mount('/start');
    await until(() => button('Continue') !== undefined);
    await click(button('Continue'));
    await until(() => router.state.location.pathname === '/start/install');
    expect(writes()).toEqual([{ method: 'POST', url: '/v1/onboarding/path', body: { path: 'systems-devops' } }]);
  });

  it('Tell me when joins the waitlist and undo leaves it', async () => {
    stub({ me: null });
    await mount('/start');
    await until(() => button('Tell me when') !== undefined);
    await click(button('Tell me when'));
    await until(() => button("You're on the list") !== undefined);
    expect(writes()).toEqual([{ method: 'POST', url: '/v1/onboarding/waitlist', body: { path: 'security' } }]);
    await click(button("You're on the list"));
    await until(() => button("You're on the list") === undefined);
    expect(writes().at(-1)).toEqual({ method: 'DELETE', url: '/v1/onboarding/waitlist/security', body: undefined });
  });
});

describe('O0 overlapping waitlist joins', () => {
  it('keeps each path busy and listed on its own when the answers arrive in reverse order', async () => {
    stub({ me: null });
    const inner = globalThis.fetch;
    const release: Record<string, () => void> = {};
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init?: RequestInit) => {
        if (url !== '/v1/onboarding/waitlist' || init?.method !== 'POST') return inner(url, init);
        const slug = (JSON.parse(String(init.body)) as { path: string }).path;
        return new Promise<Response>((resolve) => {
          release[slug] = () => resolve(new Response(null, { status: 204 }));
        });
      }),
    );
    await mount('/start');
    const forPath = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent?.includes(`for ${name}`));
    await until(() => forPath('Security') !== undefined);
    await click(forPath('Security'));
    await click(forPath('Backend'));
    await until(() => release['security'] !== undefined && release['backend'] !== undefined);
    expect(forPath('Security')?.disabled).toBe(true);
    expect(forPath('Backend')?.disabled).toBe(true);

    await act(async () => release['backend']!());
    await until(() => forPath('Backend')?.textContent?.startsWith("You're on the list") === true);
    expect(forPath('Security')?.disabled).toBe(true);

    await act(async () => release['security']!());
    await until(() => forPath('Security')?.textContent?.startsWith("You're on the list") === true);
    expect(forPath('Backend')?.textContent).toContain("You're on the list");
    expect(forPath('Security')?.disabled).toBe(false);
    expect(forPath('Backend')?.disabled).toBe(false);
  });
});

describe('placeholder steps', () => {
  it('offers a way out to Settings so nobody is stuck on a step that is not built', async () => {
    stub({ onboarding: 'o4' });
    await mount('/start/key');
    await until(() => [...container.querySelectorAll('a')].some((a) => a.textContent === 'Open settings'));
    expect([...container.querySelectorAll('a')].find((a) => a.textContent === 'Open settings')?.getAttribute('href')).toBe('/settings');
  });
});

describe('O0 waitlist readback', () => {
  it('shows the list the API reports for a signed-in learner', async () => {
    stub({ onboarding: 'o0', waitlist: ['backend'] });
    await mount('/start');
    await until(() => button("You're on the list") !== undefined);
    expect(button("You're on the list")?.textContent).toContain('Backend');
    expect([...container.querySelectorAll('button')].filter((b) => b.textContent?.startsWith('Tell me when')).length).toBe(2);
  });

  it('reads a missing waitlist field as empty', async () => {
    stub({ onboarding: 'o0' });
    await mount('/start');
    await until(() => button('Tell me when') !== undefined);
    expect(button("You're on the list")).toBeUndefined();
  });

  it('refetches onboarding after a join', async () => {
    stub({ onboarding: 'o0' });
    await mount('/start');
    await until(() => button('Tell me when') !== undefined);
    const before = calls.filter((c) => c.url === '/v1/onboarding').length;
    await click(button('Tell me when'));
    await until(() => calls.filter((c) => c.url === '/v1/onboarding').length > before);
    expect(button("You're on the list")).toBeDefined();
  });
});

describe('email prompt', () => {
  it('shows on O0 for a learner without email whom the API lists on a waitlist', async () => {
    stub({ me: { ...learner, email: null }, onboarding: 'o0', waitlist: ['security'] });
    await mount('/start');
    await until(() => container.querySelector('section[aria-label="Email address"]') !== null);
  });

  it('shows on Today while the account has no email, and not when it has one', async () => {
    stub({ me: { ...learner, email: null } });
    await mount('/today');
    await until(() => container.querySelector('section[aria-label="Email address"]') !== null);
    act(() => root.unmount());
    root = createRoot(container);
    stub({ me: learner });
    await mount('/today');
    await until(() => calls.some((c) => c.url === '/v1/today'));
    expect(container.querySelector('section[aria-label="Email address"]')).toBeNull();
  });

  it('shows on O0 only once a signed-in learner without email has joined a waitlist', async () => {
    stub({ me: { ...learner, email: null } });
    await mount('/start');
    await until(() => button('Tell me when') !== undefined);
    expect(container.querySelector('section[aria-label="Email address"]')).toBeNull();
    await click(button('Tell me when'));
    await until(() => container.querySelector('section[aria-label="Email address"]') !== null);
  });

  it('saves through PATCH /v1/me and hides once the account has the address', async () => {
    stub({ me: { ...learner, email: null } });
    await mount('/today');
    await until(() => container.querySelector('input[type=email]') !== null);
    const input = container.querySelector<HTMLInputElement>('input[type=email]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'saved@example.com');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await click(button('Save email'));
    await until(() => container.querySelector('section[aria-label="Email address"]') === null);
    expect(writes()).toEqual([{ method: 'PATCH', url: '/v1/me', body: { email: 'saved@example.com' } }]);
  });

  it('rejects a bad address before calling the API', async () => {
    stub({ me: { ...learner, email: null } });
    await mount('/today');
    await until(() => button('Save email') !== undefined);
    await click(button('Save email'));
    await until(() => container.textContent?.includes('Enter your email address.') === true);
    expect(writes()).toEqual([]);
  });

  it('validates addresses the way the API does', () => {
    expect(emailProblem('a@b.co')).toBe('');
    expect(emailProblem('a+tag@sub.example.com')).toBe('');
    for (const bad of ['', 'a', 'a@b', 'a@@b.co', '"a"@b.co', 'a b@b.co', 'a@b..co', 'a,b@b.co']) expect(emailProblem(bad)).not.toBe('');
  });
});
