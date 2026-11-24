// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { parseBlocks } from '../lib/markdown';
import { readBlockedUntil, resetReadBlock, type Ticket, type TicketProgress } from '../lib/read';
import { duration, viewOf } from './Ticket';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'irene', email: null, language: 'en', usage_stats: true, analytics_consent: null };
const ID = 'support-l1/perms-03';
const HINTS = ['Read `ls -ld shared`.', 'Read `man chmod`.', 'Pointer three.'];

const progress = (over: Partial<TicketProgress> = {}): TicketProgress => ({
  state: 'started',
  lab: 'local',
  lab_state: 'running',
  check_count: 0,
  last_check_ok: null,
  last_check_at: null,
  last_check_passed: null,
  last_check_total: null,
  last_check_output: null,
  first_passed_at: null,
  passed_seconds: null,
  passed_commit: null,
  ...over,
});

const ticket = (over: Partial<Ticket> = {}): Ticket => ({
  id: ID,
  title: 'The shared folder stopped accepting uploads',
  role: 'support-l1',
  est_minutes: 40,
  tags: [],
  brief: 'Elena says the team cannot save files.\n\n```\nls -ld /srv/shared\n```\n\n<script>alert(1)</script> and `dojo check`',
  hint_count: 3,
  hints: [],
  progress: null,
  ...over,
});

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let current: Ticket | number;
let posts: string[];
let limited = false;

function stubApi() {
  posts = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/v1/me') return Response.json(me);
      if (init?.method === 'POST') {
        posts.push(url);
        const n = Number(url.at(-1));
        if (n === 2 && limited) return new Response(null, { status: 429, headers: { 'Retry-After': '1' } });
        if (n === 3) return Response.json({ error: 'hint_locked' }, { status: 409 });
        return Response.json({ n, text: HINTS[n - 1] });
      }
      if (url === `/v1/tickets/${ID}`) return typeof current === 'number' ? new Response(null, { status: current }) : Response.json(current);
      return new Response(null, { status: 404 });
    }),
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

async function mount(t: Ticket | number, path = `/t/${ID}`) {
  current = t;
  stubApi();
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

const text = () => container.textContent ?? '';
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name);

beforeEach(() => {
  resetReadBlock();
  limited = false;
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

describe('helpers', () => {
  it('formats durations', () => {
    expect(duration(34 * 60)).toBe('34 min');
    expect(duration(10)).toBe('1 min');
    expect(duration(65 * 60)).toBe('1 h 5 min');
  });

  it('maps progress to a view', () => {
    expect(viewOf(null)).toBe('new');
    expect(viewOf(progress({ lab_state: 'starting' }))).toBe('starting');
    expect(viewOf(progress({ lab_state: null }))).toBe('running');
    expect(viewOf(progress({ state: 'passed' }))).toBe('passed');
    expect(viewOf(progress({ state: 'revisit', lab_state: 'expired' }))).toBe('revisit');
  });

  it('parses the markdown subset', () => {
    expect(parseBlocks('a\nb\n\n- x\n- y\n\n```\nc\n```')).toEqual([
      { kind: 'p', text: 'a b' },
      { kind: 'ul', items: ['x', 'y'] },
      { kind: 'code', text: 'c' },
    ]);
  });
});

describe('Ticket not started', () => {
  beforeEach(() => mount(ticket()));

  it('has one h1, the id badge and the start command', () => {
    expect(container.querySelectorAll('h1')).toHaveLength(1);
    expect(container.querySelector('h1')?.textContent).toBe('The shared folder stopped accepting uploads');
    expect(text()).toContain('Not started');
    expect(text()).toContain(ID);
    expect(container.querySelector(`button[aria-label="Copy command: dojo start ${ID}"]`)).not.toBeNull();
  });

  it('renders the brief with code blocks and never raw HTML', () => {
    expect(container.querySelector('pre')?.textContent).toBe('ls -ld /srv/shared');
    expect(container.querySelector('script')).toBeNull();
    expect(text()).toContain('<script>alert(1)</script>');
  });

  it('opens hint 1 in order and locks the rest', async () => {
    expect(button('Show hint 2')).toBeUndefined();
    expect(text()).toContain('unlocks after hint 1');
    await act(async () => button('Show hint 1')!.click());
    await settle();
    expect(posts).toEqual([`/v1/tickets/${ID}/hints/1`]);
    expect(container.querySelector('section[aria-label=Hints] code')?.textContent).toBe('ls -ld shared');
    expect(text()).toContain('1 of 3 opened');
    expect(button('Show hint 2')).toBeDefined();
  });
});

describe('Ticket live states', () => {
  it('shows a starting lab without a command', async () => {
    await mount(ticket({ progress: progress({ lab_state: 'starting' }) }));
    expect(text()).toContain('Starting your lab');
    expect(container.querySelector('button[aria-label^="Copy command"]')).toBeNull();
  });

  it('offers a restart when the lab failed or expired', async () => {
    await mount(ticket({ progress: progress({ lab_state: 'failed' }) }));
    expect(text()).toContain("Lab didn't start");
    expect(container.querySelector(`button[aria-label="Copy command: dojo start ${ID}"]`)).not.toBeNull();
  });

  it('shows the last failed check with output and a live region', async () => {
    await mount(ticket({ progress: progress({ check_count: 2, last_check_ok: false, last_check_passed: 1, last_check_total: 3, last_check_at: new Date().toISOString(), last_check_output: 'FAIL: other can write <b>' }) }));
    const status = container.querySelector('section[aria-label="Last check"] [role=status]')!;
    expect(status.textContent).toBe('Not yet · 1 of 3 checks pass · just now');
    const pre = container.querySelector('pre[aria-label="Check output"]')!;
    expect(pre.textContent).toBe('FAIL: other can write <b>');
    expect(pre.querySelector('b')).toBeNull();
  });

  it('shows the passed state with time and every hint', async () => {
    await mount(ticket({ hints: HINTS.map((t, i) => ({ n: i + 1, text: t })), progress: progress({ state: 'passed', check_count: 3, last_check_ok: true, last_check_passed: 3, last_check_total: 3, passed_seconds: 34 * 60 }) }));
    expect(text()).toContain('Ticket passed');
    expect(text()).toContain('You fixed it in 34 min.');
    expect(text()).toContain('3 of 3 open');
    expect(button('Show hint 1')).toBeUndefined();
  });

  it('shows practice mode with the plain start command', async () => {
    await mount(ticket({ progress: progress({ state: 'revisit', first_passed_at: new Date(Date.now() - 2 * 86_400_000).toISOString(), passed_seconds: 2040 }) }));
    expect(text()).toContain('Practice mode');
    expect(text()).toContain('34 min');
    expect(container.querySelector(`button[aria-label="Copy command: dojo start ${ID}"]`)).not.toBeNull();
  });
});

describe('Ticket failures', () => {
  it('says a locked hint is locked', async () => {
    await mount(ticket({ hints: [{ n: 1, text: HINTS[0]! }, { n: 2, text: HINTS[1]! }] }));
    await act(async () => button('Show hint 3')!.click());
    await settle();
    expect(text()).toContain('Open the earlier hint first.');
  });

  it('blocks reads and keeps the hint button disabled after a 429 until Retry-After', async () => {
    limited = true;
    await mount(ticket({ hints: [{ n: 1, text: HINTS[0]! }] }));
    await act(async () => button('Show hint 2')!.click());
    await settle();
    expect(text()).toContain('Too many requests');
    expect(button('Show hint 2')!.disabled).toBe(true);
    expect(readBlockedUntil()).toBeGreaterThan(Date.now());
    await act(async () => new Promise((r) => setTimeout(r, 1100)));
    expect(button('Show hint 2')!.disabled).toBe(false);
  });

  it('reports a missing ticket with a way back', async () => {
    await mount(404);
    expect(container.querySelector('[role=alert]')?.textContent).toContain('could not find that ticket');
    expect(container.querySelector('a[href="/path"]')).not.toBeNull();
  });

  it('rejects a malformed id without a request', async () => {
    await mount(ticket(), '/t/nope');
    expect(container.querySelector('[role=alert]')?.textContent).toContain('could not find that ticket');
  });

  it('redirects to sign-in when the session ended', async () => {
    await mount(401);
    expect(router.state.location.pathname).toBe('/signin');
  });

  it('keeps a heading while loading', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => (url === '/v1/me' ? Promise.resolve(Response.json(me)) : new Promise<Response>(() => {}))));
    router = createMemoryRouter(routes, { initialEntries: [`/t/${ID}`] });
    await act(async () => {
      root.render(
        <QueryClientProvider client={new QueryClient()}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
    });
    await settle();
    expect(container.querySelector('h1')?.textContent).toBe('Ticket');
    expect(text()).toContain('Loading the ticket...');
  });
});
