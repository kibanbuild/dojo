import { useEffect } from 'react';
import { Outlet, useMatches } from 'react-router';
import { analytics } from '../lib/measure';
import { useMe } from '../lib/me';
import { ConsentBanner } from './ConsentBanner';

/** Route `handle` for a page that gets a page view: its route pattern. */
export interface PageHandle {
  page: string;
}

function pageOf(handle: unknown): string | undefined {
  if (typeof handle !== 'object' || handle === null || !('page' in handle)) return undefined;
  return typeof handle.page === 'string' ? handle.page : undefined;
}

/** The one place analytics hooks into the router: page views per path, identify on sign-in, reset on sign-out. All gated by consent in the adapter. */
export function AnalyticsLayout() {
  const matches = useMatches();
  const me = useMe();
  const learner = me.data;
  // The route's declared pattern (handle.page), never the raw URL, so a future /katas/:id cannot leak an id.
  // A route without one, such as the catch-all, sends nothing.
  const pattern = pageOf(matches.at(-1)?.handle);

  useEffect(() => {
    if (pattern) analytics.page(pattern);
  }, [pattern]);

  useEffect(() => {
    if (learner === undefined) return;
    if (learner === null) {
      analytics.setAccountConsent(null);
      analytics.reset();
      return;
    }
    analytics.setAccountConsent(learner.analytics_consent);
    analytics.identify(learner.id);
  }, [learner]);

  return (
    <>
      <Outlet />
      <ConsentBanner />
    </>
  );
}
