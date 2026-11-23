import { useId, useRef, useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { DeleteAccountDialog } from '../components/DeleteAccountDialog';
import { SettingsLayout } from '../components/SettingsLayout';
import { ME_KEY, type Me } from '../lib/me';
import { analytics } from '../lib/measure';
import { PATCH_MESSAGES, patchMe, usernameProblem, type Language, type PatchFailure } from '../lib/settings';

const card = 'flex flex-col gap-3 rounded-2xl border border-line bg-surface p-5';

/** Applies a PATCH result to the cache: the response body replaces the cached learner; a 401 signs out. */
function useMeCache() {
  const queryClient = useQueryClient();
  return {
    set: (me: Me | null) => queryClient.setQueryData(ME_KEY, me),
    /** After a delete nothing cached belongs to anyone: drop every query, then mark the learner signed out. */
    clearAll: () => {
      queryClient.clear();
      queryClient.setQueryData(ME_KEY, null);
    },
    /** Puts `previous` back only if the cache still holds this optimistic value; true when it did. */
    rollbackConsent: (optimistic: boolean, previous: boolean | null) => {
      const cur = queryClient.getQueryData<Me | null>(ME_KEY);
      if (!cur || cur.analytics_consent !== optimistic) return false;
      queryClient.setQueryData<Me | null>(ME_KEY, (c) => (c && c.analytics_consent === optimistic ? { ...c, analytics_consent: previous } : c));
      return true;
    },
    setLanguage: (language: Me['language']) =>
      queryClient.setQueryData<Me | null>(ME_KEY, (cur) => (cur ? { ...cur, language } : cur)),
  };
}

function UsernameCard({ me }: { me: Me }) {
  const cache = useMeCache();
  const inputId = useId();
  const noteId = `${inputId}-note`;
  const [draft, setDraft] = useState(me.username);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<PatchFailure | null>(null);
  const [saved, setSaved] = useState(false);

  const unchanged = draft === me.username;
  const problem = unchanged ? '' : usernameProblem(draft);
  const error = problem || (failure ? PATCH_MESSAGES[failure] : '');
  const canSave = !unchanged && problem === '' && !busy;

  function onChange(value: string) {
    setDraft(value.toLowerCase());
    setFailure(null);
    setSaved(false);
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!canSave) return;
    setBusy(true);
    setFailure(null);
    setSaved(false);
    const result = await patchMe({ username: draft });
    setBusy(false);
    if (result.ok) {
      cache.set(result.me);
      setDraft(result.me.username);
      setSaved(true);
    } else if (result.reason === 'signed_out') {
      cache.set(null);
    } else {
      setFailure(result.reason);
    }
  }

  const note = error || (saved ? 'Saved.' : unchanged ? '3 to 30 characters: lowercase letters, numbers and hyphens.' : 'Looks good. Save to change it.');

  return (
    <form onSubmit={onSubmit} noValidate className={card} aria-labelledby={`${inputId}-title`}>
      <h2 id={`${inputId}-title`} className="m-0 text-base font-extrabold">
        Username
      </h2>
      <p className="m-0 text-sm leading-[1.55] text-fg2">
        Your name in the Dojo: colleagues in the story use it, and later it is your profile link.
      </p>
      <div className="flex h-11 items-center overflow-hidden rounded-[10px] border border-ctl bg-surface focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-accentink">
        <span aria-hidden="true" className="flex h-full items-center border-r border-line bg-surface2 pr-2.5 pl-3 font-mono text-[13px] text-muted">
          @
        </span>
        <input
          id={inputId}
          type="text"
          name="username"
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          aria-label="Username"
          aria-describedby={noteId}
          aria-invalid={error ? true : undefined}
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          maxLength={60}
          className="h-full min-w-0 flex-1 border-0 bg-transparent px-3 font-mono text-sm text-fg outline-none"
        />
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <p
          id={noteId}
          role="status"
          aria-live="polite"
          className={`m-0 font-mono text-xs ${error ? 'text-bad' : saved ? 'text-ok' : 'text-muted'}`}
        >
          {note}
        </p>
        <button
          type="submit"
          disabled={!canSave}
          aria-busy={busy}
          className="inline-flex h-11 items-center justify-center rounded-[10px] border-0 bg-accent px-3.5 text-sm font-bold text-accentfg disabled:opacity-45 md:h-9"
        >
          {busy ? 'Saving...' : 'Save'}
        </button>
      </div>
    </form>
  );
}

function LanguageCard({ me }: { me: Me }) {
  const cache = useMeCache();
  const noteId = useId();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<PatchFailure | null>(null);
  const spanish = me.language === 'es';

  async function onToggle(next: boolean) {
    if (pending) return;
    const previous = me.language;
    const language: Language = next ? 'es' : 'en';
    setPending(true);
    setFailure(null);
    cache.set({ ...me, language });
    const result = await patchMe({ language });
    setPending(false);
    if (result.ok) {
      cache.set(result.me);
    } else if (result.reason === 'signed_out') {
      cache.set(null);
    } else {
      cache.setLanguage(previous);
      setFailure(result.reason);
    }
  }

  return (
    <section className={card} aria-labelledby={`${noteId}-title`}>
      <h2 id={`${noteId}-title`} className="m-0 text-base font-extrabold">
        Language
      </h2>
      <label className="flex min-h-11 cursor-pointer items-center justify-between gap-4 border-t border-line py-3.5">
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-bold">Explanations about the job in Spanish</span>
          <span className="text-[13px] text-muted">Tickets and docs stay in English</span>
        </span>
        <input
          type="checkbox"
          role="switch"
          checked={spanish}
          onChange={(e) => void onToggle(e.target.checked)}
          aria-describedby={noteId}
          className="peer sr-only"
        />
        <span
          aria-hidden="true"
          className="relative h-6 w-10 flex-none rounded-full bg-line transition-colors peer-checked:bg-accent peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accentink peer-checked:[&>span]:translate-x-4"
        >
          <span className="absolute top-[3px] left-[3px] h-[18px] w-[18px] rounded-full bg-white transition-transform" />
        </span>
      </label>
      <p id={noteId} role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
        {failure ? PATCH_MESSAGES[failure] : ''}
      </p>
    </section>
  );
}

function AnalyticsCard({ me }: { me: Me }) {
  const cache = useMeCache();
  const noteId = useId();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<PatchFailure | null>(null);

  async function onToggle(next: boolean) {
    if (pending) return;
    const previous = me.analytics_consent;
    setPending(true);
    setFailure(null);
    cache.set({ ...me, analytics_consent: next });
    analytics.setAccountConsent(next);
    const result = await patchMe({ analytics_consent: next });
    setPending(false);
    if (result.ok) {
      cache.set(result.me);
    } else if (result.reason === 'signed_out') {
      cache.set(null);
    } else {
      if (cache.rollbackConsent(next, previous)) analytics.setAccountConsent(previous);
      setFailure(result.reason);
    }
  }

  return (
    <section className={card} aria-labelledby={`${noteId}-title`}>
      <h2 id={`${noteId}-title`} className="m-0 text-base font-extrabold">
        Analytics
      </h2>
      <label className="flex min-h-11 cursor-pointer items-center justify-between gap-4 border-t border-line py-3.5">
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-bold">Count which pages are used</span>
          <span className="text-[13px] text-muted">Through PostHog in the EU. Nothing is sent unless this is on</span>
        </span>
        <input
          type="checkbox"
          role="switch"
          name="analytics_consent"
          checked={me.analytics_consent !== null ? me.analytics_consent : analytics.consent() === 'granted'}
          onChange={(e) => void onToggle(e.target.checked)}
          aria-describedby={noteId}
          className="peer sr-only"
        />
        <span
          aria-hidden="true"
          className="relative h-6 w-10 flex-none rounded-full bg-line transition-colors peer-checked:bg-accent peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accentink peer-checked:[&>span]:translate-x-4"
        >
          <span className="absolute top-[3px] left-[3px] h-[18px] w-[18px] rounded-full bg-white transition-transform" />
        </span>
      </label>
      <p id={noteId} role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
        {failure ? PATCH_MESSAGES[failure] : ''}
      </p>
    </section>
  );
}

function GithubCard({ me }: { me: Me }) {
  return (
    <section className={card} aria-labelledby="gh-title">
      <h2 id="gh-title" className="m-0 text-base font-extrabold">
        GitHub account
      </h2>
      <div className="flex items-center gap-3">
        <span aria-hidden="true" className="h-10 w-10 flex-none rounded-full [background:var(--hlbg)]" />
        <span className="min-w-0 truncate font-mono text-[13px] text-fg2">
          @{me.github_login} · signed in with GitHub
        </span>
      </div>
    </section>
  );
}

function DangerCard({ me }: { me: Me }) {
  const cache = useMeCache();
  const navigate = useNavigate();
  const titleId = useId();
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);

  function close() {
    setOpen(false);
    // The dialog unmounts this tick; return focus once the button is the only thing left to take it.
    queueMicrotask(() => buttonRef.current?.focus());
  }

  return (
    <section className="overflow-hidden rounded-2xl border border-bad bg-surface" aria-labelledby={titleId}>
      <h2 id={titleId} className="m-0 bg-badsoft px-5 py-3 text-[15px] font-extrabold text-bad">
        Danger zone
      </h2>
      <div className="flex flex-col gap-3 p-5 md:flex-row md:items-center md:justify-between md:gap-6">
        <div className="flex flex-col gap-1">
          <span className="text-sm font-bold">Delete your account</span>
          <span className="text-[13px] leading-normal text-muted">
            Removes your progress and story from Dojo. Your GitHub repo and its commits stay yours. You type your GitHub username to confirm.
          </span>
        </div>
        <button
          ref={buttonRef}
          type="button"
          onClick={() => setOpen(true)}
          className="inline-flex h-11 w-full flex-none items-center justify-center rounded-[10px] border-0 bg-badsoft px-4 text-[15px] font-bold text-bad md:w-auto"
        >
          Delete account
        </button>
      </div>
      {open && (
        <DeleteAccountDialog
          login={me.github_login}
          onClose={close}
          onDeleted={() => {
            void navigate('/signin?deleted=1', { replace: true });
            cache.clearAll();
          }}
          onSignedOut={() => cache.set(null)}
        />
      )}
    </section>
  );
}

export function Settings({ me }: { me: Me }) {
  return (
    <SettingsLayout me={me}>
      <GithubCard me={me} />
      <UsernameCard me={me} />
      <LanguageCard me={me} />
      <AnalyticsCard me={me} />
      <DangerCard me={me} />
    </SettingsLayout>
  );
}
