import { useEffect } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { ONBOARDING_KEY, type DoctorCheck, type Onboarding } from './onboarding';
import { openStream, type DoctorEvent } from './stream';

export const DOCTOR_REFETCH_MS = 500;

/** Applies a `doctor` event to the cached read: a known id changes status, a new id waits for the refetch to fill its text. */
export function patchChecks(state: Onboarding | null | undefined, event: DoctorEvent): Onboarding | null | undefined {
  if (!state) return state;
  const checks = [...(state.checks ?? [])];
  for (const { id, status } of event.checks) {
    const at = checks.findIndex((c) => c.id === id);
    if (at >= 0) checks[at] = { ...checks[at]!, status };
    else checks.push({ id, status, text: '', fix: '' } satisfies DoctorCheck);
  }
  return { ...state, checks };
}

/**
 * Keeps GET /v1/onboarding live on an onboarding step: a `login` or `doctor` event patches the cache at once, the
 * read follows (debounced for doctor), and `ready`, `resync` and the polling fallback refetch it.
 */
export function useOnboardingLive(): void {
  const client = useQueryClient();
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refetch = () => void client.invalidateQueries({ queryKey: ONBOARDING_KEY });
    const close = openStream({
      onSync: refetch,
      onPoll: refetch,
      onProgress: () => undefined,
      onLogin: () => {
        client.setQueryData<Onboarding | null>(ONBOARDING_KEY, (s) => (s ? { ...s, cli_connected: true } : s));
        refetch();
      },
      onDoctor: (event) => {
        client.setQueryData<Onboarding | null>(ONBOARDING_KEY, (s) => patchChecks(s, event));
        clearTimeout(timer);
        timer = setTimeout(refetch, DOCTOR_REFETCH_MS);
      },
    });
    return () => {
      close();
      clearTimeout(timer);
    };
  }, [client]);
}

/**
 * Leaves an onboarding step. The read is refreshed and awaited first: App and Today redirect on next_step, so
 * a stale one would send the learner straight back.
 */
export async function leaveStep(client: QueryClient, navigate: ReturnType<typeof useNavigate>): Promise<void> {
  await client.invalidateQueries({ queryKey: ONBOARDING_KEY });
  await navigate('/', { replace: true });
}
