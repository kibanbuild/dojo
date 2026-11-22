import { Navigate } from 'react-router';
import { useMe } from './lib/me';
import { Shell } from './pages/Shell';
import { SignInPage } from './pages/SignIn';

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

// /app: signed out (401) goes to O1 at /app/signin; signed in sees the shell.
export function App() {
  const me = useMe();
  if (me.isPending) return <Loading />;
  if (me.isError) return <LoadFailed retry={() => void me.refetch()} />;
  if (me.data === null) return <Navigate to="/signin" replace />;
  return <Shell me={me.data} />;
}

// /app/signin: a signed-in browser has nothing to do here.
export function SignInRoute() {
  const me = useMe();
  if (me.data) return <Navigate to="/" replace />;
  return <SignInPage />;
}
