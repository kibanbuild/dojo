import { Navigate } from 'react-router';
import { useMe } from './lib/me';
import { Today } from './pages/Today';
import { Settings } from './pages/Settings';
import { Machines } from './pages/Machines';
import { PathPage } from './pages/Path';
import { SignInPage } from './pages/SignIn';
import { TicketPage } from './pages/Ticket';

function Loading() {
  return (
    <p role="status" className="p-6 font-sans text-muted">
      Loading...
    </p>
  );
}

function LoadFailed({ retry }: { retry: () => void }) {
  return (
    <div role="alert" className="p-6 font-sans">
      <p className="text-bad">Could not reach Dojo. Check your connection and try again.</p>
      <button type="button" onClick={retry} className="h-9 rounded-[10px] border border-line bg-surface px-3.5 text-sm font-bold">
        Retry
      </button>
    </div>
  );
}

// /app: signed out (401) goes to O1 at /app/signin; signed in goes to Today (L1 is home).
export function App() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <Navigate to="/today" replace />;
}

// /app/today: same guard as /app.
export function TodayRoute() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <Today me={me.data} />;
}

// /app/settings: same guard as /app.
export function SettingsRoute() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <Settings me={me.data} />;
}

// /app/settings/machines: same guard as /app.
export function MachinesRoute() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <Machines me={me.data} />;
}

// /app/path: same guard as /app.
export function PathRoute() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <PathPage me={me.data} />;
}

// /app/t/<role>/<ticket>: same guard as /app.
export function TicketRoute() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <TicketPage me={me.data} />;
}

// /app/signin: a signed-in browser has nothing to do here.
export function SignInRoute() {
  const me = useMe();
  if (me.data) return <Navigate to="/today" replace />;
  return <SignInPage />;
}
