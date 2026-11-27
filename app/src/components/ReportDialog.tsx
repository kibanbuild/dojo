import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { postReport, reportProblem, REPORT_MAX, REPORT_MESSAGES, type ReportKind } from '../lib/reports';

const FOCUSABLE = 'button:not([disabled]), textarea:not([disabled]), input:not([disabled])';
const btn = 'inline-flex h-11 items-center justify-center rounded-[10px] px-3.5 text-[15px] font-bold disabled:opacity-45';

/**
 * Modal report form. `kind` and `itemId` are the context: L3 passes `broken_ticket` with the ticket id, and the
 * onboarding help link passes `setup_help` (no item id needed). `onSignedOut` runs after a 401.
 */
export function ReportDialog({
  kind,
  itemId,
  onClose,
  onSignedOut,
}: {
  kind: ReportKind;
  itemId?: string;
  onClose: () => void;
  onSignedOut?: () => void;
}) {
  const titleId = useId();
  const fieldId = useId();
  const noteId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const fieldRef = useRef<HTMLTextAreaElement>(null);
  const doneRef = useRef<HTMLButtonElement>(null);
  const busyRef = useRef(false);
  const closeRef = useRef(onClose);
  const [message, setMessage] = useState('');
  const [includeOutput, setIncludeOutput] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [sent, setSent] = useState(false);

  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);

  // Focus goes back to whatever opened the dialog.
  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    fieldRef.current?.focus();
    return () => opener?.focus();
  }, []);

  // The field is read-only while sending, but the submit button disables: take focus back after an error or on success.
  useEffect(() => {
    if (error) fieldRef.current?.focus();
  }, [error]);
  useEffect(() => {
    if (sent) doneRef.current?.focus();
  }, [sent]);

  useEffect(() => {
    function onKeyDown(e: globalThis.KeyboardEvent) {
      if (e.key === 'Escape') {
        if (!busyRef.current) closeRef.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const items = [...(dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])];
      if (items.length === 0) return;
      const first = items[0]!;
      const last = items[items.length - 1]!;
      const active = document.activeElement;
      const inside = dialogRef.current?.contains(active) ?? false;
      if (e.shiftKey && (active === first || !inside)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !inside)) {
        e.preventDefault();
        first.focus();
      }
    }
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, []);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (busyRef.current) return;
    const problem = reportProblem(message);
    if (problem) {
      setError(problem);
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setError('');
    const result = await postReport({ kind, itemId, message, includeOutput: kind === 'broken_ticket' && includeOutput });
    busyRef.current = false;
    setBusy(false);
    if (result.ok) {
      setSent(true);
      return;
    }
    if (result.reason === 'signed_out') {
      onSignedOut?.();
      return;
    }
    setError(REPORT_MESSAGES[result.reason]);
  }

  const title = kind === 'broken_ticket' ? 'Report a broken ticket' : 'Get help with setup';
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 px-4 py-6">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="flex max-h-full w-full max-w-[480px] flex-col gap-4 overflow-y-auto rounded-2xl border border-line bg-surface p-5"
      >
        <h2 id={titleId} className="m-0 text-base font-extrabold">
          {title}
        </h2>
        {sent ? (
          <>
            <p role="status" className="m-0 text-sm leading-[1.55] text-fg2">
              Thanks, we got your report. We read every one.
            </p>
            <button ref={doneRef} type="button" onClick={onClose} className={`${btn} self-end bg-accent text-accentfg`}>
              Done
            </button>
          </>
        ) : (
          <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <label htmlFor={fieldId} className="text-sm font-bold">
                What went wrong?
              </label>
              <textarea
                ref={fieldRef}
                id={fieldId}
                name="message"
                rows={5}
                value={message}
                readOnly={busy}
                maxLength={REPORT_MAX * 2}
                aria-invalid={error !== ''}
                aria-describedby={noteId}
                onChange={(e) => {
                  setMessage(e.target.value);
                  setError('');
                }}
                className="min-w-0 rounded-[10px] border border-ctl bg-surface p-3 text-sm text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accentink"
              />
              <p id={noteId} role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
                {error}
              </p>
            </div>
            {kind === 'broken_ticket' && (
              <label className="flex items-center gap-2.5 text-sm text-fg2">
                <input type="checkbox" checked={includeOutput} disabled={busy} onChange={(e) => setIncludeOutput(e.target.checked)} className="size-4" />
                Include the output of my last check
              </label>
            )}
            <p className="m-0 text-[13px] text-muted">If you have a sign-in email, we reply there.</p>
            <div className="flex flex-col-reverse gap-2 md:flex-row md:justify-end">
              <button type="button" onClick={onClose} disabled={busy} className={`${btn} border border-line bg-surface text-fg`}>
                Cancel
              </button>
              <button type="submit" disabled={busy} aria-busy={busy} className={`${btn} bg-accent text-accentfg`}>
                {busy ? 'Sending...' : 'Send report'}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
