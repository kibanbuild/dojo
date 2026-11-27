import { apiFetch } from './api';
import type { DoctorCheck } from './onboarding';

/** The ids section 3.9 says O4 requires: each needs a row and none may be `fail`. Everything else is advisory. */
export const O4_REQUIRED = ['ssh_key', 'key_signing', 'key_auth', 'git_signing', 'repo', 'agents_md', 'ruleset'] as const;

/** The two steps GitHub never lets us read: the learner confirms them. They never gate O4. */
export const CONFIRMABLE = ['two_factor', 'secret_scanning'] as const;
export type ConfirmId = (typeof CONFIRMABLE)[number];

/** Same rule as the server's readiness function: every required id has a row and none is `fail`. */
export function keyReady(checks: DoctorCheck[] | undefined): boolean {
  const rows = checks ?? [];
  return O4_REQUIRED.every((id) => {
    const row = rows.find((c) => c.id === id);
    return row !== undefined && row.status !== 'fail';
  });
}

/** PUT or DELETE /v1/onboarding/confirmations/{step}; true on success. An unconfirm is allowed so a mis-click is not permanent. */
export async function setConfirmed(step: ConfirmId, confirmed: boolean): Promise<boolean> {
  try {
    const res = await apiFetch(`/v1/onboarding/confirmations/${step}`, { method: confirmed ? 'PUT' : 'DELETE' });
    return res.ok;
  } catch {
    return false;
  }
}
