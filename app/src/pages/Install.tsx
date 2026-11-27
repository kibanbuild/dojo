import { useId, useState, type KeyboardEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { EmailPrompt } from '../components/EmailPrompt';
import { OnboardingFrame } from '../components/OnboardingFrame';
import type { Me } from '../lib/me';
import { useOnboarding } from '../lib/onboarding';
import { leaveStep, useOnboardingLive } from '../lib/onboardingLive';
import { CopyCommand, Headline } from './Today';

type Os = 'windows' | 'mac' | 'linux';
const OS_TABS: [Os, string][] = [
  ['windows', 'Windows (WSL)'],
  ['mac', 'macOS'],
  ['linux', 'Linux'],
];

function detectOs(): Os {
  const ua = typeof navigator === 'undefined' ? '' : navigator.userAgent;
  if (/Windows/.test(ua)) return 'windows';
  return /Linux/.test(ua) && !/Android/.test(ua) ? 'linux' : 'mac';
}

const code = 'rounded bg-surface2 px-1.5 py-0.5 font-mono text-[13px]';

/** O2 Install: the real install line per OS, then `dojo login`; the `login` stream event turns the page green. */
export function Install({ me }: { me: Me }) {
  useOnboardingLive();
  const client = useQueryClient();
  const navigate = useNavigate();
  const onboarding = useOnboarding();
  const tabs = useId();
  const [os, setOs] = useState<Os>(detectOs);
  const [busy, setBusy] = useState(false);
  const data = onboarding.data;
  const connected = data?.cli_connected === true;
  const origin = data?.public_origin?.replace(/\/+$/, '');

  // Arrow keys, Home and End move between tabs and select them (automatic activation, roving tabindex).
  function onTabKey(e: KeyboardEvent<HTMLDivElement>) {
    const at = OS_TABS.findIndex(([id]) => id === os);
    const to = { ArrowRight: (at + 1) % OS_TABS.length, ArrowLeft: (at + OS_TABS.length - 1) % OS_TABS.length, Home: 0, End: OS_TABS.length - 1 }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    const next = OS_TABS[to]![0];
    setOs(next);
    document.getElementById(`${tabs}-${next}`)?.focus();
  }

  async function onContinue() {
    setBusy(true);
    await leaveStep(client, navigate);
    setBusy(false);
  }

  return (
    <OnboardingFrame step={3}>
      <EmailPrompt me={me} />
      <div className="grid grid-cols-1 gap-6 md:grid-cols-[1fr_1.15fr] md:gap-10">
        <div className="flex flex-col gap-4 md:gap-5">
          <Headline text="Day one. Set up your laptop" />
          <p className="m-0 text-[15px] leading-[1.55] text-fg2 md:text-[17px]">
            Like at any company, IT sends you a ticket before Monday. Installing your tools is your first ticket. The Dojo runs on your laptop, so do this step there.
          </p>
          <div className="flex flex-col gap-2.5 rounded-2xl border border-line bg-surface p-4 md:p-5">
            <div className="flex items-center gap-2.5">
              <span aria-hidden="true" className="inline-flex size-[34px] items-center justify-center rounded-full bg-surface2 text-[13px] font-bold text-fg2">
                IT
              </span>
              <div className="flex flex-col">
                <span className="text-sm font-bold">IT · Onboarding</span>
                <span className="font-mono text-xs text-muted">IT-001 · Monday 08:30</span>
              </div>
            </div>
            <h2 className="m-0 text-[17px] font-extrabold">Set up your laptop before Monday</h2>
            <p className="m-0 text-[15px] leading-[1.55] text-fg2">
              Welcome! Please install the dojo CLI and run the checks. Anything that breaks on the way is your first ticket, and that is fine.
            </p>
          </div>
        </div>
        <div className="flex flex-col gap-[18px] rounded-[20px] border border-line bg-surface p-4 md:p-7">
          <div
            role="tablist"
            aria-label="Your operating system"
            onKeyDown={onTabKey}
            className="inline-flex gap-1 self-start rounded-xl border border-line bg-surface2 p-1"
          >
            {OS_TABS.map(([id, label]) => (
              <button
                key={id}
                id={`${tabs}-${id}`}
                type="button"
                role="tab"
                aria-selected={os === id}
                aria-controls={`${tabs}-panel`}
                tabIndex={os === id ? 0 : -1}
                onClick={() => setOs(id)}
                className={`h-9 rounded-[10px] px-3.5 text-[13px] font-bold ${os === id ? 'bg-surface text-fg shadow-sm' : 'text-muted'}`}
              >
                {label}
              </button>
            ))}
          </div>
          <div role="tabpanel" id={`${tabs}-panel`} aria-labelledby={`${tabs}-${os}`} className="flex flex-col gap-3">
            <h2 className="m-0 text-sm font-extrabold">1. Install</h2>
            {os === 'windows' && <p className="m-0 text-sm text-fg2">Open your WSL2 terminal first, then run the same line.</p>}
            {onboarding.isPending ? (
              <p role="status" className="m-0 text-sm text-muted">
                Loading...
              </p>
            ) : origin ? (
              <CopyCommand command={`curl -fsSL ${origin}/install | sh`} />
            ) : (
              <div role="alert" className="flex flex-col items-start gap-2 text-sm text-bad">
                <p className="m-0">Could not load the install line. Check your connection and try again.</p>
                <button type="button" onClick={() => void onboarding.refetch()} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 font-bold text-fg">
                  Retry
                </button>
              </div>
            )}
            {data?.env === 'local' && (
              <p className="m-0 text-[13px] leading-normal text-muted">
                Local stack: build the CLI (<span className={code}>make build</span>) and put <span className={code}>bin/dojo</span> on your PATH.
              </p>
            )}
            <h2 className="m-0 mt-2 text-sm font-extrabold">2. Connect it to your account</h2>
            <CopyCommand command="dojo login" />
            <p className="m-0 text-[13px] leading-normal text-muted">
              It prints a code and opens GitHub. The code expires after a few minutes: if it does, run <span className={code}>dojo login</span> again for a new one.
            </p>
          </div>
          <p
            role="status"
            className={`m-0 flex items-center gap-2.5 rounded-xl px-3.5 py-3 text-sm font-bold ${connected ? 'bg-oksoft text-ok' : 'bg-surface2 text-fg2'}`}
          >
            {connected ? (
              <>
                <span aria-hidden="true">✓</span>Connected. Your computer is signed in to Dojo.
              </>
            ) : (
              <>
                <span aria-hidden="true" className="size-2.5 animate-pulse rounded-full bg-accent" />
                <span>
                  Waiting for <span className="font-mono">dojo login</span> on your computer...
                </span>
              </>
            )}
          </p>
          <div className="flex flex-col gap-1.5 text-[13px] leading-normal text-muted">
            <p className="m-0 font-bold text-fg2">The CLI sends anonymous usage stats</p>
            <p className="m-0">
              Which commands ran, check results and how long things took, so we can see where people get stuck. Never your files, your commands&apos; output or what you type.
            </p>
            <p className="m-0">
              Turn it off any time: <span className={code}>dojo telemetry off</span>
            </p>
          </div>
          <button
            type="button"
            disabled={!connected || busy}
            onClick={() => void onContinue()}
            className="inline-flex h-11 items-center justify-center self-start rounded-[10px] bg-accent px-[22px] text-[15px] font-bold text-accentfg disabled:opacity-45"
          >
            Continue
          </button>
        </div>
      </div>
    </OnboardingFrame>
  );
}
