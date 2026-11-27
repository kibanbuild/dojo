import { useId, useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ME_KEY, type Me } from '../lib/me';
import { emailProblem, saveEmail, type EmailResult } from '../lib/email';

const FAILURES: Record<Exclude<EmailResult, { ok: true }>['reason'], string> = {
  invalid: 'That address is not accepted. Check it and try again.',
  already_set: '',
  signed_out: 'Your session ended. Sign in again.',
  rate_limited: 'Too many tries. Wait a minute and try again.',
  failed: 'Could not save your address. Please try again.',
};

/** The in-app email prompt (issue 67): shown only while the account has no address on file. */
export function EmailPrompt({ me, why = 'Add an email address for your welcome message and account email.' }: { me: Me; why?: string }) {
  const queryClient = useQueryClient();
  const inputId = useId();
  const [value, setValue] = useState('');
  const [problem, setProblem] = useState('');
  const [busy, setBusy] = useState(false);

  if (me.email !== null) return null;

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const local = emailProblem(value);
    setProblem(local);
    if (local) return;
    setBusy(true);
    const result = await saveEmail(value);
    setBusy(false);
    // An address already on file (another tab) ends the prompt the same way a save does.
    if (result.ok || result.reason === 'already_set') {
      await queryClient.invalidateQueries({ queryKey: ME_KEY });
      return;
    }
    setProblem(FAILURES[result.reason]);
  }

  return (
    <section aria-label="Email address" className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-4 md:flex-row md:items-start md:gap-5">
      <p className="m-0 flex-1 text-sm leading-normal text-fg2">{why}</p>
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-2 md:w-[420px]">
        <div className="flex flex-col gap-2 sm:flex-row">
          <label htmlFor={inputId} className="sr-only">
            Email address
          </label>
          <input
            id={inputId}
            type="email"
            autoComplete="email"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-invalid={problem !== ''}
            aria-describedby={`${inputId}-problem`}
            className="h-11 min-w-0 flex-1 rounded-[10px] border border-line bg-bg px-3 text-[15px] text-fg"
          />
          <button
            type="submit"
            disabled={busy}
            className="h-11 rounded-[10px] bg-accent px-[18px] text-[15px] font-bold text-accentfg disabled:opacity-45"
          >
            Save email
          </button>
        </div>
        <p id={`${inputId}-problem`} role="status" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
          {problem}
        </p>
      </form>
    </section>
  );
}
