import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { OnboardingFrame } from '../components/OnboardingFrame';
import type { Me } from '../lib/me';
import {
  choosePath,
  DEFAULT_PATH,
  joinWaitlist,
  leaveWaitlist,
  ONBOARDING_KEY,
  PATHS,
  useOnboarding,
  WRITE_MESSAGES,
  type PathOption,
  type WriteResult,
} from '../lib/onboarding';

const badge = 'inline-flex h-[26px] flex-none items-center rounded-md px-2.5 text-xs font-bold whitespace-nowrap';
const ghostBtn = 'inline-flex h-11 items-center justify-center rounded-[10px] border border-line bg-surface px-[18px] text-[15px] font-bold text-fg disabled:opacity-45';
const PROMISES: { ok: boolean; text: string }[] = [
  { ok: true, text: 'Practice that looks like your first IT job, checked on real machines' },
  { ok: true, text: 'Proof of work an employer can open and verify' },
  { ok: false, text: 'Not a title. Not a salary. We prepare you for the entry job and the first two years.' },
];

function OpenCard({ path, selected, onSelect }: { path: PathOption; selected: boolean; onSelect: () => void }) {
  return (
    <label className={`flex cursor-pointer flex-col gap-2 rounded-2xl bg-surface p-[18px] ${selected ? 'border-2 border-accent' : 'border-2 border-line'}`}>
      <span className="flex items-center gap-2.5">
        <input type="radio" name="path" value={path.slug} checked={selected} onChange={onSelect} className="m-0 size-5 accent-accent" />
        <span className="flex-1 text-[17px] font-extrabold">{path.title}</span>
        <span className={`${badge} bg-accentsoft text-accentink`}>Open now</span>
      </span>
      <span className="text-sm text-fg2">
        Destination: <b className="text-fg">{path.destination}</b>
      </span>
      <span className="font-mono text-xs leading-normal text-muted">{path.steps}</span>
      <span className="text-[13px] leading-normal text-fg2">You can start Foundations and Support L1 today. The rest of the path follows.</span>
    </label>
  );
}

function SoonCard({ path, listed, busy, problem, onToggle }: { path: PathOption; listed: boolean; busy: boolean; problem: string; onToggle: () => void }) {
  return (
    <div className="flex flex-col gap-2 rounded-2xl border border-dashed border-ctl bg-surface p-[18px]">
      <div className="flex items-center gap-2.5">
        <span className="flex-1 text-[17px] font-extrabold text-fg2">{path.title}</span>
        <span className={`${badge} bg-surface2 text-fg2`}>Coming soon</span>
      </div>
      <div className="text-sm text-fg2">
        Destination: <b className="text-fg">{path.destination}</b>
      </div>
      <div className="font-mono text-xs leading-normal text-muted">{path.steps}</div>
      <div className="flex flex-wrap items-center gap-3">
        <button type="button" onClick={onToggle} disabled={busy} aria-pressed={listed} className={ghostBtn}>
          {listed ? "You're on the list" : 'Tell me when'}
          <span className="sr-only"> for {path.title}{listed ? ' (select to leave the list)' : ''}</span>
        </button>
      </div>
      <p role="status" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
        {problem}
      </p>
    </div>
  );
}

/** O0 Pick your path. `me` is null when signed out: the choice then waits in the dojo_path cookie the API sets. */
export function Start({ me }: { me: Me | null }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState(DEFAULT_PATH);
  // Signed in, the list comes from GET /v1/onboarding; a join or leave shows at once and the refetch confirms it.
  // Signed out the cookie cannot be read back, so the page's own choices are all there is.
  const onboarding = useOnboarding(me !== null);
  const [local, setLocal] = useState<ReadonlyMap<string, boolean>>(new Map());
  // One flag per slug (and 'continue'), so overlapping writes never clear each other's.
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const setBusyFor = (key: string, on: boolean) =>
    setBusy((s) => {
      const next = new Set(s);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  const served = onboarding.data?.waitlist ?? [];
  const isListed = (slug: string) => local.get(slug) ?? served.includes(slug);
  const [problems, setProblems] = useState<Record<string, string>>({});

  const problem = (slug: string, result: WriteResult) =>
    setProblems((p) => ({ ...p, [slug]: result.ok ? '' : WRITE_MESSAGES[result.reason] }));

  async function toggle(slug: string) {
    setBusyFor(slug, true);
    const leaving = isListed(slug);
    const result = await (leaving ? leaveWaitlist(slug) : joinWaitlist(slug));
    setBusyFor(slug, false);
    problem(slug, result);
    if (!result.ok) return;
    setLocal((m) => new Map(m).set(slug, !leaving));
    if (me) await queryClient.invalidateQueries({ queryKey: ONBOARDING_KEY });
  }

  async function onContinue() {
    setBusyFor('continue', true);
    const result = await choosePath(selected);
    setBusyFor('continue', false);
    problem('continue', result);
    if (!result.ok) return;
    if (me) {
      await queryClient.invalidateQueries({ queryKey: ONBOARDING_KEY });
      await navigate('/', { replace: true });
    } else {
      await navigate('/signin');
    }
  }

  return (
    <OnboardingFrame step={1} hint="pick a path">
      <h1 className="m-0 text-[30px] leading-[1.06] font-extrabold tracking-[-1.5px] md:text-[44px]">
        Where do you <span className="bg-clip-text text-transparent" style={{ backgroundImage: 'var(--hlbg)' }}>want to work?</span>
      </h1>
      <p className="m-0 text-[15px] leading-[1.55] text-fg2">
        A path is a destination job. Every path starts with the same Foundations, and you can switch later without losing what you passed.
      </p>
      <fieldset className="m-0 min-w-0 border-0 p-0">
        <legend className="pb-3 text-xs font-bold tracking-[0.8px] text-muted uppercase">Pick your path</legend>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 md:gap-4">
          {PATHS.map((path) =>
            path.available ? (
              <OpenCard key={path.slug} path={path} selected={selected === path.slug} onSelect={() => setSelected(path.slug)} />
            ) : (
              <SoonCard
                key={path.slug}
                path={path}
                listed={isListed(path.slug)}
                busy={busy.has(path.slug)}
                problem={problems[path.slug] ?? ''}
                onToggle={() => void toggle(path.slug)}
              />
            ),
          )}
        </div>
      </fieldset>
      <div className="rounded-2xl border border-dashed border-ctl p-[18px] text-sm leading-normal text-muted">
        <b className="text-fg2">Data</b> · coming later, when someone who does the job daily can build it with us.
      </div>
      <div className="grid grid-cols-1 items-start gap-4 md:grid-cols-2 md:gap-6">
        <div className="flex items-start gap-3">
          <span className="relative inline-block size-11 flex-none overflow-hidden rounded-full bg-accentsoft">
            <img src="/cast/nora.png" alt="" className="absolute -bottom-0.5 -left-0.5 size-12" />
          </span>
          <p className="m-0 flex-1 rounded-xl border border-line bg-surface px-3 py-2.5 text-sm leading-normal">
            <b>Nora</b> · Everyone starts with the same Foundations. Pick Systems and DevOps now; the other paths open later and you keep what you passed.
          </p>
        </div>
        <div className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-5">
          <h2 className="m-0 text-xs font-bold tracking-[0.8px] text-muted uppercase">What Dojo promises</h2>
          {PROMISES.map(({ ok, text }) => (
            <p key={text} className="m-0 flex items-start gap-2.5 text-sm leading-[1.45]">
              <span
                className={`inline-flex size-5 flex-none items-center justify-center rounded-full text-xs font-extrabold ${ok ? 'bg-ok text-white' : 'box-border border-2 border-muted'}`}
              >
                {ok && <span aria-hidden="true">✓</span>}
                <span className="sr-only">{ok ? 'Included' : 'Not included'}</span>
              </span>
              <span>{text}</span>
            </p>
          ))}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={() => void onContinue()}
          disabled={busy.has('continue')}
          className="inline-flex h-11 items-center justify-center rounded-[10px] bg-accent px-[18px] text-[15px] font-bold text-accentfg disabled:opacity-45"
        >
          Continue
        </button>
        <span className="text-[13px] text-muted">English is part of every path: tickets and docs are in English.</span>
        <p role="status" className="m-0 basis-full text-[13px] font-semibold text-bad empty:hidden">
          {problems['continue'] ?? ''}
        </p>
      </div>
    </OnboardingFrame>
  );
}
