export type EventValue = string | number | boolean;
export type EventProps = Record<string, EventValue>;

/** What every analytics backend implements. The adapter only calls it once consent is granted. */
export interface Sink {
  page(path: string): void;
  track(event: string, props: EventProps): void;
  identify(learnerId: string): void;
  reset(): void;
}

export type SinkName = 'console' | 'noop';

export const noopSink: Sink = {
  page() {},
  track() {},
  identify() {},
  reset() {},
};

/** Local stand-in for PostHog: each event goes to the browser console and nowhere else. */
export const consoleSink: Sink = {
  page: (path) => console.info('[analytics] page', path),
  track: (event, props) => console.info('[analytics] track', event, props),
  identify: (learnerId) => console.info('[analytics] identify', learnerId),
  reset: () => console.info('[analytics] reset'),
};

// The PostHog sink plugs in here later (posthog-js, EU host, loaded only after consent). Until then
// there is no network sink at all.
const sinks: Record<SinkName, Sink> = { console: consoleSink, noop: noopSink };

/** VITE_ANALYTICS_SINK picks a sink; unset or unknown means console in dev builds and noop otherwise. */
export function resolveSink(env: { VITE_ANALYTICS_SINK?: string; DEV: boolean }): Sink {
  const name = env.VITE_ANALYTICS_SINK;
  if (name === 'console' || name === 'noop') return sinks[name];
  return env.DEV ? consoleSink : noopSink;
}
