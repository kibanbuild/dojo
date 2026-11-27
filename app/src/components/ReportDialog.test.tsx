// @vitest-environment jsdom
import { act, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { reportProblem } from '../lib/reports';
import { ReportDialog } from './ReportDialog';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;
let status: number;
let release: (() => void) | null;
let signedOut: number;

const field = () => container.querySelector<HTMLTextAreaElement>('textarea')!;
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name)!;
const posts = () => (fetch as unknown as { mock: { calls: [string, RequestInit][] } }).mock.calls;

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => new Promise((r) => setTimeout(r, 5)));
}

async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field(), value);
    field().dispatchEvent(new Event('input', { bubbles: true }));
  });
}

async function submit() {
  await act(async () => container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
  await settle();
}

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      {open && <ReportDialog kind="broken_ticket" itemId="support-l1/perms-03" onClose={() => setOpen(false)} onSignedOut={() => void signedOut++} />}
    </>
  );
}

async function open() {
  await act(async () => root.render(<Harness />));
  const opener = button('Open');
  opener.focus();
  await act(async () => opener.click());
  return opener;
}

beforeEach(() => {
  status = 201;
  release = null;
  signedOut = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      if (release === null && status === -1) await new Promise<void>((r) => (release = r));
      return status === -1 ? new Response(JSON.stringify({ id: 'x' }), { status: 201 }) : new Response(status === 201 ? JSON.stringify({ id: 'x' }) : null, { status });
    }),
  );
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

describe('reportProblem', () => {
  it('refuses empty and over-long messages, counting the trimmed text', () => {
    expect(reportProblem('   ')).toMatch(/few words/);
    expect(reportProblem(' ok ')).toBe('');
    expect(reportProblem('a'.repeat(2000))).toBe('');
    expect(reportProblem('a'.repeat(2001))).toMatch(/at most 2,000/);
  });
});

describe('ReportDialog', () => {
  it('focuses the message field and returns focus to the opener on Escape', async () => {
    const opener = await open();
    expect(document.activeElement).toBe(field());
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })));
    expect(container.querySelector('[role=dialog]')).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it('wraps Tab inside the dialog', async () => {
    await open();
    button('Send report').focus();
    const tab = new KeyboardEvent('keydown', { key: 'Tab', cancelable: true });
    await act(async () => document.dispatchEvent(tab));
    expect(tab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(field());
  });

  it('shows the inline problem for an empty message and sends nothing', async () => {
    await open();
    await submit();
    expect(container.querySelector('[role=status]')?.textContent).toMatch(/few words/);
    expect(field().getAttribute('aria-invalid')).toBe('true');
    expect(posts()).toHaveLength(0);
  });

  it('sends the trimmed message with the item and the output choice, then thanks the learner', async () => {
    await open();
    await type('  The check fails after the fix.  ');
    await act(async () => container.querySelector<HTMLInputElement>('input[type=checkbox]')!.click());
    await submit();
    expect(posts()).toHaveLength(1);
    expect(posts()[0]![0]).toBe('/v1/reports');
    expect(JSON.parse(String(posts()[0]![1].body))).toEqual({
      kind: 'broken_ticket',
      item_id: 'support-l1/perms-03',
      message: 'The check fails after the fix.',
      include_output: true,
    });
    expect(container.textContent).toContain('Thanks, we got your report.');
    expect(document.activeElement).toBe(button('Done'));
    await act(async () => button('Done').click());
    expect(container.querySelector('[role=dialog]')).toBeNull();
  });

  it.each([
    [429, /several reports/],
    [404, /not available/],
    [400, /could not accept/],
    [500, /Could not send/],
  ])('shows the error for a %i and puts focus back on the field', async (code, message) => {
    status = code;
    await open();
    await type('Broken.');
    await submit();
    expect(container.querySelector('[role=status]')?.textContent).toMatch(message);
    expect(document.activeElement).toBe(field());
    expect(field().value).toBe('Broken.');
    expect(button('Send report').hasAttribute('disabled')).toBe(false);
  });

  it('reports a sign-out to the caller', async () => {
    status = 401;
    await open();
    await type('Broken.');
    await submit();
    expect(signedOut).toBe(1);
  });

  it('ignores Escape and a second submit while sending', async () => {
    status = -1;
    await open();
    await type('Broken.');
    await act(async () => container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    await act(async () => container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })));
    expect(container.querySelector('[role=dialog]')).not.toBeNull();
    expect(posts()).toHaveLength(1);
    await act(async () => release?.());
    await settle();
    expect(container.textContent).toContain('Thanks');
  });
});
