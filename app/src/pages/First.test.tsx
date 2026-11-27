// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock, type TicketProgress, type Today } from '../lib/read';
import { firstView } from '../lib/firstTicket';
import type { CommitState, NextStep, Onboarding } from '../lib/onboarding';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const learner = { id: '1', username: 'irene', github_login: 'irene-asir', email: 'irene@example.com', language: 'en', usage_stats: true, analytics_consent: null };
const FIRST = { id: 'foundations/terminal-01', title: 'Who is filling the disk?' };
const COMMIT_URL = 'https://github.com/irene-asir/dojo/commit/4c1e9a0ffff';

class FakeES {
  static all: FakeES[] = [];
  readyState = 1;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>();
  constructor(readonly url: string) {
    FakeES.all.push(this);
  }
  addEventListener(name: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn]);
  }
  close() {
    this.readyState = 2;
  }
  emit(name: string, data: object = {}) {
    for (const fn of this.listeners.get(name) ?? []) fn({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
}

type FirstTicket = NonNullable<Onboarding['first_ticket']>;

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let urls: string[];
let written: string[];
let onboarding: Onboarding;
let today: Today;
let progress: TicketProgress | null;

const ob = (next: NextStep, ticket: Partial<FirstTicket> | null = {}, extra: Partial<Onboarding> = {}): Onboarding => ({
  next_step: next,
  path: { slug: 'systems-devops', chosen: true },
  cli_connected: true,
  first_ticket: ticket && { ...FIRST, state: null, commit_state: null, ...ticket },
  ...extra,
});
const noTicketToday: Today = { current: null, next: null, role: null, all_passed: false };
const prog = (extra: Partial<TicketProgress> = {}): TicketProgress => ({
  state: 'started',
  lab: 'local',
  lab_state: 'starting',
  check_count: 0,
  last_check_ok: null,
  last_check_at: null,
  last_check_passed: null,
  last_check_total: null,
  last_check_output: null,
  first_passed_at: null,
  passed_seconds: null,
  passed_commit: null,
  ...extra,
});

function setup(state: Onboarding, p: TicketProgress | null = null) {
  urls = [];
  written = [];
  FakeES.all = [];
  onboarding = state;
  today = noTicketToday;
  progress = p;
  vi.stubGlobal('EventSource', FakeES);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn(async (t: string) => void written.push(t)) } });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      urls.push(url);
      if (url === '/v1/me') return Response.json(learner);
      if (url === '/v1/onboarding') return Response.json(onboarding);
      if (url === '/v1/today') return Response.json(today);
      if (url === `/v1/tickets/${FIRST.id}`) return Response.json({ ...FIRST, role: 'foundations', est_minutes: 20, tags: [], brief: `Brief for ${FIRST.id}.`, hint_count: 0, hints: [], progress });
      return new Response(null, { status: 404 });
    }),
  );
}

/** Polls until the condition holds, so no assertion runs before TanStack Query has notified. */
async function until(check: () => boolean, tries = 300) {
  for (let i = 0; i < tries && !check(); i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
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

const text = () => container.textContent ?? '';
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent?.startsWith(name));
const click = (el: Element | undefined) => act(async () => (el as HTMLElement).click());
const progressEvent = (p: Partial<TicketProgress> & { state: TicketProgress['state'] }) =>
  act(async () =>
    FakeES.all.at(-1)?.emit('progress', {
      item_id: FIRST.id,
      state: p.state,
      lab_state: p.lab_state ?? null,
      lab: 'local',
      check_count: p.check_count ?? null,
      last_check_ok: p.last_check_ok ?? null,
      last_check_at: p.last_check_at ?? null,
    }),
  );
const copy = (command: string) => container.querySelector(`button[aria-label="Copy command: ${command}"]`);
const reads = (url: string) => urls.filter((u) => u === url).length;
const startCommand = `dojo start ${FIRST.id}`;

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

describe('firstView', () => {
  it('settles passed from the onboarding state alone, and reads the lab from the progress row', () => {
    expect(firstView(false, null, null)).toBe('start');
    expect(firstView(true, null, null)).toBe('hosted');
    expect(firstView(true, 'started', prog({ lab_state: 'running' }))).toBe('running');
    expect(firstView(false, 'started', null)).toBe('starting');
    expect(firstView(false, null, prog({ lab_state: null }))).toBe('starting');
    expect(firstView(false, 'started', prog({ lab_state: 'running' }))).toBe('running');
    expect(firstView(false, 'started', prog({ lab_state: 'running', check_count: 1, last_check_ok: false }))).toBe('checked');
    for (const lab_state of ['failed', 'stopped', 'expired']) expect(firstView(false, 'started', prog({ lab_state }))).toBe('lost');
    expect(firstView(false, 'passed', null)).toBe('passed');
    expect(firstView(false, 'revisit', null)).toBe('passed');
    expect(firstView(true, 'passed', null)).toBe('passed');
    expect(firstView(false, 'started', prog({ state: 'passed' }))).toBe('passed');
  });
});

describe('O5 First ticket', () => {
  it('copies dojo start for the first_ticket the API names, never a hardcoded id', async () => {
    setup(ob('o5', { id: 'foundations/other-07', title: 'Another first ticket' }));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        urls.push(url);
        if (url === '/v1/me') return Response.json(learner);
        if (url === '/v1/onboarding') return Response.json(onboarding);
        return new Response(null, { status: 404 });
      }),
    );
    await mount('/start/first');
    await until(() => copy('dojo start foundations/other-07') !== null);
    expect(text()).toContain('Another first ticket');
    await click(copy('dojo start foundations/other-07')!);
    expect(written).toEqual(['dojo start foundations/other-07']);
    expect(text()).toContain('Waiting for dojo start on your computer...');
    expect(button('Open Today')?.disabled).toBe(true);
    expect(container.querySelector('[aria-current=step]')?.textContent).toContain('First ticket');
  });

  it('shows the ticket text and follows the lab from starting to ready to a failed check', async () => {
    setup(ob('o5'));
    await mount('/start/first');
    await until(() => copy(startCommand) !== null);
    await until(() => text().includes(`Brief for ${FIRST.id}.`));

    onboarding = ob('o5', { state: 'started' });
    progress = prog();
    await progressEvent({ state: 'started', lab_state: 'starting' });
    await until(() => text().includes('Starting your lab...'));
    expect(copy(startCommand)).toBeNull();
    expect(text()).toContain('Who is filling the disk?');

    progress = prog({ lab_state: 'running' });
    await progressEvent({ state: 'started', lab_state: 'running' });
    await until(() => text().includes('Lab ready.'));
    expect(copy('dojo ssh')).not.toBeNull();
    expect(copy('dojo check')).not.toBeNull();

    progress = prog({ lab_state: 'running', check_count: 1, last_check_ok: false, last_check_at: '2026-11-27T09:00:00Z' });
    await progressEvent({ state: 'started', lab_state: 'running', check_count: 1, last_check_ok: false, last_check_at: '2026-11-27T09:00:00Z' });
    await until(() => text().includes('Checked: not passing yet.'));
    expect(button('Open Today')?.disabled).toBe(true);
  });

  it('reloads into a started ticket without losing it', async () => {
    setup(ob('o5', { state: 'started' }), prog({ lab_state: 'running' }));
    await mount('/start/first');
    await until(() => text().includes('Lab ready.'));
    expect(text()).toContain('Who is filling the disk?');
    expect(copy(startCommand)).toBeNull();
  });

  it('reads a started ticket as starting until its lab row arrives', async () => {
    setup(ob('o5', { state: 'started' }));
    await mount('/start/first');
    await until(() => text().includes('Starting your lab...'));
  });

  it('offers dojo start again when the lab stopped', async () => {
    setup(ob('o5', { state: 'started' }), prog({ lab_state: 'expired' }));
    await mount('/start/first');
    await until(() => text().includes('The lab stopped. Start it again.'));
    expect(copy(startCommand)).not.toBeNull();
  });

  it.each([
    ['pending', 'Commit pending'],
    ['verified', 'Verified: your signed commit is on GitHub.'],
    ['unverified', 'Could not verify the commit'],
    [null, ''],
  ] as [CommitState | null, string][])('draws the green pass screen from the onboarding read alone on a reload after the pass (commit %s)', async (commit_state, line) => {
    setup(ob('o5', { state: 'passed', commit_state }));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        urls.push(url);
        if (url === '/v1/me') return Response.json(learner);
        if (url === '/v1/onboarding') return Response.json(onboarding);
        return new Response(null, { status: 404 });
      }),
    );
    await mount('/start/first');
    await until(() => text().includes('Passed.'));
    expect(text()).toContain('Who is filling the disk?');
    expect(copy(startCommand)).toBeNull();
    expect(button('Open Today')?.disabled).toBe(false);
    if (line) expect(text()).toContain(line);
    else expect(text()).not.toMatch(/Commit pending|Verified|Could not verify/);
  });

  it('links the verified commit when the ticket row has its address', async () => {
    setup(ob('o5', { state: 'passed', commit_state: 'verified' }), prog({ state: 'passed', passed_commit_url: COMMIT_URL, passed_commit_state: 'verified' }));
    await mount('/start/first');
    await until(() => container.querySelector(`a[href="${COMMIT_URL}"]`) !== null);
  });

  it('turns green on the pass live, shows the commit line as it settles, then opens Today with the day-one welcome', async () => {
    setup(ob('o5', { state: 'started' }), prog({ lab_state: 'running' }));
    await mount('/start/first');
    await until(() => text().includes('Lab ready.'));

    today = { first_run: { ...FIRST, first_passed_at: new Date().toISOString(), passed_commit_state: 'pending' },
      current: null,
      next: { id: 'foundations/terminal-02', title: 'Where did my file go?', state: null, lab_state: null },
      role: { slug: 'foundations', title: 'Foundations', tickets_passed: 1, tickets_total: 4 },
      all_passed: false,
    };
    onboarding = ob('done', { state: 'passed', commit_state: 'pending' });
    progress = prog({ state: 'passed', lab_state: 'running' });
    await progressEvent({ state: 'passed', lab_state: 'running' });
    await until(() => text().includes('Passed.'));
    await until(() => text().includes('Commit pending'));
    expect(button('Open Today')?.disabled).toBe(false);

    onboarding = ob('done', { state: 'passed', commit_state: 'verified' });
    progress = prog({ state: 'passed', passed_commit_url: COMMIT_URL, passed_commit_state: 'verified' });
    await progressEvent({ state: 'passed' });
    await until(() => container.querySelector(`a[href="${COMMIT_URL}"]`) !== null);
    expect(text()).toContain('Verified');

    await click(button('Open Today'));
    await until(() => router.state.location.pathname === '/today');
    await until(() => container.querySelector('section[aria-label="Your first ticket is done"]') !== null);
    expect(router.state.location.pathname).toBe('/today');
  });

  it('keeps the pass screen when the page is revisited after onboarding, and Open Today stays enabled', async () => {
    setup(ob('done', { state: 'passed', commit_state: 'verified' }));
    await mount('/start/first');
    await until(() => text().includes('Passed.'));
    expect(button('Open Today')?.disabled).toBe(false);
    expect(copy(startCommand)).toBeNull();
  });

  it('enables Open Today whenever onboarding is done, whatever the ticket row says', async () => {
    setup(ob('done', { state: 'started' }), prog({ lab_state: 'running' }));
    await mount('/start/first');
    await until(() => text().includes('Lab ready.'));
    expect(button('Open Today')?.disabled).toBe(false);
  });

  it('refreshes the onboarding read before it navigates, so a stale step cannot bounce the learner back', async () => {
    setup(ob('o5', { state: 'passed', commit_state: 'pending' }));
    await mount('/start/first');
    await until(() => button('Open Today')?.disabled === false);
    onboarding = ob('done', { state: 'passed', commit_state: 'pending' });
    const before = reads('/v1/onboarding');
    await click(button('Open Today'));
    await until(() => router.state.location.pathname === '/today');
    expect(reads('/v1/onboarding')).toBeGreaterThan(before);
    await act(async () => new Promise((r) => setTimeout(r, 30)));
    expect(router.state.location.pathname).toBe('/today');
  });

  it('tells a hosted-default learner that hosted labs are coming soon and offers no start command', async () => {
    setup(ob('o5', {}, { hosted_default: true }));
    await mount('/start/first');
    await until(() => text().includes('Hosted labs are coming soon'));
    expect(container.querySelector('button[aria-label^="Copy command: dojo start"]')).toBeNull();
    expect([...container.querySelectorAll('a')].find((a) => a.textContent === 'Open settings')?.getAttribute('href')).toBe('/settings');
    expect(button('Open Today')?.disabled).toBe(true);
  });

  it('still shows a pass to a hosted-default learner', async () => {
    setup(ob('o5', { state: 'passed' }, { hosted_default: true }));
    await mount('/start/first');
    await until(() => text().includes('Passed.'));
    expect(text()).not.toContain('Hosted labs are coming soon.');
  });

  it('can be retried when no first ticket comes back', async () => {
    setup(ob('o5', null));
    await mount('/start/first');
    await until(() => button('Retry') !== undefined);
    expect(text()).toContain('Could not find your first ticket.');
    onboarding = ob('o5');
    await click(button('Retry'));
    await until(() => copy(startCommand) !== null);
  });

  it('refetches on resync', async () => {
    setup(ob('o5'));
    await mount('/start/first');
    await until(() => copy(startCommand) !== null);
    const before = reads('/v1/onboarding');
    await act(async () => FakeES.all.at(-1)?.emit('resync'));
    await until(() => reads('/v1/onboarding') > before);
  });
});
