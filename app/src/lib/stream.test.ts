import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { IDLE_MS, openStream, patchTicket, POLL_MS, REFUSED_RETRY_MS, type ProgressEvent, type StreamHandlers } from './stream';
import type { Ticket } from './read';

class FakeES {
  static all: FakeES[] = [];
  readyState = 1;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>();
  constructor(readonly url: string) {
    FakeES.all.push(this);
  }
  addEventListener(name: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn]);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  emit(name: string, data = '{}') {
    for (const fn of this.listeners.get(name) ?? []) fn({ data } as MessageEvent<string>);
  }
}

function handlers() {
  return { onSync: vi.fn(), onProgress: vi.fn(), onPoll: vi.fn() } satisfies StreamHandlers;
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

const evt: ProgressEvent = {
  item_id: 'support-l1/perms-03',
  state: 'started',
  lab_state: 'running',
  lab: 'local',
  check_count: 3,
  last_check_ok: true,
  last_check_at: '2026-11-23T10:40:00Z',
};

describe('openStream', () => {
  it('connects to /v1/stream and syncs on ready and resync', () => {
    const h = handlers();
    openStream(h);
    const es = FakeES.all[0]!;
    expect(es.url).toBe('/v1/stream');
    es.emit('ready');
    es.emit('resync');
    expect(h.onSync).toHaveBeenCalledTimes(2);
  });

  it('passes progress data on, ignores unknown events and malformed data', () => {
    const h = handlers();
    openStream(h);
    const es = FakeES.all[0]!;
    es.emit('progress', JSON.stringify(evt));
    es.emit('progress', 'not json');
    es.emit('login');
    es.emit('heartbeat', '{"at":"x"}');
    expect(h.onProgress).toHaveBeenCalledOnce();
    expect(h.onProgress).toHaveBeenCalledWith(evt);
    expect(h.onSync).not.toHaveBeenCalled();
  });

  it('polls every 10 s after an error and stops on the next ready', () => {
    const h = handlers();
    openStream(h);
    const es = FakeES.all[0]!;
    es.readyState = 0;
    es.onerror?.();
    vi.advanceTimersByTime(POLL_MS * 2);
    expect(h.onPoll).toHaveBeenCalledTimes(2);
    es.emit('ready');
    vi.advanceTimersByTime(POLL_MS * 3);
    expect(h.onPoll).toHaveBeenCalledTimes(2);
  });

  it('recreates the stream after 30 s when the connect was refused', () => {
    const h = handlers();
    openStream(h);
    const es = FakeES.all[0]!;
    es.readyState = 2;
    es.onerror?.();
    expect(es.closed).toBe(true);
    vi.advanceTimersByTime(REFUSED_RETRY_MS - 1);
    expect(FakeES.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeES.all).toHaveLength(2);
  });

  it('reconnects after 60 s of silence, and a heartbeat keeps it open', () => {
    openStream(handlers());
    vi.advanceTimersByTime(IDLE_MS - 1);
    FakeES.all[0]!.emit('heartbeat');
    vi.advanceTimersByTime(IDLE_MS - 1);
    expect(FakeES.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeES.all[0]!.closed).toBe(true);
    expect(FakeES.all).toHaveLength(2);
  });

  it('polls from the start when the browser has no EventSource', () => {
    vi.stubGlobal('EventSource', undefined);
    const h = handlers();
    openStream(h);
    vi.advanceTimersByTime(POLL_MS);
    expect(h.onPoll).toHaveBeenCalledOnce();
  });

  it('close() stops the stream, the poll and the retry', () => {
    const h = handlers();
    const close = openStream(h);
    const es = FakeES.all[0]!;
    es.readyState = 2;
    es.onerror?.();
    close();
    vi.advanceTimersByTime(REFUSED_RETRY_MS * 2);
    expect(FakeES.all).toHaveLength(1);
    expect(h.onPoll).not.toHaveBeenCalled();
  });
});

describe('patchTicket', () => {
  const ticket: Ticket = {
    id: 'support-l1/perms-03',
    title: 't',
    role: 'support-l1',
    est_minutes: 20,
    tags: [],
    brief: 'b',
    hint_count: 3,
    hints: [],
    progress: {
      state: 'started',
      lab: 'local',
      lab_state: 'starting',
      check_count: 2,
      last_check_ok: false,
      last_check_at: null,
      last_check_passed: 3,
      last_check_total: 5,
      last_check_output: 'out',
      first_passed_at: null,
      passed_seconds: null,
      passed_commit: null,
    },
  };

  it('updates the progress fields it carries and keeps the rest', () => {
    const sameCheck = { ...ticket, progress: { ...ticket.progress!, last_check_at: evt.last_check_at } };
    const next = patchTicket(sameCheck, evt);
    expect(next.progress).toMatchObject({ lab_state: 'running', check_count: 3, last_check_ok: true, last_check_output: 'out' });
  });

  it('drops the previous check\'s counts and output when the event is a new check', () => {
    const next = patchTicket(ticket, { ...evt, state: 'passed' });
    expect(next.progress).toMatchObject({
      state: 'passed',
      last_check_ok: true,
      last_check_at: evt.last_check_at,
      last_check_passed: null,
      last_check_total: null,
      last_check_output: null,
    });
  });

  it('leaves another ticket, or one with no progress row, alone', () => {
    expect(patchTicket(ticket, { ...evt, item_id: 'other/x' })).toBe(ticket);
    const fresh = { ...ticket, progress: null };
    expect(patchTicket(fresh, evt)).toBe(fresh);
  });
});
