import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { analytics } from '../lib/measure';
import { ME_KEY, useMe, type Me } from '../lib/me';
import { PATCH_MESSAGES, patchMe, type PatchFailure } from '../lib/settings';

const button =
  'inline-flex min-h-11 flex-1 items-center justify-center rounded-[10px] border border-ctl bg-surface px-3.5 text-sm font-bold text-fg hover:bg-surface2 disabled:opacity-45 md:min-h-9';

/**
 * Asks once, while the effective consent is unknown. Closable only by choosing; both buttons look the same.
 * Signed out the choice goes to the first-party cookie; signed in it is saved to the account first and applied only on success.
 */
export function ConsentBanner() {
  const queryClient = useQueryClient();
  const me = useMe();
  const learner = me.data;
  const [, bump] = useState(0);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<PatchFailure | null>(null);

  // Not asked until /v1/me has answered.
  if (learner === undefined) return null;
  const unknown = learner === null ? analytics.consent() === 'unknown' : learner.analytics_consent === null && analytics.consent() === 'unknown';
  if (!unknown) return null;

  async function choose(allow: boolean) {
    if (busy) return;
    const choice = allow ? 'granted' : 'denied';
    if (learner === null || learner === undefined) {
      analytics.setConsent(choice);
      bump((n) => n + 1);
      return;
    }
    // Not optimistic: nothing reaches a sink until the account has the choice saved.
    setBusy(true);
    setFailure(null);
    const result = await patchMe({ analytics_consent: allow });
    setBusy(false);
    if (result.ok) {
      queryClient.setQueryData<Me | null>(ME_KEY, result.me);
      analytics.setAccountConsent(allow);
      analytics.setConsent(choice);
    } else if (result.reason === 'signed_out') {
      queryClient.setQueryData<Me | null>(ME_KEY, null);
    } else {
      setFailure(result.reason);
    }
  }

  return (
    <>
      <div aria-hidden="true" className="h-64 md:h-44" />
      <section
        role="region"
        aria-label="Analytics consent"
        className="fixed right-4 bottom-4 left-4 z-50 mx-auto flex max-w-[560px] flex-col gap-3 rounded-2xl border border-line bg-surface p-5 font-sans text-fg shadow-card"
      >
        <h2 className="m-0 text-base font-extrabold">Help us improve the Dojo?</h2>
        <p className="m-0 text-sm leading-[1.55] text-fg2">
          We&apos;d like to count which pages are used, through PostHog in the EU. Nothing is sent unless you allow it. If you allow it, this page is counted too. You can change this later in Settings.
        </p>
        <p role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
          {failure ? PATCH_MESSAGES[failure] : ''}
        </p>
        <div className="flex gap-3">
          <button type="button" disabled={busy} onClick={() => void choose(true)} className={button}>
            Allow
          </button>
          <button type="button" disabled={busy} onClick={() => void choose(false)} className={button}>
            No thanks
          </button>
        </div>
      </section>
    </>
  );
}
