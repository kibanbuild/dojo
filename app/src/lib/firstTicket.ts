import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ONBOARDING_KEY } from './onboarding';
import { patchTicket, openStream, PROGRESS_DEBOUNCE_MS } from './stream';
import { ticketKey, TICKET_KEY_PREFIX, TODAY_KEY, type Ticket, type TicketProgress, type TicketState } from './read';

export type FirstView = 'hosted' | 'start' | 'starting' | 'running' | 'checked' | 'lost' | 'passed';

/**
 * Where the first ticket stands. `state` is the learner's state on it from `GET /v1/onboarding` (null: not started), which
 * alone settles `passed`, so a reload after the pass still shows it; the ticket's progress row adds the lab. `hosted` is the
 * hosted lab default: `dojo start` only prints that hosted labs are coming soon, so no command is offered until it is turned off.
 */
export function firstView(hostedDefault: boolean, state: TicketState | null, p: TicketProgress | null | undefined): FirstView {
  if (state === 'passed' || state === 'revisit' || p?.state === 'passed' || p?.state === 'revisit') return 'passed';
  if (!p && state === null) return hostedDefault ? 'hosted' : 'start';
  if (!p) return 'starting';
  if (p.lab_state === 'failed' || p.lab_state === 'stopped' || p.lab_state === 'expired') return 'lost';
  if (p.lab_state === 'running') return p.check_count > 0 && p.last_check_ok === false ? 'checked' : 'running';
  return 'starting';
}

/**
 * Keeps O5 live: a `progress` event patches the open ticket at once and refreshes the ticket, Today and the onboarding
 * read (debounced), so the page turns green on the pass and `next_step` reads `done`; `ready`, `resync` and the poll refetch.
 */
export function useFirstTicketLive(): void {
  const client = useQueryClient();
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refetch = () => {
      void client.invalidateQueries({ queryKey: ONBOARDING_KEY });
      void client.invalidateQueries({ queryKey: TODAY_KEY });
      void client.invalidateQueries({ queryKey: TICKET_KEY_PREFIX });
    };
    const close = openStream({
      onSync: refetch,
      onPoll: refetch,
      onProgress: (e) => {
        client.setQueryData<Ticket>(ticketKey(e.item_id), (t) => (t ? patchTicket(t, e) : t));
        clearTimeout(timer);
        timer = setTimeout(refetch, PROGRESS_DEBOUNCE_MS);
      },
    });
    return () => {
      close();
      clearTimeout(timer);
    };
  }, [client]);
}
