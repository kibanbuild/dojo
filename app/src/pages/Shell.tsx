import { AppFrame } from '../components/AppFrame';
import type { Me } from '../lib/me';

export function Shell({ me }: { me: Me }) {
  return (
    <AppFrame me={me}>
      <p className="text-fg2">Signed in. Today is on its way.</p>
    </AppFrame>
  );
}
