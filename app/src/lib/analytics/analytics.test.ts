// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAnalytics } from './adapter';
import { CONSENT_COOKIE, readConsentCookie, writeConsentCookie, type Consent } from './consent';
import { sanitizeProps } from './sanitize';
import { consoleSink, noopSink, resolveSink, type Sink } from './sinks';

function fakeSink() {
  const calls: unknown[][] = [];
  const sink: Sink = {
    page: (p) => calls.push(['page', p]),
    track: (e, p) => calls.push(['track', e, p]),
    identify: (id) => calls.push(['identify', id]),
    reset: () => calls.push(['reset']),
  };
  return { sink, calls };
}

function setup(initial: Consent = 'unknown') {
  const { sink, calls } = fakeSink();
  let cookie = initial;
  const analytics = createAnalytics({ sink, readCookie: () => cookie, writeCookie: (c) => (cookie = c) });
  return { analytics, calls };
}

describe('consent gate', () => {
  it('sends nothing while consent is unknown', () => {
    const { analytics, calls } = setup();
    analytics.page('/');
    analytics.track('kata_started', { kata: 'k1' });
    analytics.identify('42');
    analytics.reset();
    expect(analytics.consent()).toBe('unknown');
    expect(calls).toEqual([]);
  });

  it('sends events after granted', () => {
    const { analytics, calls } = setup();
    analytics.setConsent('granted');
    analytics.page('/settings');
    analytics.track('kata_started', { kata: 'k1', attempt: 2 });
    analytics.identify('42');
    analytics.reset();
    expect(calls).toEqual([['page', '/settings'], ['track', 'kata_started', { kata: 'k1', attempt: 2 }], ['identify', '42'], ['reset']]);
  });

  it('sends nothing after denied', () => {
    const { analytics, calls } = setup();
    analytics.setConsent('denied');
    analytics.page('/');
    analytics.track('kata_started', { kata: 'k1' });
    analytics.identify('42');
    expect(calls).toEqual([]);
  });

  it('replays identity and the current page when consent is granted late', () => {
    const { analytics, calls } = setup();
    analytics.identify('42');
    analytics.page('/settings');
    analytics.setConsent('granted');
    expect(calls).toEqual([['identify', '42'], ['page', '/settings']]);
  });

  it('resets the sink when consent is withdrawn and stops sending', () => {
    const { analytics, calls } = setup('granted');
    analytics.setAccountConsent(false);
    analytics.page('/');
    expect(calls).toEqual([['reset']]);
  });

  it('lets the account choice win over the cookie, and falls back to it when null', () => {
    const { analytics } = setup('denied');
    analytics.setAccountConsent(true);
    expect(analytics.consent()).toBe('granted');
    analytics.setAccountConsent(null);
    expect(analytics.consent()).toBe('denied');
  });
});

describe('props allowlist', () => {
  const schema = { ok: 'boolean', n: 'number', id: 'id', level: ['easy', 'hard'] } as const;

  it('keeps declared props of the declared kind', () => {
    expect(sanitizeProps(schema, { ok: true, n: 3, id: 'k1', level: 'easy' })).toEqual({ ok: true, n: 3, id: 'k1', level: 'easy' });
  });

  it('drops undeclared keys, including the names a blocklist would miss', () => {
    const bypass = { user_name: 'irene', e_mail: 'a@b.co', emailAddress: 'a@b.co', access_token: 'abc', password: 'hunter2', phone: '600000000', userId: '42', username: 'irene', github_login: 'irene' };
    expect(sanitizeProps(schema, { ...bypass, ok: true })).toEqual({ ok: true });
  });

  it('drops declared keys with the wrong kind, emails, free text and enum misses', () => {
    expect(sanitizeProps(schema, { ok: 'yes', n: NaN, id: 'irene@example.com', level: 'medium' })).toEqual({});
    expect(sanitizeProps(schema, { id: 'too hard for me' })).toEqual({});
    expect(sanitizeProps(schema, { n: { a: 1 }, ok: [true] })).toEqual({});
  });

  it('sends a known event with only its declared props', () => {
    const { analytics, calls } = setup('granted');
    analytics.track('kata_started', { kata: 'k1', attempt: 2, email: 'a@b.co', userId: '42', note: 'x y' });
    expect(calls).toEqual([['track', 'kata_started', { kata: 'k1', attempt: 2 }]]);
  });

  it('does not send an unknown event', () => {
    const { analytics, calls } = setup('granted');
    analytics.track('made_up' as never, { kata: 'k1' });
    analytics.track('toString' as never, {});
    expect(calls).toEqual([]);
  });
});

describe('sink selection', () => {
  it('honours VITE_ANALYTICS_SINK', () => {
    expect(resolveSink({ VITE_ANALYTICS_SINK: 'console', DEV: false })).toBe(consoleSink);
    expect(resolveSink({ VITE_ANALYTICS_SINK: 'noop', DEV: true })).toBe(noopSink);
  });

  it('defaults to console in dev and noop in production', () => {
    expect(resolveSink({ DEV: true })).toBe(consoleSink);
    expect(resolveSink({ DEV: false })).toBe(noopSink);
    expect(resolveSink({ VITE_ANALYTICS_SINK: 'posthog', DEV: false })).toBe(noopSink);
  });

  it('console sink logs with the [analytics] prefix', () => {
    const info = vi.spyOn(console, 'info').mockImplementation(() => {});
    consoleSink.page('/x');
    consoleSink.track('e', { a: 1 });
    expect(info.mock.calls.map((c) => c[0])).toEqual(['[analytics] page', '[analytics] track']);
    info.mockRestore();
  });
});

describe('consent cookie', () => {
  beforeEach(() => {
    document.cookie = `${CONSENT_COOKIE}=; Max-Age=0; Path=/`;
  });
  afterEach(() => {
    document.cookie = `${CONSENT_COOKIE}=; Max-Age=0; Path=/`;
  });

  it('round-trips and ignores unknown values', () => {
    expect(readConsentCookie()).toBe('unknown');
    writeConsentCookie('granted');
    expect(readConsentCookie()).toBe('granted');
    document.cookie = `${CONSENT_COOKIE}=maybe; Path=/`;
    expect(readConsentCookie()).toBe('unknown');
  });

  it('writes first-party, Lax, one year, Secure only on https', () => {
    const written: string[] = [];
    const doc = { get cookie() { return ''; }, set cookie(v: string) { written.push(v); } };
    writeConsentCookie('denied', doc, true);
    writeConsentCookie('denied', doc, false);
    expect(written[0]).toBe(`${CONSENT_COOKIE}=denied; Path=/; Max-Age=31536000; SameSite=Lax; Secure`);
    expect(written[1]).toBe(`${CONSENT_COOKIE}=denied; Path=/; Max-Age=31536000; SameSite=Lax`);
    expect(written.join()).not.toContain('Domain');
  });
});
