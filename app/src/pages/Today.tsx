import { useEffect, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { AppFrame } from '../components/AppFrame';
import { ME_KEY, type Me } from '../lib/me';
import { READ_MESSAGES, ReadError, useToday, type Today as TodayData, type TodayTicket } from '../lib/read';
import { useLiveData } from '../lib/stream';

const card = 'flex flex-col gap-3 rounded-2xl border border-line bg-surface p-5';
const badge = 'inline-flex h-[26px] flex-none items-center rounded-md px-2.5 text-xs font-bold whitespace-nowrap';
const primaryBtn =
  'inline-flex h-11 items-center justify-center rounded-[10px] bg-accent px-[18px] text-[15px] font-bold text-accentfg no-underline';
const ghostBtn =
  'inline-flex h-11 items-center justify-center rounded-[10px] px-[18px] text-[15px] font-bold text-fg2 no-underline hover:text-fg';
const mono = 'font-mono text-muted';

/** Ticket ids hold a slash (`support-l1/perms-03`); each segment is escaped, the slash stays. */
export const ticketHref = (id: string) => `/t/${id.split('/').map(encodeURIComponent).join('/')}`;

const shortId = (id: string) => id.slice(id.indexOf('/') + 1);

/** The title with its last two words picked out by the accent gradient, as the board does. */
function Headline({ text }: { text: string }) {
  const words = text.split(' ');
  const head = words.length > 3 ? words.slice(0, -2).join(' ') + ' ' : '';
  const tail = words.length > 3 ? words.slice(-2).join(' ') : text;
  return (
    <h1 className="m-0 text-[32px] leading-[1.08] font-extrabold tracking-[-1px] md:text-[44px] md:leading-[1.05] md:tracking-[-1.4px]">
      {head}
      <span style={{ background: 'var(--hlbg)', WebkitBackgroundClip: 'text', backgroundClip: 'text', color: 'transparent' }}>{tail}</span>
    </h1>
  );
}

function RoleCard({ role, note }: { role: NonNullable<TodayData['role']>; note: string }) {
  const { tickets_passed: passed, tickets_total: total } = role;
  const label = `${role.title}, ${passed} of ${total} tickets`;
  return (
    <section aria-label={role.title} className="flex flex-col gap-3.5 rounded-[20px] border border-line bg-surface p-5 md:p-[22px]">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="m-0 text-xl font-extrabold md:text-[22px]">{role.title}</h2>
        <span className={`${mono} text-[13px]`}>
          {passed} of {total} tickets
        </span>
      </div>
      <div
        role="progressbar"
        aria-label={label}
        aria-valuenow={passed}
        aria-valuemin={0}
        aria-valuemax={total}
        className="h-2.5 overflow-hidden rounded-full bg-surface2"
      >
        <span className="block h-full bg-accent" style={{ width: `${total > 0 ? Math.min(100, (passed / total) * 100) : 0}%` }} />
      </div>
      <p className="m-0 text-sm leading-normal text-fg2">{note}</p>
    </section>
  );
}

function roleNote(role: TodayData['role'], allPassed: boolean): string {
  if (!role) return '';
  if (allPassed) return 'Every ticket is passed.';
  const left = role.tickets_total - role.tickets_passed;
  return `${left} ${left === 1 ? 'ticket' : 'tickets'} to go.`;
}

function CopyCommand({ command }: { command: string }) {
  const [notice, setNotice] = useState('');
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setNotice('Copied');
    } catch {
      setNotice('Copy failed, select the command instead');
    }
  }
  return (
    <div className="flex flex-col gap-1">
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={`Copy command: ${command}`}
        className="flex h-11 items-center gap-2.5 rounded-[10px] border border-ctl bg-surface pr-2 pl-3.5 font-mono text-sm font-semibold text-fg"
      >
        <span className="text-muted">$</span>
        {command}
        <span className="ml-auto text-xs font-bold text-fg2" aria-hidden="true">
          Copy
        </span>
      </button>
      <p role="status" className="m-0 text-xs font-bold text-fg2 empty:hidden">
        {notice}
      </p>
    </div>
  );
}

function CurrentView({ current, next, role }: { current: TodayTicket; next: TodayTicket | null; role: TodayData['role'] }) {
  return (
    <div className="grid items-start gap-6 md:grid-cols-[1fr_340px] md:gap-7">
      <div className="flex min-w-0 flex-col gap-5">
        <div className="flex flex-col gap-2.5">
          <div className="flex flex-wrap items-center gap-2.5">
            <span className={`${badge} bg-accentsoft text-accentink`}>In progress</span>
            <span className={`${mono} text-xs md:text-[13px]`}>
              {current.id}
              {current.lab_state ? ` · lab ${current.lab_state}` : ''}
            </span>
          </div>
          <Headline text={current.title} />
        </div>
        <section aria-label="Your lab" className={card}>
          <div className="flex items-center justify-between gap-3">
            <h2 className="m-0 text-[15px] font-extrabold">Your lab</h2>
            {current.lab_state && <span className={`${badge} bg-surface2 font-mono text-fg2`}>{current.lab_state}</span>}
          </div>
          <p className="m-0 text-sm leading-normal text-fg2">
            Fix it on your computer, then run <code className="rounded-md bg-surface2 px-1.5 py-px font-mono text-[13px]">dojo check</code>.
          </p>
          <div className="flex flex-col gap-2.5">
            <Link to={ticketHref(current.id)} className={primaryBtn}>
              Continue the ticket
            </Link>
            <CopyCommand command="dojo check" />
          </div>
        </section>
        {next && (
          <section aria-label="Now and next" className="overflow-hidden rounded-2xl border border-line bg-surface">
            <h2 className="m-0 border-b border-line px-5 py-4 text-base font-extrabold">Now and next</h2>
            <ul className="m-0 list-none p-0">
              <Row ticket={current} tag="Now" tone="now" />
              <Row ticket={next} tag="Next" tone="next" />
            </ul>
          </section>
        )}
      </div>
      {role && <RoleCard role={role} note={roleNote(role, false)} />}
    </div>
  );
}

function Row({ ticket, tag, tone }: { ticket: TodayTicket; tag: string; tone: 'now' | 'next' }) {
  const tagCls = tone === 'now' ? 'bg-accent text-accentfg' : 'bg-surface2 text-muted';
  return (
    <li className={`border-b border-line last:border-b-0 ${tone === 'now' ? 'bg-accentsoft' : ''}`}>
      <Link to={ticketHref(ticket.id)} className="flex min-h-11 items-center gap-3.5 px-5 py-3 text-sm text-fg2 no-underline">
        <span className={`${badge} ${tagCls}`}>{tag}</span>
        <span className="hidden w-[190px] flex-none font-mono sm:inline">{ticket.id}</span>
        <span className={tone === 'now' ? 'font-bold text-fg' : ''}>{ticket.title}</span>
      </Link>
    </li>
  );
}

// The "day one" welcome variant returns with O5 in M3, when the first pass can be told apart.
function NextView({ next, role }: { next: TodayTicket; role: TodayData['role'] }) {
  return (
    <div className="grid items-start gap-6 md:grid-cols-[1fr_340px] md:gap-7">
      <div className="flex min-w-0 flex-col gap-5">
        <Headline text="Ready for the next ticket." />
        <section aria-label="Next up" className={card}>
          <h2 className="m-0 text-base font-extrabold">Next up</h2>
          <p className="m-0 text-[17px] font-extrabold">
            <span className="font-mono text-muted">{shortId(next.id)}</span> · {next.title}
          </p>
          <div className="flex flex-wrap gap-2.5">
            <Link to={ticketHref(next.id)} className={primaryBtn}>
              Start {shortId(next.id)}
            </Link>
            <Link to="/path" className={ghostBtn}>
              See the whole path
            </Link>
          </div>
        </section>
      </div>
      {role && <RoleCard role={role} note={roleNote(role, false)} />}
    </div>
  );
}

function Message({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex max-w-xl flex-col gap-3">
      <h1 className="m-0 text-[32px] leading-[1.08] font-extrabold tracking-[-1px] md:text-[44px]">{title}</h1>
      {children}
    </div>
  );
}

function PlainTitle() {
  return <h1 className="m-0 text-[32px] leading-[1.08] font-extrabold tracking-[-1px] md:text-[44px]">Today</h1>;
}

function Failed({ error, retry }: { error: Error; retry: () => void }) {
  const reason = error instanceof ReadError ? error.reason : 'failed';
  return (
    <div role="alert" className="flex flex-col items-start gap-3">
      <p className="m-0 text-bad">{READ_MESSAGES[reason]}</p>
      {reason !== 'suspended' && (
        <button type="button" onClick={retry} className="h-11 rounded-[10px] border border-line bg-surface px-3.5 text-sm font-bold text-fg">
          Retry
        </button>
      )}
    </div>
  );
}

/** L1 Today: the ticket in progress, or the next one; role progress on the side. */
export function Today({ me }: { me: Me }) {
  useLiveData();
  const today = useToday();
  const queryClient = useQueryClient();
  const signedOut = today.isError && today.error instanceof ReadError && today.error.reason === 'signed_out';

  // The cached learner is stale: clear it so /signin does not bounce back here.
  useEffect(() => {
    if (signedOut) queryClient.setQueryData(ME_KEY, null);
  }, [signedOut, queryClient]);
  if (signedOut) return null;

  let body: ReactNode;
  if (today.isPending) {
    body = (
      <div className="flex flex-col gap-3">
        <PlainTitle />
        <p role="status" className="m-0 text-muted">
          Loading today...
        </p>
      </div>
    );
  } else if (today.isError) {
    body = (
      <div className="flex flex-col gap-3">
        <PlainTitle />
        <Failed error={today.error} retry={() => void today.refetch()} />
      </div>
    );
  } else {
    const { current, next, role, all_passed: allPassed } = today.data;
    if (current) body = <CurrentView current={current} next={next} role={role} />;
    else if (next) body = <NextView next={next} role={role} />;
    else if (allPassed && role) {
      body = (
        <div className="grid items-start gap-6 md:grid-cols-[1fr_340px] md:gap-7">
          <Message title="Every ticket is passed.">
            <p className="m-0 text-fg2">Nothing is waiting for you right now.</p>
            <Link to="/path" className={`${primaryBtn} self-start`}>
              See your path
            </Link>
          </Message>
          <RoleCard role={role} note={roleNote(role, true)} />
        </div>
      );
    } else {
      body = (
        <Message title="Nothing to work on yet.">
          <p className="m-0 text-fg2">Pick up a ticket from your path.</p>
          <Link to="/path" className={`${primaryBtn} self-start`}>
            See your path
          </Link>
        </Message>
      );
    }
  }

  return <AppFrame me={me}>{body}</AppFrame>;
}
