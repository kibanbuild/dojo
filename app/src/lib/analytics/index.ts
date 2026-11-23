import { createAnalytics } from './adapter';
import { resolveSink } from './sinks';

export type { Analytics } from './adapter';
export { CONSENT_COOKIE, type Consent } from './consent';

/** The app's analytics adapter. Sink from VITE_ANALYTICS_SINK (console | noop); default console in dev, noop in production. */
export const analytics = createAnalytics({
  sink: resolveSink({ VITE_ANALYTICS_SINK: import.meta.env.VITE_ANALYTICS_SINK as string | undefined, DEV: import.meta.env.DEV }),
});
