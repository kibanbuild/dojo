import { useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { Mentor } from '../components/Mentor';
import { OnboardingFrame } from '../components/OnboardingFrame';
import type { Me } from '../lib/me';
import { ONBOARDING_KEY, useOnboarding, type CheckStatus, type DoctorCheck } from '../lib/onboarding';
import { leaveStep, useOnboardingLive } from '../lib/onboardingLive';
import { keyReady, setConfirmed, type ConfirmId } from '../lib/keyStep';
import { CopyCommand, Headline } from './Today';

const PUBKEY = 'cat ~/.ssh/id_ed25519.pub';
const SSH_NEW = 'https://github.com/settings/ssh/new';

interface Step {
  id: string;
  title: string;
  tag?: string;
  note: ReactNode;
  commands?: string[];
  link?: { href: string; label: string };
  /** The AGENTS.md copy block. */
  block?: boolean;
  /** GitHub shows this to nobody but its owner, so the learner confirms it. */
  confirm?: ConfirmId;
}

const mono = 'rounded bg-surface2 px-1.5 py-0.5 font-mono text-[13px]';

function stepsFor(login: string): Step[] {
  return [
    {
      id: 'ssh_key',
      title: 'Create your SSH key',
      note: 'Press Enter to accept the default file name. A passphrase is optional. Already have a key? Skip this and use the path of its .pub file instead of ~/.ssh/id_ed25519.pub in the commands below.',
      commands: [`ssh-keygen -t ed25519 -C "${login}"`],
    },
    {
      id: 'key_signing',
      title: 'Add the key to GitHub for signing',
      note: 'Paste the public key, choose Signing Key as the key type and save. This is what makes your commits show Verified.',
      commands: [PUBKEY],
      link: { href: SSH_NEW, label: 'Open GitHub SSH settings' },
    },
    {
      id: 'key_auth',
      title: 'Add the same key again for authentication',
      tag: 'Only if you clone over SSH',
      note: 'GitHub keeps signing and authentication keys apart, so the key goes in twice. Choose Authentication Key. The GitHub CLI clones over HTTPS unless you set it to SSH; an HTTPS clone reports Not needed.',
      link: { href: SSH_NEW, label: 'Open GitHub SSH settings' },
    },
    {
      id: 'git_signing',
      title: 'Turn on commit signing',
      note: 'Needs git 2.34 or newer. With an existing key, put the path of its .pub file in user.signingkey.',
      commands: ['git config --global gpg.format ssh', 'git config --global user.signingkey ~/.ssh/id_ed25519.pub', 'git config --global commit.gpgsign true'],
    },
    {
      id: 'repo',
      title: `Create your public practice repo ${login}/dojo`,
      note: 'It must be public: every ticket you pass becomes a signed commit in it. No GitHub CLI? Create it on the web and clone it into ~/dojo.',
      commands: ['cd ~ && gh repo create dojo --public --clone'],
      link: { href: 'https://github.com/new', label: 'Create it on GitHub' },
    },
    {
      id: 'agents_md',
      title: 'Add AGENTS.md to the repo',
      note: 'The rules your own AI mentor follows: it helps you learn and never solves the ticket. Save this text as AGENTS.md in ~/dojo, then commit and push it.',
      block: true,
      commands: ['cd ~/dojo && git add AGENTS.md && git commit -m "Add AGENTS.md" && git branch -M main && git push -u origin main'],
    },
    {
      id: 'ruleset',
      title: 'Protect the main branch with a ruleset',
      note: 'Name it main, set Enforcement to Active and add the default branch as target. Turn on Restrict deletions, Block force pushes and Require signed commits, then save.',
      link: { href: `https://github.com/${login}/dojo/settings/rules/new?target=branch`, label: 'Open the new ruleset form' },
    },
    {
      id: 'two_factor',
      title: 'Two-factor authentication on GitHub',
      note: 'Worth doing for any account that holds code. We cannot see your settings, so you confirm it.',
      link: { href: 'https://github.com/settings/security', label: 'Open GitHub security settings' },
      confirm: 'two_factor',
    },
    {
      id: 'secret_scanning',
      title: 'Secret scanning and push protection',
      note: 'Check both are on for the repo, so a key pasted by mistake is caught. We cannot see them, so you confirm it.',
      link: { href: `https://github.com/${login}/dojo/settings/security_analysis`, label: 'Open the repo security settings' },
      confirm: 'secret_scanning',
    },
  ];
}

const GLYPH: Record<CheckStatus | 'waiting', { mark: string; name: string; cls: string }> = {
  ok: { mark: '✓', name: 'Passed', cls: 'bg-ok text-accentfg' },
  fail: { mark: '✗', name: 'Not yet', cls: 'bg-bad text-accentfg' },
  warn: { mark: '!', name: 'Warning', cls: 'bg-accentsoft text-accentink' },
  unknown: { mark: '?', name: 'Could not check', cls: 'bg-surface2 text-muted' },
  waiting: { mark: '', name: 'Not checked yet', cls: 'border-2 border-line' },
};

function Line({ step, row, agentsMd, busy, onConfirm }: { step: Step; row: DoctorCheck | undefined; agentsMd: string; busy: boolean; onConfirm: (on: boolean) => void }) {
  const status = row?.status ?? 'waiting';
  const g = GLYPH[status];
  const open = status !== 'ok';
  return (
    <li className="flex flex-col gap-2 border-b border-line py-3 last:border-b-0">
      <div className="flex items-start gap-2.5 text-sm font-semibold">
        <span className={`inline-flex size-5 flex-none items-center justify-center rounded-full text-xs font-extrabold ${g.cls}`}>
          <span aria-hidden="true">{g.mark}</span>
          <span className="sr-only">{g.name}</span>
        </span>
        <span className="flex-1">{step.title}</span>
        {step.tag && <span className="rounded-md bg-surface2 px-2 py-0.5 text-xs font-bold text-fg2">{step.tag}</span>}
        {step.confirm && status === 'ok' && (
          <button type="button" disabled={busy} onClick={() => onConfirm(false)} className="text-[13px] font-bold text-accentink disabled:opacity-45">
            Undo
          </button>
        )}
      </div>
      {row && status !== 'ok' && row.text && <p className="m-0 ml-[30px] text-[13px] text-fg2">{row.text}</p>}
      {row && status !== 'ok' && row.fix && <div className="ml-[30px] font-mono text-[13px] leading-normal whitespace-pre-wrap text-fg2">-&gt; {row.fix}</div>}
      {open && (
        <div className="ml-[30px] flex flex-col gap-2.5">
          <p className="m-0 text-[13px] leading-normal text-muted">{step.note}</p>
          {step.block && agentsMd && <CopyBlock text={agentsMd} />}
          {step.commands?.map((c) => <CopyCommand key={c} command={c} />)}
          {step.link && (
            <a href={step.link.href} target="_blank" rel="noreferrer" className="self-start text-[13px] font-bold text-accentink">
              {step.link.label}
            </a>
          )}
          {step.confirm && (
            <button
              type="button"
              disabled={busy}
              onClick={() => onConfirm(true)}
              className="inline-flex h-9 items-center justify-center self-start rounded-[10px] border border-line bg-surface px-3.5 text-[13px] font-bold text-fg disabled:opacity-45"
            >
              I did this
            </button>
          )}
        </div>
      )}
    </li>
  );
}

function CopyBlock({ text }: { text: string }) {
  const [notice, setNotice] = useState('');
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setNotice('Copied');
    } catch {
      setNotice('Copy failed, select the text instead');
    }
  }
  return (
    <div className="flex flex-col gap-1.5">
      <pre aria-label="AGENTS.md" className="m-0 max-h-48 overflow-auto rounded-[10px] border border-ctl bg-surface2 p-3 font-mono text-xs leading-normal whitespace-pre-wrap text-fg">{text}</pre>
      <div className="flex items-center gap-3">
        <button type="button" onClick={() => void copy()} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 text-[13px] font-bold text-fg">
          Copy AGENTS.md
        </button>
        <span role="status" className="text-[13px] text-muted">
          {notice}
        </span>
      </div>
    </div>
  );
}

/** O4 SSH key and repo: the steps with their commands; `dojo doctor` turns each line green over the `doctor` stream event. */
export function Key({ me }: { me: Me }) {
  useOnboardingLive();
  const client = useQueryClient();
  const navigate = useNavigate();
  const onboarding = useOnboarding();
  const data = onboarding.data;
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState('');
  const checks = data?.checks ?? [];
  const steps = stepsFor(me.github_login);
  const required = steps.filter((s) => !s.confirm);
  const done = required.filter((s) => checks.find((c) => c.id === s.id)?.status === 'ok').length;
  const fails = required.filter((s) => checks.find((c) => c.id === s.id)?.status === 'fail').length;
  const ready = keyReady(checks);

  async function confirm(id: ConfirmId, on: boolean) {
    setBusy(true);
    setProblem('');
    const ok = await setConfirmed(id, on);
    if (ok) await client.invalidateQueries({ queryKey: ONBOARDING_KEY });
    else setProblem('Could not save that. Please try again.');
    setBusy(false);
  }

  async function onContinue() {
    setBusy(true);
    await leaveStep(client, navigate);
    setBusy(false);
  }

  return (
    <OnboardingFrame step={5}>
      <div className="grid grid-cols-1 gap-6 md:grid-cols-[1fr_1.3fr] md:gap-10">
        <div className="flex flex-col gap-4 md:gap-5">
          <Headline text="Your key. Your repo" />
          <p className="m-0 text-[15px] leading-[1.55] text-fg2 md:text-[17px]">
            One SSH key opens your lab and signs your commits, so GitHub shows them as Verified. Your practice repo gets the minimum security a company repo would. We never get write access to your GitHub.
          </p>
          <p className="m-0 text-[15px] leading-[1.55] text-fg2">
            Do a step, then run <span className={mono}>dojo doctor</span> on your computer. This page turns each line green as it checks.
          </p>
          <Mentor>
            {fails > 0 ? 'A red line is a to-do, not a failure. Run the fix, then the doctor again.' : ready ? 'Keys, signing and repo in one sitting. Your commits are yours from here.' : 'Take it one line at a time. The doctor tells you what is missing.'}
          </Mentor>
        </div>
        <section aria-label="Your key and repo" className="flex flex-col gap-4 rounded-2xl border border-line bg-surface p-4 md:p-5">
          <div className="flex items-center justify-between gap-3">
            <h2 className="m-0 text-base font-extrabold">{me.github_login}/dojo</h2>
            <span role="status" className="rounded-md bg-surface2 px-2.5 py-1 text-xs font-bold text-fg2">
              {done} of {required.length} done
            </span>
          </div>
          {onboarding.isPending ? (
            <p role="status" className="m-0 text-sm text-muted">
              Loading...
            </p>
          ) : (
            <ul className="m-0 list-none p-0">
              {steps.map((s) => (
                <Line key={s.id} step={s} row={checks.find((c) => c.id === s.id)} agentsMd={data?.agents_md ?? ''} busy={busy} onConfirm={(on) => s.confirm && void confirm(s.confirm, on)} />
              ))}
            </ul>
          )}
          <p role="alert" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
            {problem}
          </p>
          <div className="flex flex-wrap items-center gap-3">
            <button
              type="button"
              disabled={!ready || busy}
              onClick={() => void onContinue()}
              className="inline-flex h-11 items-center justify-center rounded-[10px] bg-accent px-[22px] text-[15px] font-bold text-accentfg disabled:opacity-45"
            >
              Continue
            </button>
            <span className="text-[13px] leading-normal text-muted">Continue unlocks when the first seven steps are green. Two-factor and secret scanning never block you.</span>
          </div>
        </section>
      </div>
    </OnboardingFrame>
  );
}
