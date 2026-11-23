import { apiFetch } from './api';
import type { Me } from './me';

export type Language = 'en' | 'es';

export const USERNAME_PATTERN = /^[a-z0-9-]{3,30}$/;

/** Inline message for a username that cannot be saved, or '' when it is fine. */
export function usernameProblem(value: string): string {
  if (value === '') return 'Pick a username.';
  if (value.length < 3) return 'Use at least 3 characters.';
  if (value.length > 30) return 'Use at most 30 characters.';
  if (!USERNAME_PATTERN.test(value)) return 'Use only lowercase letters, numbers and hyphens.';
  return '';
}

export type PatchResult = { ok: true; me: Me } | { ok: false; reason: PatchFailure };
export type PatchFailure =
  | 'username_taken'
  | 'username_invalid'
  | 'invalid'
  | 'suspended'
  | 'signed_out'
  | 'unavailable'
  | 'failed';

export const PATCH_MESSAGES: Record<PatchFailure, string> = {
  username_taken: 'That username is taken. Try another one.',
  username_invalid: 'That username is not available. Try another one.',
  invalid: 'That value is not accepted. Check it and try again.',
  suspended: 'This account is suspended, so it cannot be changed.',
  signed_out: 'Your session ended. Sign in again.',
  unavailable: 'Saving is not available yet. Try again later.',
  failed: 'Could not save. Please try again.',
};

/** PATCH /v1/me with a partial body; maps every failure to a reason the UI can word. */
export async function patchMe(body: { username?: string; language?: Language }): Promise<PatchResult> {
  let res: Response;
  try {
    res = await apiFetch('/v1/me', { method: 'PATCH', body: JSON.stringify(body) });
  } catch {
    return { ok: false, reason: 'failed' };
  }
  if (res.ok) {
    try {
      return { ok: true, me: (await res.json()) as Me };
    } catch {
      return { ok: false, reason: 'failed' };
    }
  }
  if (res.status === 401) return { ok: false, reason: 'signed_out' };
  if (res.status === 404 || res.status === 405) return { ok: false, reason: 'unavailable' };
  const code = await errorCode(res);
  if (res.status === 403 && code === 'account_suspended') return { ok: false, reason: 'suspended' };
  if (res.status === 409) return { ok: false, reason: 'username_taken' };
  if (res.status === 400 && code === 'invalid_username') return { ok: false, reason: 'username_invalid' };
  if (res.status === 400) return { ok: false, reason: 'invalid' };
  return { ok: false, reason: 'failed' };
}

export async function errorCode(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: unknown };
    return typeof body.error === 'string' ? body.error : '';
  } catch {
    return '';
  }
}
