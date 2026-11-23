// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { routes } from '../routes';
import { ME_KEY } from '../lib/me';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const me = { id: '1', username: 'irene', github_login: 'Irene-GH', email: null, language: 'en', usage_stats: true, analytics_consent: null };

type Call = { url: string; method: string; headers: Headers; body: string | undefined };
let container: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let client: QueryClient;
let calls: Call[];

/** GET /v1/me serves the learner (until a DELETE succeeds); DELETE answers `del`: a status, or [status, error code]. */
function stubApi(del: number | [number, string] | 'network') {
  calls = [];
  let gone = false;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = (init.method ?? 'GET').toUpperCase();
      calls.push({ url, method, headers: new Headers(init.headers), body: init.body as string | undefined });
      if (method === 'GET') return gone ? new Response(null, { status: 401 }) : Response.json(me);
      if (del === 'network') throw new TypeError('offline');
      if (Array.isArray(del)) return Response.json({ error: del[1] }, { status: del[0] });
      if (del === 204) gone = true;
      return new Response(null, { status: del });
    }),
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

async function mount() {
  router = createMemoryRouter(routes, { initialEntries: ['/settings'] });
  client = new QueryClient();
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
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

const openBtn = () => [...container.querySelectorAll('button')].find((b) => b.textContent === 'Delete account')!;
const dialog = () => container.querySelector<HTMLElement>('[role=dialog]');
const confirmInput = () => container.querySelector<HTMLInputElement>('input[name=confirm]')!;
const submitBtn = () => [...container.querySelectorAll('button')].find((b) => /^Delet(e my account|ing)/.test(b.textContent ?? ''))!;
const cancelBtn = () => [...container.querySelectorAll('button')].find((b) => b.textContent === 'Cancel')!;
const click = async (el: HTMLElement) => {
  await act(async () => el.click());
  await settle();
};
async function type(value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(confirmInput(), value);
    confirmInput().dispatchEvent(new Event('input', { bubbles: true }));
  });
}
async function openAndType(value = 'irene-gh') {
  await click(openBtn());
  await type(value);
}
const submit = async () => {
  await act(async () => submitBtn().click());
  await settle();
};
const deletes = () => calls.filter((c) => c.method === 'DELETE');

describe('danger zone card', () => {
  it('renders the section with its copy and no dialog', async () => {
    stubApi(204);
    await mount();
    const section = container.querySelector('section[aria-labelledby]:last-of-type')!;
    expect(section.querySelector('h2')?.textContent).toBe('Danger zone');
    expect(section.textContent).toContain('Delete your account');
    expect(section.textContent).toContain('Your GitHub repo and its commits stay yours.');
    expect(openBtn()).toBeTruthy();
    expect(dialog()).toBeNull();
  });
});

describe('confirm dialog', () => {
  it('opens as a labelled modal with focus on the input and the plain-words copy', async () => {
    stubApi(204);
    await mount();
    await click(openBtn());
    const d = dialog()!;
    expect(d.getAttribute('aria-modal')).toBe('true');
    expect(document.getElementById(d.getAttribute('aria-labelledby')!)?.textContent).toBe('Delete your account?');
    expect(d.textContent).toContain('your progress, your story, and your CLI sign-ins on every machine');
    expect(d.textContent).toContain('your GitHub account and your repo');
    expect(d.textContent).toContain('Type your GitHub username (@Irene-GH) to confirm');
    expect(document.activeElement).toBe(confirmInput());
  });

  it('Escape closes it and focus returns to the button', async () => {
    stubApi(204);
    await mount();
    await click(openBtn());
    await act(async () => confirmInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    await settle();
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(openBtn());
    expect(deletes()).toHaveLength(0);
  });

  it('Cancel closes it and focus returns to the button', async () => {
    stubApi(204);
    await mount();
    await click(openBtn());
    await click(cancelBtn());
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(openBtn());
  });

  it('traps Tab inside the dialog', async () => {
    stubApi(204);
    await mount();
    await openAndType();
    // Order: input, Cancel, Delete. Tab from the last wraps to the first; Shift+Tab from the first wraps to the last.
    submitBtn().focus();
    const tab = (shiftKey: boolean) => new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true });
    const fwd = tab(false);
    await act(async () => submitBtn().dispatchEvent(fwd));
    expect(fwd.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(confirmInput());
    const back = tab(true);
    await act(async () => confirmInput().dispatchEvent(back));
    expect(back.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(submitBtn());
  });

  it('keeps Delete disabled until the typed value matches, ignoring case and spaces', async () => {
    stubApi(204);
    await mount();
    await click(openBtn());
    expect(submitBtn().disabled).toBe(true);
    await type('irene');
    expect(submitBtn().disabled).toBe(true);
    await type('  IRENE-gh ');
    expect(submitBtn().disabled).toBe(false);
    await type('irene-gh2');
    expect(submitBtn().disabled).toBe(true);
  });
});

describe('deleting', () => {
  it('sends DELETE /v1/me with the confirmation and the CSRF header, clears the cache and lands on sign-in with a notice', async () => {
    stubApi(204);
    await mount();
    client.setQueryData(['devices'], [{ id: 'd1' }]);
    await openAndType('  irene-GH ');
    await submit();
    expect(deletes()).toHaveLength(1);
    const call = deletes()[0]!;
    expect(call.url).toBe('/v1/me');
    expect(call.headers.get('X-Dojo-Csrf')).toBe('1');
    // The canonical login goes out, not the typed text.
    expect(JSON.parse(call.body!)).toEqual({ confirm: 'Irene-GH' });
    expect(client.getQueryData(ME_KEY)).toBeNull();
    expect(client.getQueryData(['devices'])).toBeUndefined();
    expect(router.state.location.pathname).toBe('/signin');
    expect(container.textContent).toContain('Your account was deleted.');
    const notice = [...container.querySelectorAll('p')].find((p) => p.textContent === 'Your account was deleted.')!;
    expect(notice.className).not.toContain('text-bad');
    expect(notice.closest('[role=status]')).not.toBeNull();
  });

  it('sends one request for two rapid submits', async () => {
    stubApi(204);
    await mount();
    await openAndType();
    const form = dialog()!.querySelector('form')!;
    await act(async () => {
      form.requestSubmit();
      form.requestSubmit();
    });
    await settle();
    expect(deletes()).toHaveLength(1);
  });

  it('after a failure, takes focus back so Escape still closes the dialog', async () => {
    stubApi(500);
    await mount();
    await openAndType();
    await submit();
    expect(document.activeElement).toBe(confirmInput());
    // jsdom does not drop focus when a control is disabled; simulate it.
    await act(async () => {
      confirmInput().blur();
      document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    await settle();
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(openBtn());
  });

  it('keeps the Tab trap when focus is on <body>', async () => {
    stubApi(204);
    await mount();
    await openAndType();
    confirmInput().blur();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    await act(async () => document.body.dispatchEvent(ev));
    expect(ev.defaultPrevented).toBe(true);
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  it.each([
    [[400, 'confirm_mismatch'] as [number, string], 'does not match your GitHub username'],
    [429, 'Too many attempts'],
    [500, 'Could not delete your account'],
    ['network' as const, 'Could not delete your account'],
  ])('shows %j inline and keeps the dialog open', async (answer, text) => {
    stubApi(answer);
    await mount();
    await openAndType();
    await submit();
    expect(dialog()).not.toBeNull();
    expect(dialog()!.querySelector('[role=status]')?.textContent).toContain(text);
    expect(submitBtn().disabled).toBe(false);
    expect(client.getQueryData(ME_KEY)).toEqual(me);
    expect(router.state.location.pathname).toBe('/settings');
  });

  it('treats a 401 as signed out: clears the cache and goes to sign-in', async () => {
    stubApi(401);
    await mount();
    await openAndType();
    await submit();
    expect(client.getQueryData(ME_KEY)).toBeNull();
    expect(router.state.location.pathname).toBe('/signin');
    expect(container.textContent).not.toContain('Your account was deleted.');
  });
});
