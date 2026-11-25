// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock, type LearnerPath, type Ticket, type Today } from '../lib/read';
import { ticketHref } from './Today';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'irene', email: null, language: 'en', usage_stats: true, analytics_consent: null };
const role = { slug: 'support-l1', title: 'Support L1', tickets_passed: 7, tickets_total: 12 };
const perms = { id: 'support-l1/perms-03', title: 'The shared folder stopped accepting uploads', state: 'started' as const, lab_state: 'running' };
const ssh = { id: 'support-l1/ssh-01', title: "The new server won't let anyone in", state: null, lab_state: null };

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;

const sudo = { id: 'support-l1/sudo-01', title: 'Who gave the intern sudo?', state: 'passed' as const, lab_state: null };
const path: LearnerPath = {
  path: 'support',
  chosen: true,
  roles: [{ slug: 'support-l1', title: 'Support L1', summary: '', locked: false, passed: false, tickets_passed: 7, tickets_total: 12, items: [{ ...sudo, order: 1 }] }],
};

function ticket(id: string, extra: Partial<Ticket> = {}): Ticket {
  return {
    id,
    title: id,
    role: 'support-l1',
    est_minutes: 15,
    tags: [],
    brief: 'Ticket OPS-114, from Elena (finance):\n\nHi! The team cannot save files in /srv/shared.\n\nThanks.',
    hint_count: 2,
    hints: [],
    progress: null,
    ...extra,
  };
}

const passedProgress = (at: string, commit: string) => ({
  state: 'passed' as const,
  lab: null,
  lab_state: null,
  check_count: 1,
  last_check_ok: true,
  last_check_at: at,
  last_check_passed: 3,
  last_check_total: 3,
  last_check_output: null,
  first_passed_at: at,
  passed_seconds: 60,
  passed_commit: commit,
});

/** `today` is the GET /v1/today answer: a body, or a status code. `extra` answers /v1/path and /v1/tickets/*. */
function stubApi(today: Today | number, extra: { path?: LearnerPath; tickets?: Record<string, Ticket> } = {}) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url === '/v1/me') return Response.json(me);
      if (url === '/v1/today') return typeof today === 'number' ? new Response(null, { status: today }) : Response.json(today);
      if (url === '/v1/path' && extra.path) return Response.json(extra.path);
      const id = url.startsWith('/v1/tickets/') ? decodeURIComponent(url.slice('/v1/tickets/'.length)) : '';
      if (extra.tickets?.[id]) return Response.json(extra.tickets[id]);
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
    const list = container.querySelector('section[aria-label="Recent and next"]')!;
    expect(list.textContent).toContain(perms.title);
    expect(list.querySelector<HTMLAnchorElement>('a[href="/t/support-l1/ssh-01"]')).not.toBeNull();
  });

  it('has no search or bell the app cannot back, and an avatar to Settings', () => {
    expect(container.querySelector('header button[aria-label^="Notifications"]')).toBeNull();
    expect(container.textContent).not.toContain('Search or run a command');
    const avatar = container.querySelector('header a[href="/settings"]')!;
    expect(avatar.getAttribute('aria-label')).toBe('Settings, @irene');
    expect(avatar.textContent).toBe('i');
  });

  it('shows an empty belt that claims no color or count', () => {
    expect(text()).toContain('Your belt');
    expect(text()).toContain('Marks come from how you work, not from tickets.');
    expect(text()).not.toMatch(/white belt|of 4 marks/i);
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

describe('Today with history', () => {
  const at = new Date(Date.now() - 3 * 86_400_000).toISOString();
  beforeEach(async () => {
    stubApi(
      { current: perms, next: ssh, role, all_passed: false },
      {
        path,
        tickets: {
          [perms.id]: ticket(perms.id, {
            progress: { ...passedProgress(new Date(Date.now() - 3 * 60_000).toISOString(), 'x'), state: 'started', lab_state: 'running', last_check_ok: false, last_check_passed: 1, last_check_total: 3, first_passed_at: null, passed_commit: null, last_check_output: 'nobody else can' },
          }),
          [sudo.id]: ticket(sudo.id, { progress: passedProgress(at, '3f9a1c2deadbeef') }),
        },
      },
    );
    await mount();
  });

  it('shows the client message parsed from the brief', () => {
    const msg = container.querySelector('section[aria-label="Message from Elena"]')!;
    expect(msg.textContent).toContain('Elena · Finance');
    expect(msg.textContent).toContain('OPS-114');
    expect(msg.textContent).toContain('The team cannot save files in /srv/shared.');
    expect(msg.textContent).not.toContain('Thanks.');
  });

  it('puts the last check in Your lab', () => {
    const lab = container.querySelector('section[aria-label="Your lab"]')!;
    expect(lab.textContent).toContain('3 min ago');
    expect(lab.textContent).toContain('1 of 3 checks pass');
  });

  it('lists the last passed ticket with its commit before now and next', () => {
    const rows = [...container.querySelectorAll('section[aria-label="Recent and next"] li')].map((li) => li.textContent);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain('Passed');
    expect(rows[0]).toContain('support-l1/sudo-01');
    expect(rows[0]).toContain('3f9a1c2');
    expect(rows[1]).toContain('Now');
    expect(rows[2]).toContain('Next');
  });

  it('has no Activity panel on the returning view', () => {
    expect(container.querySelector('section[aria-label="Activity"]')).toBeNull();
  });

  it('shows the phone last-check card with its count', () => {
    const last = container.querySelector('section[aria-label="Last check"]')!;
    expect(last.textContent).toContain('1/3');
    expect(last.textContent).toContain('nobody else can');
  });

  it('offers a hint link when the ticket has hints', () => {
    expect(link('Open the ticket for hints')?.getAttribute('href')).toBe('/t/support-l1/perms-03');
  });
});

describe('Today read budget', () => {
  it('reads one passed ticket however many are passed', async () => {
    const items = [1, 2, 3, 4].map((n) => ({ ...sudo, id: `support-l1/sudo-0${n}`, order: n }));
    const many = { ...path, roles: [{ ...path.roles[0]!, items }] };
    stubApi({ current: perms, next: ssh, role, all_passed: false }, { path: many, tickets: { 'support-l1/sudo-04': ticket('support-l1/sudo-04', { progress: passedProgress(new Date().toISOString(), 'dddddd4') }) } });
    await mount();
    const calls = (fetch as unknown as { mock: { calls: string[][] } }).mock.calls.map((c) => c[0]);
    expect(calls.filter((u) => u?.startsWith('/v1/tickets/support-l1/sudo'))).toEqual(['/v1/tickets/support-l1/sudo-04']);
    expect(container.querySelector('section[aria-label="Recent and next"]')?.textContent).toContain('dddddd4');
  });
});

describe('Today on day one', () => {
  it('welcomes the learner after the first ticket passed today', async () => {
    const one = { ...path, roles: [{ ...path.roles[0]!, items: [{ ...sudo, order: 1 }] }] };
    stubApi(
      { current: null, next: ssh, role: { ...role, tickets_passed: 1 }, all_passed: false },
      { path: one, tickets: { [sudo.id]: ticket(sudo.id, { progress: passedProgress(new Date().toISOString(), '4c1e9a0ffff') }), [ssh.id]: ticket(ssh.id) } },
    );
    await mount();
    expect(container.querySelector('h1')?.textContent).toBe('Welcome to the team, @irene.');
    expect(text()).toContain('day one · done');
    expect(container.querySelector('section[aria-label="Your first ticket is done"]')?.textContent).toContain('4c1e9a0');
    expect(container.querySelector('section[aria-label="Next up"]')?.textContent).toContain('about 15 min');
    expect(link('Start ssh-01')).toBeDefined();
    expect(container.querySelector('section[aria-label="Activity"]')?.textContent).toContain('One so far: 4c1e9a0');
    expect(container.querySelector('section[aria-label="Your first ticket is done"]')?.textContent).toContain('11 tickets to go in Support L1.');
  });

  it('falls back to the plain headline when the ticket read fails, without claiming a commit', async () => {
    const one = { ...path, roles: [{ ...path.roles[0]!, items: [{ ...sudo, order: 1 }] }] };
    stubApi({ current: null, next: ssh, role: { ...role, tickets_passed: 1 }, all_passed: false }, { path: one, tickets: {} });
    await mount();
    await act(async () => new Promise((r) => setTimeout(r, 1200)));
    expect(container.querySelector('h1')?.textContent).toBe('Ready for the next ticket.');
    expect(container.querySelector('section[aria-label="Activity"]')).toBeNull();
  });

  it('shows no headline until the one read settles', async () => {
    const one = { ...path, roles: [{ ...path.roles[0]!, items: [{ ...sudo, order: 1 }] }] };
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url === '/v1/me') return Promise.resolve(Response.json(me));
        if (url === '/v1/today') return Promise.resolve(Response.json({ current: null, next: ssh, role: { ...role, tickets_passed: 1 }, all_passed: false }));
        if (url === '/v1/path') return Promise.resolve(Response.json(one));
        return new Promise<Response>(() => {});
      }),
    );
    await mount();
    expect(container.querySelector('h1')).toBeNull();
    expect(text()).toContain('Loading today...');
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
