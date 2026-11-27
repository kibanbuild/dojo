import { useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router';
import { Mentor } from '../components/Mentor';
import { OnboardingFrame } from '../components/OnboardingFrame';
import { firstView, useFirstTicketLive, type FirstView } from '../lib/firstTicket';
import { Markdown } from '../lib/markdown';
import { useOnboarding, type CommitState, type Onboarding } from '../lib/onboarding';
import { leaveStep } from '../lib/onboardingLive';
import { useTicket } from '../lib/read';
import { CopyCommand, Headline } from './Today';

const MENTOR: Record<FirstView, string> = {
  hosted: 'Hosted labs are not open yet. Your key and repo are ready, so you are not behind.',
  start: 'Nobody here expects you to know everything. They expect you to read before you ask. Start the lab, then read the ticket twice.',
  starting: 'Booting a lab takes a minute the first time. Read the ticket while you wait.',
  running: 'You are in. Look around before you change anything: read first, then fix.',
  checked: 'Not yet is a normal answer. Read the output, change one thing, check again.',
  lost: 'Labs stop sometimes. Start it again, nothing you learned is lost.',
  passed: 'First ticket done. That is a real commit on your own repo.',
};

const STATUS: Record<FirstView, string> = {
  hosted: 'Hosted labs are coming soon.',
  start: 'Waiting for dojo start on your computer...',
  starting: 'Starting your lab...',
  running: 'Lab ready. Fix the problem, then check.',
  checked: 'Checked: not passing yet.',
  lost: 'The lab stopped. Start it again.',
  passed: 'Passed.',
};

function CommitLine({ state, url }: { state: CommitState | null; url: string | null | undefined }) {
  switch (state) {
    case 'verified':
      return (
        <p className="m-0 text-sm text-fg2">
          Verified: your signed commit is on GitHub.{' '}
          {url && (
            <a href={url} target="_blank" rel="noreferrer" className="font-bold text-accentink">
              See the commit
            </a>
          )}
        </p>
      );
    case 'pending':
      return <p className="m-0 text-sm text-fg2">Commit pending: we are looking for your signed commit on GitHub. Push it if you have not yet.</p>;
    case 'unverified':
      return (
        <p className="m-0 text-sm text-fg2">
          Could not verify the commit: check that your signing key is on GitHub, that your git email is one GitHub knows (the noreply address works), and the main ruleset, then run{' '}
          <span className="font-mono">dojo check</span> again.
        </p>
      );
    default:
      return null;
  }
}

function Step({ n, done, title, children }: { n: number; done: boolean; title: string; children?: ReactNode }) {
  return (
    <li className="flex flex-col gap-2">
      <div className="flex items-center gap-2.5 text-sm font-extrabold">
        <span className={`inline-flex size-6 flex-none items-center justify-center rounded-full text-xs ${done ? 'bg-ok text-accentfg' : 'border border-line'}`}>
          {done ? <span aria-hidden="true">✓</span> : n}
          {done && <span className="sr-only">Done</span>}
        </span>
        {title}
      </div>
      {children && <div className="ml-[34px] flex flex-col gap-2">{children}</div>}
    </li>
  );
}

function Ticket({ first, state }: { first: NonNullable<Onboarding['first_ticket']>; state: Onboarding }) {
  const { id, title } = first;
  const client = useQueryClient();
  const navigate = useNavigate();
  const ticket = useTicket(id);
  const [busy, setBusy] = useState(false);
  const progress = ticket.data?.progress;
  const view = firstView(state.hosted_default === true, first.state ?? null, progress);
  const started = view !== 'start' && view !== 'hosted' && view !== 'lost';
  const commit = first.commit_state ?? progress?.passed_commit_state ?? null;
  // The backstop: onboarding is over, so Today is always open to a learner who comes back to this page.
  const canOpen = view === 'passed' || state.next_step === 'done';

  async function onOpen() {
    setBusy(true);
    await leaveStep(client, navigate);
    setBusy(false);
  }

  return (
    <div className="grid grid-cols-1 gap-6 md:grid-cols-[1fr_1.15fr] md:gap-10">
      <div className="flex flex-col gap-4 md:gap-5">
        <Headline text="Your first ticket" />
        <Mentor>{MENTOR[view]}</Mentor>
        <section aria-label="Ticket" className="flex flex-col gap-2.5 rounded-2xl border border-line bg-surface p-4 md:p-5">
          <span className="font-mono text-xs text-muted">{id}</span>
          <h2 className="m-0 text-[17px] font-extrabold">{ticket.data?.title ?? title}</h2>
          {ticket.data?.brief && (
            <div className="max-h-56 overflow-auto text-[15px] leading-[1.55] text-fg2">
              <Markdown source={ticket.data.brief} />
            </div>
          )}
          {ticket.isError && (
            <div role="alert" className="flex items-center gap-3 text-sm text-bad">
              Could not load the ticket text.
              <button type="button" onClick={() => void ticket.refetch()} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 font-bold text-fg">
                Retry
              </button>
            </div>
          )}
        </section>
      </div>
      <div className="flex flex-col gap-[18px] rounded-[20px] border border-line bg-surface p-4 md:p-7">
        {view === 'hosted' ? (
          <section aria-label="Hosted lab" className="flex flex-col gap-2.5">
            <h2 className="m-0 text-[17px] font-extrabold">Hosted labs are coming soon</h2>
            <p className="m-0 text-sm leading-[1.5] text-fg2">
              You chose a hosted lab, and <span className="font-mono">dojo start</span> will say the same. Turn the hosted default off in Settings to start a local lab.
            </p>
            <Link to="/settings" className="self-start text-[15px] font-bold text-accentink">
              Open settings
            </Link>
          </section>
        ) : (
          <ol className="m-0 flex list-none flex-col gap-4 p-0">
            <Step n={1} done={started} title="Start the lab">
              {!started && <CopyCommand command={`dojo start ${id}`} />}
            </Step>
            <Step n={2} done={view === 'passed'} title="Find the problem and fix it">
              {started && view !== 'passed' && (
                <>
                  <CopyCommand command="dojo ssh" />
                  <CopyCommand command="dojo check" />
                </>
              )}
            </Step>
            <Step n={3} done={view === 'passed'} title="Pass the ticket">
              <p className="m-0 text-sm text-fg2">A pass writes a signed commit in your practice repo.</p>
            </Step>
          </ol>
        )}
        <p role="status" className={`m-0 flex items-center gap-2.5 rounded-xl px-3.5 py-3 text-sm font-bold ${view === 'passed' ? 'bg-oksoft text-ok' : 'bg-surface2 text-fg2'}`}>
          {view === 'passed' ? <span aria-hidden="true">✓</span> : <span aria-hidden="true" className="size-2.5 animate-pulse rounded-full bg-accent" />}
          {STATUS[view]}
        </p>
        {view === 'passed' && <CommitLine state={commit} url={progress?.passed_commit_url} />}
        <button
          type="button"
          disabled={!canOpen || busy}
          onClick={() => void onOpen()}
          className="inline-flex h-11 items-center justify-center self-start rounded-[10px] bg-accent px-[22px] text-[15px] font-bold text-accentfg disabled:opacity-45"
        >
          Open Today
        </button>
      </div>
    </div>
  );
}

/** O5 First ticket: `dojo start <id>` for `first_ticket`, then the lab and the signed-commit pass arrive on `progress` events. */
export function First() {
  useFirstTicketLive();
  const onboarding = useOnboarding();
  // The path's first ticket is the same one forever, with the learner's state and commit state on it, so a reload shows the same page.
  const state = onboarding.data;
  const first = state?.first_ticket;

  return (
    <OnboardingFrame step={6}>
      {state && first ? (
        <Ticket key={first.id} first={first} state={state} />
      ) : onboarding.isPending ? (
        <p role="status" className="m-0 text-sm text-muted">
          Loading...
        </p>
      ) : (
        <div role="alert" className="flex flex-col items-start gap-2 text-sm text-bad">
          <p className="m-0">Could not find your first ticket. Check your connection and try again.</p>
          <button type="button" onClick={() => void onboarding.refetch()} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 font-bold text-fg">
            Retry
          </button>
        </div>
      )}
    </OnboardingFrame>
  );
}
