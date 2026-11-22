// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { noticeFor, SignInScreen, START_PATH } from './SignIn';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
  act(() => root.render(<SignInScreen notice="" />));
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const form = () => container.querySelector('form')!;
const button = () => container.querySelector<HTMLButtonElement>('button[type=submit]')!;
const box = (name: string) => container.querySelector<HTMLInputElement>(`input[name=${name}]`)!;
const tick = (name: string) => act(() => box(name).click());
const body = () => new URLSearchParams(new FormData(form()) as unknown as Record<string, string>).toString();

describe('sign-in form', () => {
  it('is a plain POST to the start endpoint, never a query string', () => {
    expect(form().method).toBe('post');
    expect(form().getAttribute('action')).toBe(START_PATH);
    expect(form().getAttribute('action')).not.toContain('?');
  });

  it('keeps the button disabled until both boxes are ticked', () => {
    expect(button().disabled).toBe(true);
    tick('age');
    expect(button().disabled).toBe(true);
    tick('terms');
    expect(button().disabled).toBe(false);
    tick('age');
    expect(button().disabled).toBe(true);
  });

  it('submits age=on and terms=on in the body', () => {
    expect(body()).toBe('');
    tick('age');
    tick('terms');
    expect(body()).toBe('age=on&terms=on');
  });

  it('ties a label to each box and links the terms', () => {
    expect(box('age').labels?.[0]?.textContent).toBe('I am 14 or older');
    expect(box('terms').labels?.[0]?.textContent).toContain('I accept the terms');
    expect(container.querySelector('a[href="/terms"]')).not.toBeNull();
  });

  it('describes the disabled button and drops the wrong copy line', () => {
    const hint = container.querySelector(`#${button().getAttribute('aria-describedby')}`);
    expect(hint?.textContent).toContain('Tick both boxes');
    expect(container.textContent).not.toContain('lives on GitHub, not here');
  });

  it('has a live region for notices', () => {
    expect(container.querySelector('[aria-live=polite]')).not.toBeNull();
  });
});

describe('callback notices', () => {
  it('maps the confirm flag and each error code', () => {
    expect(noticeFor(new URLSearchParams('confirm=1'))).toContain('Confirm both boxes');
    expect(noticeFor(new URLSearchParams('error=state_check'))).toContain('try again');
    expect(noticeFor(new URLSearchParams('error=suspended'))).toContain('suspended');
    expect(noticeFor(new URLSearchParams('error=unknown_code'))).toContain('Something went wrong');
    expect(noticeFor(new URLSearchParams(''))).toBe('');
  });
});
