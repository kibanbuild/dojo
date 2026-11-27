// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock } from '../lib/read';
import type { CheckStatus, DoctorCheck, NextStep, Onboarding } from '../lib/onboarding';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const learner = { id: '1', username: 'irene', github_login: 'irene', email: 'irene@example.com', language: 'en', usage_stats: true, analytics_consent: null };

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

interface Call {
  method: string;
  url: string;
  body: unknown;
}

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let calls: Call[];
let onboarding: Onboarding;
let patchStatus: number;
let meBody: object | null;

function ob(next: NextStep, extra: Partial<Onboarding> = {}): Onboarding {
  return { next_step: next, env: 'pro', public_origin: 'https://dojo.example', path: { slug: 'systems-devops', chosen: true }, cli_connected: false, first_ticket: null, ...extra };
}

const check = (id: string, status: CheckStatus = 'ok', text = `${id} text`, fix = ''): DoctorCheck => ({ id, status, text, fix });
const REQUIRED = ['memory', 'disk', 'cpu', 'virtualization', 'vm_manager', 'git'];
const allOk = () => REQUIRED.map((id) => check(id));

function setup(state: Onboarding, me: object | null = learner) {
  calls = [];
  FakeES.all = [];
  onboarding = state;
  patchStatus = 200;
  meBody = me;
  vi.stubGlobal('EventSource', FakeES);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      calls.push({ method, url, body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined });
      if (url === '/v1/me' && method === 'GET') return meBody ? Response.json(meBody) : new Response(null, { status: 401 });
      if (url === '/v1/me' && method === 'PATCH') {
        if (patchStatus !== 200) return Response.json({ error: 'bad_request' }, { status: patchStatus });
        onboarding = { ...onboarding, next_step: 'o4', hosted_default: true };
        return Response.json(learner);
      }
      if (url === '/v1/onboarding') return Response.json(onboarding);
      if (url === '/v1/reports') return Response.json({ id: 'r1' }, { status: 201 });
      return new Response(null, { status: 404 });
    }),
  );
}

/** Polls until the condition holds, so no assertion runs before TanStack Query has notified. */
async function until(check: () => boolean, tries = 100) {
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
const emit = (name: string, data?: object) => act(async () => FakeES.all.at(-1)?.emit(name, data));
const onboardingReads = () => calls.filter((c) => c.url === '/v1/onboarding').length;
const ready = (name: string) => until(() => button(name) !== undefined);

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

describe('O2 Install', () => {
  it('shows the real install line from public_origin and waits for dojo login', async () => {
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => text().includes('curl -fsSL https://dojo.example/install | sh'));
    expect(text()).toContain('Waiting for dojo login on your computer');
    expect(button('Continue')?.disabled).toBe(true);
    expect(text()).toContain('dojo telemetry off');
    expect(container.querySelector('button[aria-label="Copy command: dojo login"]')).not.toBeNull();
  });

  it('copies the install line', async () => {
    setup(ob('o2'));
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await mount('/start/install');
    await until(() => container.querySelector('button[aria-label^="Copy command: curl"]') !== null);
    await click(container.querySelector('button[aria-label^="Copy command: curl"]') ?? undefined);
    expect(writeText).toHaveBeenCalledWith('curl -fsSL https://dojo.example/install | sh');
  });

  it('adds the WSL note on the Windows tab and the local-stack note only when env is local', async () => {
    setup(ob('o2', { env: 'local', public_origin: 'http://localhost:8080/' }));
    await mount('/start/install');
    await until(() => text().includes('curl -fsSL http://localhost:8080/install | sh'));
    expect(text()).toContain('Local stack: build the CLI');
    expect(text()).not.toContain('WSL2 terminal');
    await click(button('Windows'));
    expect(text()).toContain('Open your WSL2 terminal first');
    expect(button('Windows')?.getAttribute('aria-selected')).toBe('true');
  });

  it('follows the tabs pattern: linked panel, roving tabindex, arrow, Home and End keys', async () => {
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => text().includes('/install | sh'));
    const tabs = () => [...container.querySelectorAll<HTMLElement>('[role=tab]')];
    const key = (k: string) => act(async () => void document.activeElement?.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })));
    const selected = () => tabs().find((t) => t.getAttribute('aria-selected') === 'true')?.textContent;
    const panel = container.querySelector('[role=tabpanel]');
    expect(tabs().every((t) => t.getAttribute('aria-controls') === panel?.id)).toBe(true);
    expect(panel?.getAttribute('aria-labelledby')).toBe(tabs().find((t) => t.textContent === 'macOS')?.id);
    expect(tabs().map((t) => t.tabIndex)).toEqual([-1, 0, -1]);
    act(() => tabs()[1]!.focus());
    await key('ArrowRight');
    expect(selected()).toBe('Linux');
    expect(document.activeElement).toBe(tabs()[2]);
    await key('ArrowRight');
    expect(selected()).toBe('Windows (WSL)');
    await key('ArrowLeft');
    expect(selected()).toBe('Linux');
    await key('Home');
    expect(selected()).toBe('Windows (WSL)');
    await key('End');
    expect(selected()).toBe('Linux');
    expect(tabs().map((t) => t.tabIndex)).toEqual([-1, -1, 0]);
    expect(panel?.getAttribute('aria-labelledby')).toBe(tabs()[2]!.id);
  });

  it('omits the local-stack note outside local', async () => {
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => text().includes('/install | sh'));
    expect(text()).not.toContain('Local stack');
  });

  it('turns green and unlocks Continue when the login event arrives', async () => {
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => FakeES.all.length > 0 && text().includes('/install | sh'));
    // The login commits with the event, so the read that follows already says connected.
    onboarding = ob('o2', { cli_connected: true });
    await emit('login', { device_name: 'laptop', os: 'darwin', cli_version: '1.0.0', at: '2026-11-27T09:00:00Z' });
    await until(() => text().includes('Connected.'));
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('is green at once when a login happened before the page opened', async () => {
    setup(ob('o2', { cli_connected: true }));
    await mount('/start/install');
    await until(() => text().includes('Connected.'));
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('refreshes onboarding before moving on, so a stale next_step cannot send the learner back', async () => {
    setup(ob('o2', { cli_connected: true }));
    await mount('/start/install');
    await ready('Continue');
    await until(() => button('Continue')?.disabled === false);
    // The server moved on without the page hearing about it.
    onboarding = ob('o3', { cli_connected: true });
    await click(button('Continue'));
    await until(() => router.state.location.pathname === '/start/doctor');
  });

  it('shows the email banner only while no address is on file', async () => {
    setup(ob('o2'), { ...learner, email: null });
    await mount('/start/install');
    await until(() => container.querySelector('section[aria-label="Email address"]') !== null);
    act(() => root.unmount());
    root = createRoot(container);
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => text().includes('/install | sh'));
    expect(container.querySelector('section[aria-label="Email address"]')).toBeNull();
  });

  it('says so when the install line cannot be loaded and retries', async () => {
    setup(ob('o2'));
    onboarding = ob('o2', { public_origin: undefined });
    await mount('/start/install');
    await until(() => container.querySelector('[role=alert]') !== null);
    onboarding = ob('o2');
    await click(button('Retry'));
    await until(() => text().includes('/install | sh'));
  });

  it('tells the learner what to do when the code expired', async () => {
    setup(ob('o2'));
    await mount('/start/install');
    await until(() => text().includes('run dojo login again for a new one'));
  });
});

describe('O3 Doctor', () => {
  it('waits while no result exists', async () => {
    setup(ob('o3', { checks: [] }));
    await mount('/start/doctor');
    await until(() => text().includes('Not checked yet'));
    expect(text()).toContain('waiting');
    expect(button('Continue')?.disabled).toBe(true);
  });

  it('draws results that arrive on the doctor event, then fills their text from the read', async () => {
    setup(ob('o3', { checks: [] }));
    await mount('/start/doctor');
    await until(() => FakeES.all.length > 0 && text().includes('waiting'));
    const before = onboardingReads();
    onboarding = ob('o3', { checks: [check('memory', 'ok', '16 GB RAM'), check('vm_manager', 'fail', 'No VM manager found', 'brew install lima')] });
    await emit('doctor', { checks: [{ id: 'memory', status: 'ok' }, { id: 'vm_manager', status: 'fail' }] });
    await until(() => text().includes('1 to fix'));
    await until(() => onboardingReads() > before && text().includes('No VM manager found'), 300);
    expect(text()).toContain('-> brew install lima');
    expect(text()).toContain('16 GB RAM');
  });

  it('counts fail and warn lines to fix, and blocks Continue while one fails', async () => {
    setup(ob('o3', { checks: [...allOk().slice(0, 4), check('vm_manager', 'fail', 'No VM manager found', 'brew install lima'), check('git', 'ok'), check('git_identity', 'warn', 'Git has no name', 'git config --global user.name "Your Name"')] }));
    await mount('/start/doctor');
    await until(() => text().includes('2 to fix'));
    expect(button('Continue')?.disabled).toBe(true);
    expect(text()).toContain('git config --global user.name');
  });

  it('lets amber lines through', async () => {
    setup(ob('o3', { checks: [...allOk(), check('git_identity', 'warn', 'Git has no name', 'fix it')] }));
    await mount('/start/doctor');
    await until(() => text().includes('1 to fix'));
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('says all good and unlocks Continue when every required line passes', async () => {
    setup(ob('o3', { checks: allOk() }));
    await mount('/start/doctor');
    await until(() => text().includes('all good'));
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('keeps waiting while a required line has no result yet', async () => {
    setup(ob('o3', { checks: allOk().slice(0, 3) }));
    await mount('/start/doctor');
    await until(() => text().includes('memory text'));
    expect(text()).toContain('waiting');
    expect(button('Continue')?.disabled).toBe(true);
  });

  it('treats an advisory line that fails as amber: it never blocks and never offers the hosted way out', async () => {
    setup(ob('o3', { checks: [...allOk(), check('git_identity', 'fail', 'Git has no name', 'set it')] }));
    await mount('/start/doctor');
    await until(() => text().includes('1 to fix'));
    expect(button('Continue')?.disabled).toBe(false);
    expect(button("I can't fix this")).toBeUndefined();
    expect(container.querySelector('[aria-label="Your machine"]')?.textContent).toContain('Warning');
    expect(container.querySelector('[aria-label="Your machine"]')?.textContent).not.toContain('Not yet');
  });

  it('says so when the results cannot be loaded and retries', async () => {
    setup(ob('o3', { checks: [] }));
    const real = globalThis.fetch;
    let down = true;
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => (down && url === '/v1/onboarding' ? Promise.resolve(new Response(null, { status: 500 })) : real(url, init))));
    await mount('/start/doctor');
    await until(() => container.querySelector('[role=alert]') !== null);
    expect(text()).not.toContain('Not checked yet');
    down = false;
    await click(button('Retry'));
    await until(() => text().includes('Not checked yet'));
  });

  it('says a hosted lab is already chosen instead of asking again', async () => {
    setup(ob('o3', { checks: [], hosted_default: true }));
    await mount('/start/doctor');
    await until(() => text().includes('You already chose a hosted lab'));
    expect(text()).not.toContain('Choose this');
  });

  it('shows only the machine lines', async () => {
    setup(ob('o3', { checks: [...allOk(), check('ssh_key', 'fail', 'No key')] }));
    await mount('/start/doctor');
    await until(() => text().includes('all good'));
    expect(text()).not.toContain('No key');
  });

  it('refreshes onboarding before moving on, so a stale next_step cannot send the learner back', async () => {
    setup(ob('o3', { checks: allOk() }));
    await mount('/start/doctor');
    await until(() => button('Continue')?.disabled === false);
    onboarding = ob('o4', { checks: allOk() });
    await click(button('Continue'));
    await until(() => router.state.location.pathname === '/start/key');
  });

  it('offers the hosted lab once a failing line is called unfixable, and sets hosted_default', async () => {
    setup(ob('o3', { checks: [...allOk().slice(0, 4), check('vm_manager', 'fail', 'No admin rights', 'ask IT'), check('git')] }));
    await mount('/start/doctor');
    await until(() => button("I can't fix this") !== undefined);
    expect(button('Use a hosted lab')).toBeUndefined();
    await click(button("I can't fix this"));
    expect(text()).toContain('blocked');
    expect(text()).toContain('Hosted labs arrive later');
    expect(button('Continue')?.disabled).toBe(true);
    await click(button('Use a hosted lab'));
    await until(() => router.state.location.pathname === '/start/key');
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ hosted_default: true });
  });

  it('keeps the learner on the page and says why when hosted_default cannot be saved', async () => {
    setup(ob('o3', { checks: [check('vm_manager', 'fail', 'No admin rights', 'ask IT')] }));
    await mount('/start/doctor');
    await until(() => button("I can't fix this") !== undefined);
    await click(button("I can't fix this"));
    patchStatus = 400;
    await click(button('Use a hosted lab'));
    await until(() => text().includes('That value is not accepted'));
    expect(router.state.location.pathname).toBe('/start/doctor');
    expect(button('Use a hosted lab')?.disabled).toBe(false);
  });

  it('shows the blocked view to a learner who already chose a hosted lab', async () => {
    setup(ob('o3', { checks: [], hosted_default: true }));
    await mount('/start/doctor');
    await until(() => text().includes('Use a hosted lab instead'));
  });

  it('opens the setup help report from "I\'m stuck: get help"', async () => {
    setup(ob('o3', { checks: [] }));
    await mount('/start/doctor');
    await ready("I'm stuck");
    await click(button("I'm stuck"));
    expect(container.querySelector('[role=dialog]')?.textContent).toContain('Get help with setup');
    const field = container.querySelector('textarea') as HTMLTextAreaElement;
    expect(field).not.toBeNull();
    await click(button('Cancel'));
    expect(container.querySelector('[role=dialog]')).toBeNull();
  });
});
