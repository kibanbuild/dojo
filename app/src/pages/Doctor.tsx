import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { Mentor } from '../components/Mentor';
import { OnboardingFrame } from '../components/OnboardingFrame';
import { ReportDialog } from '../components/ReportDialog';
import { useOnboarding, type CheckStatus, type DoctorCheck } from '../lib/onboarding';
import { leaveStep, useOnboardingLive } from '../lib/onboardingLive';
import { PATCH_MESSAGES, patchMe } from '../lib/settings';
import { Headline } from './Today';

// The machine group of section 3.9, in display order. The first six gate the step; the rest are advisory.
const MACHINE = ['memory', 'disk', 'cpu', 'virtualization', 'vm_manager', 'git', 'git_identity', 'editor'];
const REQUIRED = MACHINE.slice(0, 6);
const WAITING = ['Memory and disk', 'Git', 'Git name and email', 'VM manager'];
const LABELS: Record<string, string> = {
  memory: 'Memory',
  disk: 'Disk',
  cpu: 'Processor',
  virtualization: 'Virtualization',
  vm_manager: 'VM manager',
  git: 'Git',
  git_identity: 'Git name and email',
  editor: 'Editor',
};

type View = 'waiting' | 'fix' | 'good' | 'blocked';
const MENTOR: Record<View, string> = {
  waiting: 'Open a terminal on your computer and type dojo doctor. This page is listening.',
  fix: 'A missing tool is the most normal ticket there is. Read the line, run the fix, run the doctor again.',
  good: 'Clean machine. Most first days take longer than this.',
  blocked: 'A locked laptop is a real first-week problem. You can go on with the next steps today and ask IT tomorrow.',
};
const GLYPH: Record<CheckStatus, { mark: string; name: string; cls: string }> = {
  ok: { mark: '✓', name: 'Passed', cls: 'bg-ok text-accentfg' },
  fail: { mark: '✗', name: 'Not yet', cls: 'bg-bad text-accentfg' },
  warn: { mark: '!', name: 'Warning', cls: 'bg-accentsoft text-accentink' },
  unknown: { mark: '?', name: 'Could not check', cls: 'bg-surface2 text-muted' },
};

function Line({ check }: { check: DoctorCheck }) {
  const g = GLYPH[check.status];
  return (
    <li className="flex flex-col gap-1 border-b border-line py-2.5 last:border-b-0">
      <div className="flex items-start gap-2.5 text-sm font-semibold">
        <span className={`inline-flex size-5 flex-none items-center justify-center rounded-full text-xs font-extrabold ${g.cls}`}>
          <span aria-hidden="true">{g.mark}</span>
          <span className="sr-only">{g.name}</span>
        </span>
        {check.text || LABELS[check.id] || check.id}
      </div>
      {check.status !== 'ok' && check.fix && <div className="ml-[30px] font-mono text-[13px] leading-normal whitespace-pre-wrap text-fg2">-&gt; {check.fix}</div>}
    </li>
  );
}

function Terminal({ lines }: { lines: DoctorCheck[] }) {
  return (
    <figure className="m-0 hidden overflow-hidden rounded-xl border border-termline bg-term font-mono md:block">
      <figcaption className="bg-termbar px-3 py-[9px] text-center text-xs text-termmute">~/dojo · mirrored from your laptop</figcaption>
      <div className="px-4 py-3.5 text-[13px] leading-[1.75] whitespace-pre-wrap text-termfg">
        <div>$ {lines.length > 0 && 'dojo doctor'}</div>
        {lines.map((l) => (
          <div key={l.id}>
            {`  ${GLYPH[l.status].mark} ${l.text || LABELS[l.id] || l.id}`}
            {l.status !== 'ok' && l.fix && <div className="text-termmute">{`      -> ${l.fix}`}</div>}
          </div>
        ))}
      </div>
    </figure>
  );
}

/** O3 Doctor: `dojo doctor` results arrive on the `doctor` stream event and in GET /v1/onboarding `checks`. */
export function Doctor() {
  useOnboardingLive();
  const client = useQueryClient();
  const navigate = useNavigate();
  const read = useOnboarding();
  const data = read.data;
  const [busy, setBusy] = useState<'' | 'continue' | 'hosted'>('');
  const [problem, setProblem] = useState('');
  const [stuck, setStuck] = useState(false);
  const [reporting, setReporting] = useState(false);

  // Only the required ids can block, so an advisory line that fails reads as amber.
  const lines = (data?.checks ?? [])
    .filter((c) => MACHINE.includes(c.id))
    .map((c): DoctorCheck => (c.status === 'fail' && !REQUIRED.includes(c.id) ? { ...c, status: 'warn' } : c))
    .sort((a, b) => MACHINE.indexOf(a.id) - MACHINE.indexOf(b.id));
  const fails = lines.filter((l) => l.status === 'fail').length;
  const warns = lines.filter((l) => l.status === 'warn').length;
  const complete = REQUIRED.every((id) => lines.some((l) => l.id === id));
  const canContinue = complete && fails === 0;
  const view: View = (stuck && fails > 0) || data?.hosted_default === true ? 'blocked' : fails > 0 || (complete && warns > 0) ? 'fix' : complete ? 'good' : 'waiting';
  const badge = { waiting: 'waiting', fix: `${fails + warns} to fix`, good: 'all good', blocked: 'blocked' }[view];

  async function next(hosted: boolean) {
    setBusy(hosted ? 'hosted' : 'continue');
    setProblem('');
    if (hosted) {
      const result = await patchMe({ hosted_default: true });
      if (!result.ok) {
        setBusy('');
        setProblem(PATCH_MESSAGES[result.reason]);
        return;
      }
    }
    await leaveStep(client, navigate);
    setBusy('');
  }

  return (
    <OnboardingFrame step={4}>
      <div className="grid grid-cols-1 gap-6 md:grid-cols-[1fr_1.15fr] md:gap-10">
        <div className="flex flex-col gap-4 md:gap-5">
          <Headline text="Check your machine" />
          <p className="m-0 text-[15px] leading-[1.55] text-fg2 md:text-[17px]">
            The doctor works like a company onboarding script: it says what is missing and how to fix it. Run <span className="font-mono">dojo doctor</span> on your computer. This page updates while you run it.
          </p>
          <Terminal lines={lines} />
          <Mentor>{MENTOR[view]}</Mentor>
        </div>
        <div className="flex flex-col gap-4">
          {data === null ? (
            <div role="alert" className="flex flex-col items-start gap-2 rounded-2xl border border-line bg-surface p-4 text-sm text-bad md:p-5">
              <p className="m-0">Could not load your results. Check your connection and try again.</p>
              <button type="button" onClick={() => void read.refetch()} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 font-bold text-fg">
                Retry
              </button>
            </div>
          ) : (
          <section aria-label="Your machine" aria-busy={view === 'waiting'} className="rounded-2xl border border-line bg-surface p-4 md:p-5">
            <div className="flex items-center justify-between gap-3 pb-2">
              <h2 className="m-0 text-base font-extrabold">Your machine</h2>
              <span role="status" className="rounded-md bg-surface2 px-2.5 py-1 text-xs font-bold text-fg2">
                {badge}
              </span>
            </div>
            <ul className="m-0 list-none p-0">
              {lines.length > 0
                ? lines.map((l) => <Line key={l.id} check={l} />)
                : WAITING.map((name) => (
                    <li key={name} className="flex items-center gap-2.5 border-b border-line py-2.5 text-sm font-semibold text-muted last:border-b-0">
                      <span aria-hidden="true" className="size-5 flex-none rounded-full border-2 border-line" />
                      <span className="sr-only">Not checked yet: </span>
                      {name}
                    </li>
                  ))}
            </ul>
          </section>
          )}
          {view === 'blocked' && (
            <section aria-label="Hosted lab" className="flex flex-col gap-2.5 rounded-2xl border border-line bg-surface2 p-4 md:p-5">
              <h2 className="m-0 text-[15px] font-extrabold">Use a hosted lab instead</h2>
              <p className="m-0 text-sm leading-[1.5] text-fg2">
                {data?.hosted_default === true
                  ? 'You already chose a hosted lab. Hosted labs arrive later: go on with your SSH key and practice repo, and start labs once hosted ones open. '
                  : 'Hosted labs arrive later. Choose this to go on with your SSH key and practice repo now. You can start labs once hosted ones open, or fix the lines above and run dojo doctor again. '}
                Turn it off in Settings any time.
              </p>
              <button
                type="button"
                disabled={busy !== ''}
                onClick={() => void next(true)}
                className="inline-flex h-11 items-center justify-center self-start rounded-[10px] bg-accent px-[18px] text-[15px] font-bold text-accentfg disabled:opacity-45"
              >
                Use a hosted lab
              </button>
              <p role="alert" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
                {problem}
              </p>
            </section>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <button
              type="button"
              disabled={!canContinue || busy !== ''}
              onClick={() => void next(false)}
              className="inline-flex h-11 items-center justify-center rounded-[10px] bg-accent px-[22px] text-[15px] font-bold text-accentfg disabled:opacity-45"
            >
              Continue
            </button>
            <button
              type="button"
              onClick={() => setReporting(true)}
              className="inline-flex h-11 items-center justify-center rounded-[10px] border border-line bg-surface px-[18px] text-[15px] font-bold text-fg"
            >
              I&apos;m stuck: get help
            </button>
            {fails > 0 && view !== 'blocked' && (
              <button type="button" onClick={() => setStuck(true)} className="h-11 text-[15px] font-bold text-accentink">
                I can&apos;t fix this
              </button>
            )}
          </div>
          <p className="m-0 text-[13px] leading-normal text-muted">Continue unlocks when no line is red. Amber lines are worth fixing but never block you.</p>
        </div>
      </div>
      {reporting && <ReportDialog kind="setup_help" onClose={() => setReporting(false)} onSignedOut={() => void navigate('/signin')} />}
    </OnboardingFrame>
  );
}
