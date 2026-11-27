import { apiFetch } from './api';
import { errorCode } from './settings';

export const REPORT_MAX = 2000;

export type ReportKind = 'broken_ticket' | 'setup_help';
export type ReportFailure = 'invalid' | 'not_found' | 'rate_limited' | 'suspended' | 'signed_out' | 'failed';
export type ReportResult = { ok: true } | { ok: false; reason: ReportFailure };

export const REPORT_MESSAGES: Record<Exclude<ReportFailure, 'signed_out'>, string> = {
  invalid: 'We could not accept that report. Check the message and try again.',
  not_found: 'That ticket is not available to report.',
  rate_limited: 'You sent several reports already. Try again in a while.',
  suspended: 'This account is suspended, so it cannot send reports.',
  failed: 'Could not send your report. Please try again.',
};

/** The inline problem with a message, or '' when it can be sent. Counts what the API counts: the trimmed text. */
export function reportProblem(message: string): string {
  const n = [...message.trim()].length;
  if (n === 0) return 'Write a few words about what went wrong.';
  if (n > REPORT_MAX) return `Use at most ${REPORT_MAX.toLocaleString('en-US')} characters.`;
  return '';
}

/** POST /v1/reports. `includeOutput` asks the API to attach the learner's own last check output; the client never sends it. */
export async function postReport(body: { kind: ReportKind; itemId?: string; message: string; includeOutput: boolean }): Promise<ReportResult> {
  let res: Response;
  try {
    res = await apiFetch('/v1/reports', {
      method: 'POST',
      body: JSON.stringify({ kind: body.kind, item_id: body.itemId, message: body.message.trim(), include_output: body.includeOutput }),
    });
  } catch {
    return { ok: false, reason: 'failed' };
  }
  if (res.status === 201) return { ok: true };
  if (res.status === 401) return { ok: false, reason: 'signed_out' };
  if (res.status === 403 && (await errorCode(res)) === 'account_suspended') return { ok: false, reason: 'suspended' };
  if (res.status === 400) return { ok: false, reason: 'invalid' };
  if (res.status === 404) return { ok: false, reason: 'not_found' };
  if (res.status === 429) return { ok: false, reason: 'rate_limited' };
  return { ok: false, reason: 'failed' };
}
