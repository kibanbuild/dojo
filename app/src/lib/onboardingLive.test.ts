import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { patchChecks } from './onboardingLive';
import type { Onboarding } from './onboarding';
import { openStream, type StreamHandlers } from './stream';

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
  emit(name: string, data: string) {
    for (const fn of this.listeners.get(name) ?? []) fn({ data } as MessageEvent<string>);
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeES.all = [];
  vi.stubGlobal('EventSource', FakeES);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const base: StreamHandlers = { onSync: vi.fn(), onProgress: vi.fn(), onPoll: vi.fn() };

describe('login and doctor stream events', () => {
  it('hands the parsed data to the handlers', () => {
    const onLogin = vi.fn();
    const onDoctor = vi.fn();
    openStream({ ...base, onLogin, onDoctor });
    const es = FakeES.all[0]!;
    es.emit('login', '{"device_name":"laptop","os":"darwin","cli_version":"1.0.0","at":"t"}');
    es.emit('doctor', '{"checks":[{"id":"memory","status":"ok"}]}');
    expect(onLogin).toHaveBeenCalledWith({ device_name: 'laptop', os: 'darwin', cli_version: '1.0.0', at: 't' });
    expect(onDoctor).toHaveBeenCalledWith({ checks: [{ id: 'memory', status: 'ok' }] });
  });

  it('ignores a malformed message and a page that does not listen', () => {
    const onDoctor = vi.fn();
    openStream({ ...base, onDoctor });
    const es = FakeES.all[0]!;
    es.emit('doctor', 'not json');
    es.emit('login', '{}');
    expect(onDoctor).not.toHaveBeenCalled();
  });
});

describe('patchChecks', () => {
  const state = {
    next_step: 'o3',
    checks: [{ id: 'memory', status: 'fail', text: '4 GB RAM', fix: 'add RAM' }],
  } as Onboarding;

  it('changes the status of a known id and keeps its text', () => {
    const next = patchChecks(state, { checks: [{ id: 'memory', status: 'ok' }] });
    expect(next?.checks).toEqual([{ id: 'memory', status: 'ok', text: '4 GB RAM', fix: 'add RAM' }]);
  });

  it('adds an unknown id with empty text until the read fills it', () => {
    const next = patchChecks(state, { checks: [{ id: 'git', status: 'warn' }] });
    expect(next?.checks?.map((c) => c.id)).toEqual(['memory', 'git']);
  });

  it('leaves a missing read alone', () => {
    expect(patchChecks(null, { checks: [] })).toBeNull();
    expect(patchChecks(undefined, { checks: [] })).toBeUndefined();
  });
});
