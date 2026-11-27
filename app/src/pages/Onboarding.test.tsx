// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock } from '../lib/read';
import type { NextStep, Onboarding } from '../lib/onboarding';

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
let meBody: object | null;

function ob(next: NextStep, extra: Partial<Onboarding> = {}): Onboarding {
  return { next_step: next, env: 'pro', public_origin: 'https://dojo.example', path: { slug: 'systems-devops', chosen: true }, cli_connected: false, first_ticket: null, ...extra };
}

function setup(state: Onboarding, me: object | null = learner) {
  calls = [];
  FakeES.all = [];
  onboarding = state;
  meBody = me;
  vi.stubGlobal('EventSource', FakeES);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      calls.push({ method, url, body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined });
      if (url === '/v1/me' && method === 'GET') return meBody ? Response.json(meBody) : new Response(null, { status: 401 });
      if (url === '/v1/onboarding') return Response.json(onboarding);
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
