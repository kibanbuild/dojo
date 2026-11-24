// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock, type Today } from '../lib/read';
import { ticketHref } from './Today';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'irene', email: null, language: 'en', usage_stats: true, analytics_consent: null };
const role = { slug: 'support-l1', title: 'Support L1', tickets_passed: 7, tickets_total: 12 };
const perms = { id: 'support-l1/perms-03', title: 'The shared folder stopped accepting uploads', state: 'started' as const, lab_state: 'running' };
const ssh = { id: 'support-l1/ssh-01', title: "The new server won't let anyone in", state: null, lab_state: null };

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;

/** `today` is the GET /v1/today answer: a body, or a status code. */
function stubApi(today: Today | number) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url === '/v1/me') return Response.json(me);
      if (url === '/v1/today') return typeof today === 'number' ? new Response(null, { status: today }) : Response.json(today);
      return new Response(null, { status: 404 });
    }),
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

async function mount(path = '/today') {
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
const link = (name: string) => [...container.querySelectorAll('a')].find((a) => a.textContent === name);

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

describe('ticketHref', () => {
  it('keeps the slash and escapes each segment', () => {
    expect(ticketHref('support-l1/perms-03')).toBe('/t/support-l1/perms-03');
    expect(ticketHref('a b/c?d')).toBe('/t/a%20b/c%3Fd');
  });
});

describe('Today with a ticket in progress', () => {
  beforeEach(async () => {
    stubApi({ current: perms, next: ssh, role, all_passed: false });
    await mount();
  });

  it('shows the ticket, lab state and a link into it', () => {
    expect(container.querySelector('h1')?.textContent).toBe(perms.title);
    expect(text()).toContain('In progress');
    expect(text()).toContain('support-l1/perms-03 · lab running');
    expect(link('Continue the ticket')?.getAttribute('href')).toBe('/t/support-l1/perms-03');
  });

  it('lists now and next', () => {
    const list = container.querySelector('section[aria-label="Now and next"]')!;
    expect(list.textContent).toContain(perms.title);
    expect(list.querySelector<HTMLAnchorElement>('a[href="/t/support-l1/ssh-01"]')).not.toBeNull();
  });

  it('draws role progress as a progressbar', () => {
    const bar = container.querySelector('[role=progressbar]')!;
    expect(bar.getAttribute('aria-valuenow')).toBe('7');
    expect(bar.getAttribute('aria-valuemax')).toBe('12');
    expect(bar.getAttribute('aria-label')).toBe('Support L1, 7 of 12 tickets');
    expect(text()).toContain('5 tickets to go.');
  });

  it('has the nav with Today current, in the frame landmarks', () => {
    expect(container.querySelector('header')).not.toBeNull();
    expect(container.querySelector('main')).not.toBeNull();
    expect(container.querySelector('nav[aria-label="Primary"] [aria-current=page]')?.textContent).toBe('Today');
    expect(container.querySelector('nav[aria-label="Primary, tabs"] a[href="/path"]')).not.toBeNull();
  });
});

describe('Today without a ticket in progress', () => {
  it('offers the next ticket', async () => {
    stubApi({ current: null, next: ssh, role, all_passed: false });
    await mount();
    expect(container.querySelector('h1')?.textContent).toBe('Ready for the next ticket.');
    expect(link('Start ssh-01')?.getAttribute('href')).toBe('/t/support-l1/ssh-01');
    expect(link('See the whole path')?.getAttribute('href')).toBe('/path');
  });

  it('says every ticket is passed', async () => {
    stubApi({ current: null, next: null, role: { ...role, tickets_passed: 12 }, all_passed: true });
    await mount();
    expect(container.querySelector('h1')?.textContent).toBe('Every ticket is passed.');
    expect(link('See your path')).toBeDefined();
    expect(container.querySelector('[role=progressbar]')?.getAttribute('aria-valuenow')).toBe('12');
  });

  it('shows a plain empty state with no data', async () => {
    stubApi({ current: null, next: null, role: null, all_passed: false });
    await mount();
    expect(container.querySelector('h1')?.textContent).toBe('Nothing to work on yet.');
  });
});

describe('copy command', () => {
  const copyBtn = () => container.querySelector<HTMLButtonElement>('button[aria-label="Copy command: dojo check"]')!;
  const notice = () => copyBtn().parentElement!.querySelector('[role=status]')!;

  beforeEach(async () => {
    stubApi({ current: perms, next: null, role, all_passed: false });
    await mount();
  });

  it('announces Copied in a live region outside the button', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await act(async () => copyBtn().click());
    expect(writeText).toHaveBeenCalledWith('dojo check');
    expect(notice().textContent).toBe('Copied');
    expect(copyBtn().contains(notice())).toBe(false);
  });

  it('announces a clipboard failure the same way', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => Promise.reject(new Error('denied'))) } });
    await act(async () => copyBtn().click());
    expect(notice().textContent).toBe('Copy failed, select the command instead');
  });
});

describe('Today failures', () => {
  it('keeps a Today heading while loading', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => (url === '/v1/me' ? Promise.resolve(Response.json(me)) : new Promise<Response>(() => {}))));
    await mount();
    expect(container.querySelector('h1')?.textContent).toBe('Today');
    expect(text()).toContain('Loading today...');
  });

  it('redirects to sign-in when the session ended', async () => {
    stubApi(401);
    await mount();
    expect(router.state.location.pathname).toBe('/signin');
  });

  it('shows a retry on a server error and loads on retry', async () => {
    stubApi(500);
    await mount();
    await act(async () => new Promise((r) => setTimeout(r, 1200)));
    expect(container.querySelector('[role=alert]')?.textContent).toContain('Could not load this');
    expect(container.querySelector('h1')?.textContent).toBe('Today');
    stubApi({ current: perms, next: null, role, all_passed: false });
    const retry = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Retry')!;
    await act(async () => retry.click());
    await settle();
    expect(container.querySelector('h1')?.textContent).toBe(perms.title);
  });

  it('says the account is suspended without a retry', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url === '/v1/me' ? Response.json(me) : Response.json({ error: 'account_suspended' }, { status: 403 }),
      ),
    );
    await mount();
    expect(container.querySelector('[role=alert]')?.textContent).toContain('suspended');
    expect([...container.querySelectorAll('button')].some((b) => b.textContent === 'Retry')).toBe(false);
  });
});
