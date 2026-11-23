import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { DELETE_MESSAGES, deleteMe } from '../lib/settings';

const FOCUSABLE = 'button:not([disabled]), input:not([disabled])';

/** Modal that asks for the GitHub username before deleting the account. `onDeleted` runs after a 204; `onSignedOut` after a 401. */
export function DeleteAccountDialog({
  login,
  onClose,
  onDeleted,
  onSignedOut,
}: {
  login: string;
  onClose: () => void;
  onDeleted: () => void;
  onSignedOut: () => void;
}) {
  const titleId = useId();
  const descId = useId();
  const inputId = useId();
  const noteId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const sending = useRef(false);
  const busyRef = useRef(false);
  const closeRef = useRef(onClose);

  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  // The input was disabled while sending, so focus fell to <body>: take it back when an error shows.
  useEffect(() => {
    if (error) inputRef.current?.focus();
  }, [error]);

  // Document level, so Escape and the Tab trap still work after a disabled control dropped focus to <body>.
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

  const matches = typed.trim().toLowerCase() === login.toLowerCase();

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!matches || busy || sending.current) return;
    sending.current = true;
    busyRef.current = true;
    setBusy(true);
    setError('');
    const result = await deleteMe(login);
    if (result.ok) {
      onDeleted();
      return;
    }
    sending.current = false;
    busyRef.current = false;
    setBusy(false);
    if (result.reason === 'signed_out') {
      onSignedOut();
      return;
    }
    setError(DELETE_MESSAGES[result.reason]);
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 px-4 py-6">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descId}
        className="flex max-h-full w-full max-w-[460px] flex-col gap-4 overflow-y-auto rounded-2xl border border-bad bg-surface p-5"
      >
        <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
          <h2 id={titleId} className="m-0 text-base font-extrabold text-bad">
            Delete your account?
          </h2>
          <div id={descId} className="flex flex-col gap-3 text-sm leading-[1.55] text-fg2">
            <p className="m-0">
              <strong className="text-fg">This removes:</strong> your progress, your story, and your CLI sign-ins on every machine. It cannot be undone.
            </p>
            <p className="m-0">
              <strong className="text-fg">This stays:</strong> your GitHub account and your repo with its commits.
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <label htmlFor={inputId} className="text-sm font-bold">
              Type your GitHub username (@{login}) to confirm
            </label>
            <input
              ref={inputRef}
              id={inputId}
              type="text"
              name="confirm"
              value={typed}
              onChange={(e) => {
                setTyped(e.target.value);
                setError('');
              }}
              disabled={busy}
              aria-describedby={noteId}
              autoComplete="off"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              className="h-11 min-w-0 rounded-[10px] border border-ctl bg-surface px-3 font-mono text-sm text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accentink"
            />
            <p id={noteId} role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
              {error}
            </p>
          </div>
          <div className="flex flex-col-reverse gap-2 md:flex-row md:justify-end">
            <button
              type="button"
              onClick={onClose}
              disabled={busy}
              className="inline-flex h-11 items-center justify-center rounded-[10px] border border-line bg-surface px-3.5 text-[15px] font-bold text-fg disabled:opacity-45"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!matches || busy}
              aria-busy={busy}
              className="inline-flex h-11 items-center justify-center rounded-[10px] border-0 bg-bad px-3.5 text-[15px] font-bold text-white disabled:opacity-45"
            >
              {busy ? 'Deleting...' : 'Delete my account'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
