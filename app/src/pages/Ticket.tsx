import { useEffect, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import { AppFrame } from '../components/AppFrame';
import { Inline, Markdown } from '../lib/markdown';
import { ME_KEY, type Me } from '../lib/me';
import { openHint, READ_MESSAGES, ReadError, ticketKey, useTicket, type Hint, type ReadFailure, type Ticket, type TicketProgress } from '../lib/read';
import { useLiveData } from '../lib/stream';
import { CopyCommand, Headline } from './Today';

const card = 'flex flex-col gap-3 rounded-2xl border border-line bg-surface p-5';
const badge = 'inline-flex h-[26px] flex-none items-center rounded-md px-2.5 text-xs font-bold whitespace-nowrap';
const mono = 'font-mono text-muted';
const ghostBtn =
  'inline-flex h-11 items-center justify-center rounded-[10px] border border-ctl bg-surface px-3.5 text-sm font-bold text-fg md:h-9';
const TICKET_ID = /^[^/]+\/[^/]+$/;

function ago(iso: string | null, now = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return '';
  const min = Math.max(0, Math.round((now - t) / 60_000));
  if (min < 1) return 'just now';
  if (min < 60) return `${min} min ago`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}

export function duration(seconds: number): string {
  const min = Math.max(1, Math.round(seconds / 60));
  return min < 60 ? `${min} min` : `${Math.floor(min / 60)} h ${min % 60} min`;
}

type View = 'new' | 'starting' | 'running' | 'failed' | 'stopped' | 'expired' | 'passed' | 'revisit';

export function viewOf(p: TicketProgress | null): View {
  if (!p) return 'new';
  if (p.state === 'passed') return 'passed';
  if (p.state === 'revisit') return 'revisit';
  if (p.lab_state === 'starting' || p.lab_state === 'failed' || p.lab_state === 'stopped' || p.lab_state === 'expired') return p.lab_state;
  return 'running';
}

const BADGE: Record<View, { label: string; cls: string }> = {
  new: { label: 'Not started', cls: 'bg-surface2 text-fg2' },
  starting: { label: 'Starting lab', cls: 'bg-accentsoft text-accentink' },
  running: { label: 'In progress', cls: 'bg-accentsoft text-accentink' },
  failed: { label: "Lab didn't start", cls: 'bg-surface2 text-bad' },
  stopped: { label: 'Lab stopped', cls: 'bg-surface2 text-fg2' },
  expired: { label: 'Lab expired', cls: 'bg-surface2 text-fg2' },
  passed: { label: 'Passed', cls: 'bg-oksoft text-ok' },
  revisit: { label: 'Practice mode', cls: 'bg-oksoft text-ok' },
};

function LabCard({ ticket, view, progress }: { ticket: Ticket; view: View; progress: TicketProgress | null }) {
  const start = `dojo start ${ticket.id}`;
  let title: string;
  let body: ReactNode;
  let command: string | null = start;
  switch (view) {
    case 'new':
      title = 'Start this ticket';
      body = 'Run this in your terminal. It boots your lab and sets up the ticket in ~/dojo.';
      break;
    case 'starting':
      title = 'Starting your lab';
      body = 'Usually under a minute. The first start of a role downloads the lab image once.';
      command = null;
      break;
    case 'failed':
      title = 'The lab did not start';
      body = (
        <>
          Nothing is broken. Run <code className="font-mono text-[13px]">dojo doctor</code> to see what is in the way, then start again.
        </>
      );
      break;
    case 'expired':
      title = 'Your lab expired';
      body = 'We had no news from your lab for 7 days. Start it again to continue this ticket.';
      break;
    case 'stopped':
      title = 'Your lab is stopped';
      body = 'Start it again and continue where the ticket is.';
      break;
    case 'passed':
      title = 'Ticket passed';
      body = progress?.passed_seconds != null ? `You fixed it in ${duration(progress.passed_seconds)}.` : 'You fixed it.';
      command = null;
      break;
    case 'revisit':
      title = 'Practice mode';
      body = `${progress?.first_passed_at ? `You passed this ticket ${ago(progress.first_passed_at)}` : 'You passed this ticket'}${progress?.passed_seconds != null ? ` in ${duration(progress.passed_seconds)}` : ''}. Run it again whenever you want; all hints are open.`;
      break;
    default:
      title = 'Your lab';
      body = (
        <>
          Fix it on your computer, then run <code className="font-mono text-[13px]">dojo check</code>. Results show here.
        </>
      );
      command = 'dojo check';
  }
  return (
    <section aria-label="Your lab" className={card}>
      <h2 className="m-0 text-[15px] font-extrabold">{title}</h2>
      <p className="m-0 text-sm leading-normal text-fg2">{body}</p>
      {command && <CopyCommand command={command} />}
    </section>
  );
}

function LastCheck({ progress }: { progress: TicketProgress }) {
  if (progress.check_count === 0 || progress.last_check_ok === null) return null;
  const ok = progress.last_check_ok;
  const counts =
    progress.last_check_passed !== null && progress.last_check_total !== null ? `${progress.last_check_passed} of ${progress.last_check_total} checks pass` : '';
  const when = ago(progress.last_check_at);
  return (
    <section aria-label="Last check" className={card}>
      <div className="flex items-center justify-between gap-3">
        <h2 className="m-0 text-[15px] font-extrabold">Last dojo check</h2>
        <span className={`${badge} ${ok ? 'bg-oksoft text-ok' : 'bg-surface2 text-fg2'}`}>{ok ? 'Passed' : 'Not yet'}</span>
      </div>
      <p role="status" className="m-0 text-sm text-fg2">
        {[ok ? 'Passed' : 'Not yet', counts, when].filter(Boolean).join(' · ')}
      </p>
      {progress.last_check_output && (
        <pre tabIndex={0} aria-label="Check output" className="m-0 max-h-60 overflow-auto rounded-[10px] bg-surface2 p-3 font-mono text-[13px] whitespace-pre-wrap text-fg">
          {progress.last_check_output}
        </pre>
      )}
    </section>
  );
}

const HINT_ERRORS: Partial<Record<'hint_locked' | ReadFailure, string>> = {
  hint_locked: 'Open the earlier hint first.',
  rate_limited: READ_MESSAGES.rate_limited,
  suspended: READ_MESSAGES.suspended,
  failed: 'Could not open the hint. Please try again.',
};

function Hints({ ticket }: { ticket: Ticket }) {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const open = new Map(ticket.hints.map((h) => [h.n, h.text]));
  const all = ticket.progress?.state === 'passed' || ticket.progress?.state === 'revisit';
  const next = Array.from({ length: ticket.hint_count }, (_, i) => i + 1).find((n) => !open.has(n));
  if (ticket.hint_count === 0) return null;

  async function show(n: number) {
    setBusy(true);
    setError('');
    const res = await openHint(ticket.id, n);
    if (!res.ok && res.reason === 'rate_limited') {
      setTimeout(() => setBusy(false), res.retryAfterMs ?? 1000);
    } else {
      setBusy(false);
    }
    if (res.ok) {
      queryClient.setQueryData<Ticket>(ticketKey(ticket.id), (t) => {
        if (!t) return t;
        const hints: Hint[] = [...t.hints.filter((h) => h.n !== res.hint.n), res.hint].sort((a, b) => a.n - b.n);
        return { ...t, hints };
      });
      return;
    }
    if (res.reason === 'hint_locked') void queryClient.invalidateQueries({ queryKey: ticketKey(ticket.id) });
    setError(HINT_ERRORS[res.reason] ?? HINT_ERRORS.failed ?? '');
  }

  return (
    <section aria-label="Hints" className={card}>
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="m-0 text-[15px] font-extrabold">Hints</h2>
        <span className={`${mono} text-xs`}>
          {open.size} of {ticket.hint_count} {all ? 'open' : 'opened'}
        </span>
      </div>
      <ol className="m-0 flex list-none flex-col gap-3 p-0">
        {Array.from({ length: ticket.hint_count }, (_, i) => i + 1).map((n) => {
          const text = open.get(n);
          return (
            <li key={n} className="flex flex-col gap-1.5 border-t border-line pt-3 first:border-t-0 first:pt-0">
              <span className={`${mono} text-xs`}>hint {n}</span>
              {text !== undefined ? (
                <p className="m-0 text-sm leading-normal text-fg2">
                  <Inline text={text} />
                </p>
              ) : n === next ? (
                <button type="button" disabled={busy} onClick={() => void show(n)} className={`${ghostBtn} self-start disabled:opacity-60`}>
                  Show hint {n}
                </button>
              ) : (
                <p className="m-0 text-xs text-muted">unlocks after hint {n - 1}</p>
              )}
            </li>
          );
        })}
      </ol>
      <p role="status" className="m-0 text-xs font-bold text-bad empty:hidden">
        {error}
      </p>
      <p className="m-0 text-xs text-muted">Hints never block you. They are only counted for you.</p>
    </section>
  );
}

function Detail({ ticket }: { ticket: Ticket }) {
  const view = viewOf(ticket.progress);
  const b = BADGE[view];
  return (
    <div className="grid items-start gap-6 md:grid-cols-[1fr_380px] md:gap-7">
      <div className="flex min-w-0 flex-col gap-5">
        <div className="flex flex-col gap-2.5">
          <div className="flex flex-wrap items-center gap-2.5">
            <span className={`${badge} ${b.cls}`}>{b.label}</span>
            <span className={`${mono} text-xs md:text-[13px]`}>{ticket.id}</span>
          </div>
          <Headline text={ticket.title} />
        </div>
        {ticket.brief && (
          <section aria-label="The ticket" className={card}>
            <Markdown source={ticket.brief} />
          </section>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-5">
        <LabCard ticket={ticket} view={view} progress={ticket.progress} />
        {ticket.progress && <LastCheck progress={ticket.progress} />}
        <Hints ticket={ticket} />
      </div>
    </div>
  );
}

function PlainTitle() {
  return <h1 className="m-0 text-[32px] leading-[1.08] font-extrabold tracking-[-1px] md:text-[44px]">Ticket</h1>;
}

function Body({ id }: { id: string }) {
  useLiveData();
  const ticket = useTicket(id);
  const queryClient = useQueryClient();
  const signedOut = ticket.isError && ticket.error instanceof ReadError && ticket.error.reason === 'signed_out';
  useEffect(() => {
    if (signedOut) queryClient.setQueryData(ME_KEY, null);
  }, [signedOut, queryClient]);
  if (signedOut) return null;
  if (ticket.isPending) {
    return (
      <div className="flex flex-col gap-3">
        <PlainTitle />
        <p role="status" className="m-0 text-muted">
          Loading the ticket...
        </p>
      </div>
    );
  }
  if (ticket.isError) {
    const reason = ticket.error instanceof ReadError ? ticket.error.reason : 'failed';
    return (
      <div className="flex flex-col gap-3">
        <PlainTitle />
        <div role="alert" className="flex flex-col items-start gap-3">
          <p className="m-0 text-bad">{READ_MESSAGES[reason]}</p>
          {reason === 'not_found' ? (
            <Link to="/path" className={ghostBtn}>
              Back to the path
            </Link>
          ) : (
            reason !== 'suspended' && (
              <button type="button" onClick={() => void ticket.refetch()} className={ghostBtn}>
                Retry
              </button>
            )
          )}
        </div>
      </div>
    );
  }
  return <Detail ticket={ticket.data} />;
}

/** L3: one ticket with its brief, lab state, last check and written hints. */
export function TicketPage({ me }: { me: Me }) {
  const id = useParams()['*'] ?? '';
  return (
    <AppFrame me={me}>
      {TICKET_ID.test(id) ? (
        <Body key={id} id={id} />
      ) : (
        <div role="alert" className="flex flex-col items-start gap-3">
          <PlainTitle />
          <p className="m-0 text-bad">{READ_MESSAGES.not_found}</p>
          <Link to="/path" className={ghostBtn}>
            Back to the path
          </Link>
        </div>
      )}
    </AppFrame>
  );
}
