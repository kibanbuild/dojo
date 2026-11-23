// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { analytics } from '../lib/measure';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '42', username: 'irene', github_login: 'irene', email: null, language: 'en', usage_stats: true, analytics_consent: true };

let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let spies: Record<'page' | 'identify' | 'reset', ReturnType<typeof vi.spyOn>>;

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

async function mount(path: string, meResponse: object | null) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => (meResponse ? Response.json(meResponse) : new Response(null, { status: 401 }))),
  );
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
  spies = {
    page: vi.spyOn(analytics, 'page'),
    identify: vi.spyOn(analytics, 'identify'),
    reset: vi.spyOn(analytics, 'reset'),
  };
  vi.spyOn(analytics, 'setAccountConsent');
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('analytics wiring', () => {
  it('records a page view on mount and on navigation, path only', async () => {
    await mount('/settings?tab=x', me);
    expect(spies.page).toHaveBeenCalledWith('/settings');
    await act(async () => {
      await router.navigate('/settings/machines');
    });
    expect(spies.page).toHaveBeenCalledWith('/settings/machines');
  });

  it('sends no page view for an unmatched URL, and never its raw path', async () => {
    await mount('/foo/someuser', me);
    expect(spies.page.mock.calls.flat().join(' ')).not.toContain('someuser');
    expect(spies.page).not.toHaveBeenCalledWith('/foo/someuser');
    expect(spies.page).toHaveBeenCalledWith('/');
  });

  it('identifies the signed-in learner by id and passes the account consent', async () => {
    await mount('/', me);
    expect(spies.identify).toHaveBeenCalledWith('42');
    expect(analytics.setAccountConsent).toHaveBeenCalledWith(true);
  });

  it('resets when signed out', async () => {
    await mount('/signin', null);
    expect(spies.reset).toHaveBeenCalled();
    expect(spies.identify).not.toHaveBeenCalled();
    expect(analytics.setAccountConsent).toHaveBeenCalledWith(null);
  });
});
