import { apiFetch } from './api';
import { errorCode } from './settings';

export interface Device {
  id: string;
  device_name: string;
  os: string;
  cli_version: string;
  created_at: string;
  last_used_at: string | null;
  /** True only for the CLI token making the call, so it is always false from the browser. */
  current: boolean;
}

export const DEVICES_KEY = ['devices'] as const;

export type DevicesFailure = 'signed_out' | 'unavailable' | 'suspended' | 'failed';

export const DEVICES_MESSAGES: Record<DevicesFailure, string> = {
  signed_out: 'Your session ended. Sign in again.',
  unavailable: 'Machines are not available yet. Try again later.',
  suspended: 'This account is suspended, so its machines cannot be listed.',
  failed: 'Could not load your machines. Please try again.',
};

export class DevicesError extends Error {
  constructor(readonly reason: DevicesFailure) {
    super(`devices: ${reason}`);
  }
}

/** GET /v1/me/devices, most recently used first. Failures throw a DevicesError the page can word. */
export async function fetchDevices(): Promise<Device[]> {
  let res: Response;
  try {
    res = await apiFetch('/v1/me/devices');
  } catch {
    throw new DevicesError('failed');
  }
  if (res.ok) {
    try {
      const body = (await res.json()) as { devices?: Device[] };
      return Array.isArray(body.devices) ? body.devices : [];
    } catch {
      throw new DevicesError('failed');
    }
  }
  if (res.status === 401) throw new DevicesError('signed_out');
  if (res.status === 404 || res.status === 405) throw new DevicesError('unavailable');
  if (res.status === 403 && (await errorCode(res)) === 'account_suspended') throw new DevicesError('suspended');
  throw new DevicesError('failed');
}

export type RevokeFailure = 'signed_out' | 'csrf' | 'failed';

export const REVOKE_MESSAGES: Record<RevokeFailure, string> = {
  signed_out: 'Your session ended. Sign in again.',
  csrf: 'Your session looks out of date. Reload the page and try again.',
  failed: 'Could not revoke this machine. Please try again.',
};

export type RevokeResult = { ok: true } | { ok: false; reason: RevokeFailure };

/** DELETE /v1/me/devices/{id}. 204 and 404 (already revoked) both mean the machine is gone. */
export async function revokeDevice(id: string): Promise<RevokeResult> {
  let res: Response;
  try {
    res = await apiFetch(`/v1/me/devices/${encodeURIComponent(id)}`, { method: 'DELETE' });
  } catch {
    return { ok: false, reason: 'failed' };
  }
  if (res.status === 204 || res.status === 404) return { ok: true };
  if (res.status === 401) return { ok: false, reason: 'signed_out' };
  if (res.status === 403 && (await errorCode(res)) === 'csrf_rejected') return { ok: false, reason: 'csrf' };
  return { ok: false, reason: 'failed' };
}

const OS_LABELS: Record<string, string> = { darwin: 'macOS', linux: 'Linux', windows: 'Windows' };

/** The CLI sends GOOS/GOARCH: darwin/arm64 -> "macOS (arm64)"; an unknown OS is shown as sent. */
export function osLabel(os: string): string {
  const [name, arch] = os.split('/', 2);
  const label = OS_LABELS[(name ?? '').toLowerCase()];
  if (!label) return os;
  return arch ? `${label} (${arch})` : label;
}

const UNITS: [limit: number, size: number, unit: string][] = [
  [3_600_000, 60_000, 'minute'],
  [86_400_000, 3_600_000, 'hour'],
  [14 * 86_400_000, 86_400_000, 'day'],
  [60 * 86_400_000, 7 * 86_400_000, 'week'],
  [Infinity, 30 * 86_400_000, 'month'],
];

/** "just now", "5 minutes ago", "3 days ago", "2 weeks ago"; '' when the time is missing or invalid. */
export function relativeTime(iso: string | null, now: number = Date.now()): string {
  const then = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(then)) return '';
  const diff = Math.max(0, now - then);
  if (diff < 60_000) return 'just now';
  for (const [limit, size, unit] of UNITS) {
    if (diff < limit) {
      const n = Math.floor(diff / size);
      return `${n} ${unit}${n === 1 ? '' : 's'} ago`;
    }
  }
  return '';
}
