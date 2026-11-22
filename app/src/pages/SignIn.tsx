import { useId, useState } from 'react';
import { useSearchParams } from 'react-router';
import { Logo } from '../components/Logo';

// Where the sign-in form posts; the server answers with a 303 to GitHub (internal/server/signin.go start).
export const START_PATH = '/v1/auth/github/start';

// The callback redirects here with ?error=<code> or ?confirm=1; GitHub's own text is never echoed.
const MESSAGES: Record<string, string> = {
  state_check: 'That sign-in attempt expired or did not match. Please try again.',
  rate_limited: 'Too many attempts. Wait a minute and try again.',
  access_denied: 'GitHub sign-in was cancelled. You can try again when you are ready.',
  github_refused: 'GitHub did not accept the sign-in. Please try again.',
  github_unavailable: 'GitHub is not answering right now. Please try again in a moment.',
  suspended: 'This account is suspended.',
  server_error: 'Something went wrong on our side. Please try again.',
};
const CONFIRM_MESSAGE = 'We do not know this GitHub account yet. Confirm both boxes below to create it.';

export function noticeFor(params: URLSearchParams): string {
  if (params.get('confirm') === '1') return CONFIRM_MESSAGE;
  const code = params.get('error');
  if (!code) return '';
  return MESSAGES[code] ?? MESSAGES.server_error!;
}

function Header() {
  return (
    <header className="border-b border-line">
      <div className="mx-auto flex max-w-[1200px] items-center justify-between gap-4 py-2 pr-2 pl-4 md:px-6 md:py-3.5">
        <div className="flex items-center gap-2 md:gap-2.5">
          <span className="md:hidden">
            <Logo size={24} stroke={2.66} />
          </span>
          <span className="hidden md:inline">
            <Logo size={28} stroke={2.66} />
          </span>
          <span className="font-mono text-base font-semibold md:text-lg">dojo</span>
        </div>
      </div>
    </header>
  );
}

export function SignInScreen({ notice }: { notice: string }) {
  const [age, setAge] = useState(false);
  const [terms, setTerms] = useState(false);
  const hintId = useId();
  const ready = age && terms;

  return (
    <div className="min-h-screen overflow-x-hidden bg-bg font-sans text-fg">
      <Header />
      <main className="relative flex justify-center px-4 py-8 md:px-6 md:py-24">
        <div
          aria-hidden="true"
          className="pointer-events-none absolute top-[-120px] left-1/2 h-[460px] w-[900px] -translate-x-1/2 md:top-[-60px]"
          style={{ background: 'radial-gradient(closest-side, var(--glow), transparent)' }}
        />
        <form
          method="post"
          action={START_PATH}
          className="relative flex w-full max-w-[440px] flex-col gap-[18px] rounded-[20px] border border-line bg-surface p-6 shadow-card md:p-9"
        >
          <Logo size={44} stroke={1.69} />
          <h1 className="m-0 text-[28px] leading-[1.1] font-extrabold tracking-[-1px] md:text-[34px]">
            Your first day{' '}
            <span className="bg-clip-text text-transparent" style={{ backgroundImage: 'var(--hlbg)' }}>
              starts here
            </span>
            <span className="-ml-[0.12em]">.</span>
          </h1>
          <p className="m-0 text-[15px] leading-[1.55] text-fg2">Sign in with GitHub. We only use it to know it's you.</p>

          <div role="status" aria-live="polite" className="empty:hidden">
            {notice && (
              <p className="m-0 rounded-[10px] bg-badsoft px-3.5 py-2.5 text-[13px] leading-normal font-semibold text-bad">{notice}</p>
            )}
          </div>

          <div className="flex flex-col gap-3 text-sm">
            <label className="flex items-start gap-3">
              <input
                type="checkbox"
                name="age"
                value="on"
                checked={age}
                onChange={(e) => setAge(e.target.checked)}
                className="mt-0.5 size-5 shrink-0 accent-accent"
              />
              <span>I am 14 or older</span>
            </label>
            <label className="flex items-start gap-3">
              <input
                type="checkbox"
                name="terms"
                value="on"
                checked={terms}
                onChange={(e) => setTerms(e.target.checked)}
                className="mt-0.5 size-5 shrink-0 accent-accent"
              />
              <span>
                I accept the{' '}
                <a href="/terms" target="_blank" rel="noopener" className="font-semibold text-accentink underline">
                  terms
                </a>
              </span>
            </label>
          </div>

          <button
            type="submit"
            disabled={!ready}
            aria-describedby={hintId}
            className="flex h-12 items-center justify-center gap-2 rounded-[10px] border-0 bg-fg px-[18px] text-[15px] font-bold text-bg disabled:opacity-45"
          >
            <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="currentColor">
              <path d="M12 .5a11.5 11.5 0 0 0-3.6 22.4c.6.1.8-.3.8-.6v-2c-3.2.7-3.9-1.5-3.9-1.5-.5-1.3-1.3-1.7-1.3-1.7-1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.7-1.6-2.6-.3-5.3-1.3-5.3-5.7 0-1.3.5-2.3 1.2-3.1-.1-.3-.5-1.5.1-3.1 0 0 1-.3 3.2 1.2a11 11 0 0 1 5.8 0c2.2-1.5 3.2-1.2 3.2-1.2.6 1.6.2 2.8.1 3.1.8.8 1.2 1.8 1.2 3.1 0 4.4-2.7 5.4-5.3 5.7.4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6A11.5 11.5 0 0 0 12 .5z" />
            </svg>
            Continue with GitHub
          </button>
          <p id={hintId} className="sr-only">
            {ready ? 'Ready to continue.' : 'Tick both boxes to enable this button.'}
          </p>

          <div className="flex flex-col gap-2 border-t border-line pt-3.5 text-[13px] text-muted">
            <div>We ask GitHub for your public profile and email. Nothing else, no access to your code.</div>
            <div>Free to learn.</div>
          </div>
        </form>
      </main>
    </div>
  );
}

export function SignInPage() {
  const [params] = useSearchParams();
  return <SignInScreen notice={noticeFor(params)} />;
}
