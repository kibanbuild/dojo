import { readConsentCookie, writeConsentCookie, type Consent } from './consent';
import { EVENTS, isEventName, type EventName } from './events';
import { sanitizeProps } from './sanitize';
import type { Sink } from './sinks';

export interface Analytics {
  page(path: string): void;
  /** Only events declared in events.ts, with only their declared props; anything else is dropped. */
  track(event: EventName, props?: Record<string, unknown>): void;
  /** The signed-in learner's id, the only identity a sink ever sees. */
  identify(learnerId: string): void;
  reset(): void;
  /** The choice made in the banner (11b). Stored in the first-party cookie. */
  setConsent(choice: 'granted' | 'denied'): void;
  /** The signed-in account's `analytics_consent` from /v1/me; null means undecided and falls back to the cookie. */
  setAccountConsent(value: boolean | null): void;
  consent(): Consent;
}

export interface AnalyticsDeps {
  sink: Sink;
  readCookie?: () => Consent;
  writeCookie?: (choice: 'granted' | 'denied') => void;
}

/** One interface over a sink. Nothing reaches the sink unless consent is granted; the account's choice wins over the cookie. */
export function createAnalytics({ sink, readCookie = readConsentCookie, writeCookie = writeConsentCookie }: AnalyticsDeps): Analytics {
  let account: boolean | null = null;
  let learnerId: string | null = null;
  let lastPath: string | null = null;

  const consent = (): Consent => {
    if (account !== null) return account ? 'granted' : 'denied';
    return readCookie();
  };

  // Runs after any change that can move consent: replays identity and the current page on grant, clears the sink on denial.
  function apply(before: Consent) {
    const now = consent();
    if (now === before) return;
    if (now === 'granted') {
      if (learnerId) sink.identify(learnerId);
      if (lastPath) sink.page(lastPath);
    } else if (before === 'granted') {
      sink.reset();
    }
  }

  return {
    page(path) {
      lastPath = path;
      if (consent() === 'granted') sink.page(path);
    },
    track(event, props) {
      if (consent() !== 'granted' || !isEventName(event)) return;
      sink.track(event, sanitizeProps(EVENTS[event], props));
    },
    identify(id) {
      learnerId = id;
      if (consent() === 'granted') sink.identify(id);
    },
    reset() {
      learnerId = null;
      if (consent() === 'granted') sink.reset();
    },
    setConsent(choice) {
      const before = consent();
      writeCookie(choice);
      apply(before);
    },
    setAccountConsent(value) {
      const before = consent();
      account = value;
      apply(before);
    },
    consent,
  };
}
