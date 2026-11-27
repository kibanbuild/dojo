import { apiFetch } from './api';
import { errorCode } from './settings';

/** Rules for the address the API accepts (section 3.9); the API stays the judge. */
export function emailProblem(value: string): string {
  const v = value.trim();
  if (v === '') return 'Enter your email address.';
  if (v.length < 3 || v.length > 320 || v.split('@').length !== 2) return 'Enter a valid email address.';
  const [local, domain] = v.split('@') as [string, string];
  if (!/^[A-Za-z0-9._%+-]+$/.test(local)) return 'Enter a valid email address.';
  if (!/^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/.test(domain)) return 'Enter a valid email address.';
  return '';
}

export type EmailResult = { ok: true } | { ok: false; reason: 'invalid' | 'already_set' | 'signed_out' | 'rate_limited' | 'failed' };

/** PATCH /v1/me {"email"}: saved only while the stored address is empty. */
export async function saveEmail(email: string): Promise<EmailResult> {
  let res: Response;
  try {
    res = await apiFetch('/v1/me', { method: 'PATCH', body: JSON.stringify({ email: email.trim() }) });
  } catch {
    return { ok: false, reason: 'failed' };
  }
  if (res.ok) return { ok: true };
  if (res.status === 401) return { ok: false, reason: 'signed_out' };
  if (res.status === 429) return { ok: false, reason: 'rate_limited' };
  const code = await errorCode(res);
  if (res.status === 409 && code === 'email_set') return { ok: false, reason: 'already_set' };
  if (res.status === 400 && code === 'invalid_email') return { ok: false, reason: 'invalid' };
  return { ok: false, reason: 'failed' };
}
