import { useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router';
import { Logo } from './Logo';
import { TabBar, TopNav } from './PrimaryNav';
import { apiFetch } from '../lib/api';
import type { Me } from '../lib/me';

/** POST /v1/auth/logout. 204, and 401/404 (already signed out, endpoint not live yet) all mean "go to O1". */
export async function signOut(): Promise<boolean> {
  const res = await apiFetch('/v1/auth/logout', { method: 'POST', body: '{}' });
  return res.status === 204 || res.status === 401 || res.status === 404;
}

/** The signed-in shell: header (home, Settings, sign out) around a page. */
export function AppFrame({ me, children }: { me: Me; children: ReactNode }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  async function onSignOut() {
    setBusy(true);
    setFailed(false);
    const ok = await signOut().catch(() => false);
    if (ok) {
      queryClient.clear();
      await navigate('/signin', { replace: true });
      return;
    }
    setBusy(false);
    setFailed(true);
  }

  return (
    <div className="min-h-screen bg-bg font-sans text-fg">
      <header className="border-b border-line">
        <div className="mx-auto flex max-w-[1200px] items-center justify-between gap-4 py-2 pr-2 pl-4 md:px-6 md:py-3.5">
          <div className="flex items-center gap-5">
            <Link to="/today" aria-label="Dojo, home" className="flex min-h-11 items-center gap-2.5 text-fg no-underline">
              <Logo size={28} stroke={2.66} />
              <span className="font-mono text-base font-semibold md:text-lg">dojo</span>
            </Link>
            <TopNav />
          </div>
          <div className="flex items-center gap-3">
            <Link
              to="/settings"
              aria-label={`Settings, signed in as ${me.username}`}
              className="flex min-h-11 items-center font-mono text-[13px] text-fg2 no-underline hover:text-fg"
            >
              {me.username}
            </Link>
            <button
              type="button"
              onClick={onSignOut}
              disabled={busy}
              className="h-9 rounded-[10px] border border-line bg-surface px-3.5 text-sm font-bold text-fg disabled:opacity-45"
            >
              Sign out
            </button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-[1200px] px-4 pt-6 pb-28 md:px-6 md:py-9">
        <p role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
          {failed ? 'Could not sign out. Please try again.' : ''}
        </p>
        {children}
      </main>
      <TabBar />
    </div>
  );
}
