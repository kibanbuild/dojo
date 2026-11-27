// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { resetReadBlock } from '../lib/read';
import { keyReady } from '../lib/keyStep';
import type { CheckStatus, DoctorCheck, NextStep, Onboarding } from '../lib/onboarding';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const learner = { id: '1', username: 'irene', github_login: 'irene-asir', email: 'irene@example.com', language: 'en', usage_stats: true, analytics_consent: null };
const AGENTS = '# Mentor rules\n\nHelp the learner, never solve the ticket.\n';

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
}

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let calls: Call[];
let onboarding: Onboarding;
let confirmStatus: number;
let written: string[];

const check = (id: string, status: CheckStatus = 'ok', fix = ''): DoctorCheck => ({ id, status, text: `${id} text`, fix });
const REQUIRED = ['ssh_key', 'key_signing', 'key_auth', 'git_signing', 'repo', 'agents_md', 'ruleset'];
const allOk = () => REQUIRED.map((id) => check(id));
const ob = (next: NextStep, checks: DoctorCheck[]): Onboarding => ({
  next_step: next,
  public_origin: 'https://dojo.example',
  agents_md: AGENTS,
  path: { slug: 'systems-devops', chosen: true },
  cli_connected: true,
  first_ticket: { id: 'support-l1/terminal-01', title: 'Who is filling the disk?' },
  checks,
});

function setup(state: Onboarding) {
  calls = [];
  FakeES.all = [];
  written = [];
  confirmStatus = 204;
  onboarding = state;
  vi.stubGlobal('EventSource', FakeES);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn(async (t: string) => void written.push(t)) } });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      calls.push({ method, url });
      if (url === '/v1/me') return Response.json(learner);
      if (url === '/v1/onboarding') return Response.json(onboarding);
      if (url.startsWith('/v1/onboarding/confirmations/')) {
        if (confirmStatus !== 204) return new Response(null, { status: confirmStatus });
        const id = url.split('/').pop()!;
        const rest = (onboarding.checks ?? []).filter((c) => c.id !== id);
        onboarding = { ...onboarding, checks: method === 'PUT' ? [...rest, { id, status: 'ok', text: 'Confirmed by you', fix: '', source: 'learner' }] : rest };
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: 404 });
    }),
  );
}

/** Polls until the condition holds, so no assertion runs before TanStack Query has notified. */
async function until(check: () => boolean, tries = 200) {
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
const row = (title: string) => [...container.querySelectorAll('li')].find((li) => li.textContent?.includes(title));
const copyLabels = () => [...container.querySelectorAll('button[aria-label^="Copy command: "]')].map((b) => b.getAttribute('aria-label')!.slice('Copy command: '.length));
const onboardingReads = () => calls.filter((c) => c.url === '/v1/onboarding').length;
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

describe('keyReady', () => {
  it('needs a row for each required id and no fail; warn, unknown and the advisory steps never block', () => {
    expect(keyReady(undefined)).toBe(false);
    expect(keyReady(allOk().slice(1))).toBe(false);
    expect(keyReady(allOk())).toBe(true);
    expect(keyReady([...allOk().slice(1), check('ssh_key', 'fail')])).toBe(false);
    expect(keyReady([...allOk().slice(1), check('ssh_key', 'warn'), check('repo', 'unknown')])).toBe(true);
    expect(keyReady([...allOk(), check('two_factor', 'fail'), check('git_email_github', 'warn')])).toBe(true);
  });
});

describe('O4 SSH key and repo', () => {
  it('lists every step with the exact command and link to copy', async () => {
    setup(ob('o4', []));
    await mount('/start/key');
    await until(() => row('Create your SSH key') !== undefined);
    expect(copyLabels()).toEqual([
      'ssh-keygen -t ed25519 -C "irene-asir"',
      'cat ~/.ssh/id_ed25519.pub',
      'git config --global gpg.format ssh',
      'git config --global user.signingkey ~/.ssh/id_ed25519.pub',
      'git config --global commit.gpgsign true',
      'cd ~ && gh repo create dojo --public --clone',
      'cd ~/dojo && git add AGENTS.md && git commit -m "Add AGENTS.md" && git branch -M main && git push -u origin main',
    ]);
    expect(text()).toContain('irene-asir/dojo');
    const links = [...container.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    expect(links).toContain('https://github.com/settings/ssh/new');
    expect(links).toContain('https://github.com/irene-asir/dojo/settings/rules/new?target=branch');
    expect(links).toContain('https://github.com/settings/security');
    expect(links).toContain('https://github.com/irene-asir/dojo/settings/security_analysis');
    expect(container.querySelector('[aria-current=step]')?.textContent).toContain('SSH key');
    expect(button('Continue')?.disabled).toBe(true);
  });

  it('marks the authentication key as SSH-only and lets a learner with a key reuse it', async () => {
    setup(ob('o4', []));
    await mount('/start/key');
    await until(() => row('Add the same key again') !== undefined);
    expect(row('Add the same key again')?.textContent).toContain('Only if you clone over SSH');
    expect(row('Add the same key again')?.textContent).toContain('HTTPS clone reports Not needed');
    expect(row('Create your SSH key')?.textContent).toContain('Already have a key? Skip this and use the path of its .pub file');
    expect(row('Turn on commit signing')?.textContent).toContain('put the path of its .pub file in user.signingkey');
  });

  it('shows agents_md from the API as the copy block and copies it exactly', async () => {
    setup(ob('o4', []));
    await mount('/start/key');
    await until(() => button('Copy AGENTS.md') !== undefined);
    expect(container.querySelector('pre[aria-label="AGENTS.md"]')?.textContent).toBe(AGENTS);
    await click(button('Copy AGENTS.md'));
    expect(written).toEqual([AGENTS]);
    await until(() => text().includes('Copied'));
  });

  it('copies a command to the clipboard', async () => {
    setup(ob('o4', []));
    await mount('/start/key');
    await until(() => copyLabels().length > 0);
    await click(container.querySelector('button[aria-label="Copy command: git config --global gpg.format ssh"]')!);
    expect(written).toEqual(['git config --global gpg.format ssh']);
  });

  it('draws a line per state and shows the fix only where a line is not green', async () => {
    setup(ob('o4', [check('ssh_key'), check('key_signing', 'fail', 'Add the key at github.com/settings/ssh/new'), check('key_auth', 'unknown'), check('git_signing', 'warn')]));
    await mount('/start/key');
    await until(() => row('Add the key to GitHub for signing') !== undefined);
    expect(row('Create your SSH key')?.textContent).toContain('Passed');
    expect(row('Create your SSH key')?.textContent).not.toContain('Press Enter');
    expect(row('Add the key to GitHub for signing')?.textContent).toContain('Not yet');
    expect(row('Add the key to GitHub for signing')?.textContent).toContain('-> Add the key at github.com/settings/ssh/new');
    expect(row('Add the same key again')?.textContent).toContain('Could not check');
    expect(row('Turn on commit signing')?.textContent).toContain('Warning');
    expect(row('Create your public practice repo')?.textContent).toContain('Not checked yet');
    expect(container.querySelector('[role=status]')?.textContent).toBe('1 of 7 done');
  });

  it('turns a line green on the doctor event and unlocks Continue when the last required line passes', async () => {
    setup(ob('o4', [...allOk().slice(0, 6), check('ruleset', 'fail', 'Create the ruleset')]));
    await mount('/start/key');
    await until(() => row('Protect the main branch') !== undefined);
    expect(button('Continue')?.disabled).toBe(true);
    expect(row('Protect the main branch')?.textContent).toContain('-> Create the ruleset');

    onboarding = ob('o5', allOk());
    await emit('doctor', { checks: [{ id: 'ruleset', status: 'ok' }] });
    await until(() => row('Protect the main branch')?.textContent?.includes('Passed') === true);
    expect(row('Protect the main branch')?.textContent).not.toContain('-> Create the ruleset');
    expect(button('Continue')?.disabled).toBe(false);
    const reads = onboardingReads();
    await until(() => onboardingReads() > reads);
  });

  it('keeps Continue locked while a required id has no row, and a doctor event can add it', async () => {
    setup(ob('o4', allOk().filter((c) => c.id !== 'agents_md')));
    await mount('/start/key');
    await until(() => row('Add AGENTS.md') !== undefined);
    expect(button('Continue')?.disabled).toBe(true);
    await emit('doctor', { checks: [{ id: 'agents_md', status: 'ok' }] });
    await until(() => button('Continue')?.disabled === false);
  });

  it('never gates on the advisory steps, whatever their state', async () => {
    setup(ob('o4', [...allOk(), check('git_email_github', 'warn')]));
    await mount('/start/key');
    await until(() => button('Continue')?.disabled === false);
    expect(row('Two-factor authentication on GitHub')?.textContent).toContain('Not checked yet');
    expect(button('I did this')).toBeDefined();
  });

  it('confirms and unconfirms a step GitHub does not show us', async () => {
    setup(ob('o4', allOk()));
    await mount('/start/key');
    await until(() => button('I did this') !== undefined);
    const forTwoFactor = () => [...(row('Two-factor authentication on GitHub')?.querySelectorAll('button') ?? [])].find((b) => b.textContent === 'I did this');
    await click(forTwoFactor());
    await until(() => row('Two-factor authentication on GitHub')?.textContent?.includes('Passed') === true);
    expect(writes()).toEqual([{ method: 'PUT', url: '/v1/onboarding/confirmations/two_factor' }]);
    expect(row('Secret scanning and push protection')?.textContent).toContain('Not checked yet');
    await click([...(row('Two-factor authentication on GitHub')?.querySelectorAll('button') ?? [])].find((b) => b.textContent === 'Undo'));
    await until(() => row('Two-factor authentication on GitHub')?.textContent?.includes('Not checked yet') === true);
    expect(writes().at(-1)).toEqual({ method: 'DELETE', url: '/v1/onboarding/confirmations/two_factor' });
  });

  it('says so when a confirmation cannot be saved and leaves the line as it was', async () => {
    setup(ob('o4', allOk()));
    await mount('/start/key');
    await until(() => button('I did this') !== undefined);
    confirmStatus = 500;
    await click(button('I did this'));
    await until(() => text().includes('Could not save that. Please try again.'));
    expect(row('Two-factor authentication on GitHub')?.textContent).toContain('Not checked yet');
  });

  it('refreshes the onboarding read before it navigates, so the old step cannot bounce the learner back', async () => {
    setup(ob('o4', allOk()));
    await mount('/start/key');
    await until(() => button('Continue')?.disabled === false);
    onboarding = ob('o5', allOk());
    const before = onboardingReads();
    await click(button('Continue'));
    await until(() => router.state.location.pathname === '/start/first');
    expect(onboardingReads()).toBeGreaterThan(before);
    await act(async () => new Promise((r) => setTimeout(r, 30)));
    expect(router.state.location.pathname).toBe('/start/first');
  });

  it('is signed-in only', async () => {
    setup(ob('o4', []));
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 401 })));
    await mount('/start/key');
    await until(() => router.state.location.pathname === '/signin');
  });
});
