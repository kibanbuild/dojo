import { useQuery } from '@tanstack/react-query';
import { apiFetch } from './api';
import { errorCode } from './settings';

export interface Me {
  id: string;
  username: string;
  github_login: string;
  email: string | null;
  language: string;
  usage_stats: boolean;
  analytics_consent: boolean | null;
}

export const ME_KEY = ['me'] as const;

/** Thrown by fetchMe when the session is valid but the account is suspended. */
export class SuspendedError extends Error {
  constructor() {
    super('account_suspended');
  }
}

/** The signed-in learner, or null when the API answers 401 (signed out). Anything else is an error. */
export async function fetchMe(): Promise<Me | null> {
  const res = await apiFetch('/v1/me');
  if (res.status === 401) return null;
  if (res.status === 403 && (await errorCode(res)) === 'account_suspended') throw new SuspendedError();
  if (!res.ok) throw new Error(`GET /v1/me answered ${res.status}`);
  return (await res.json()) as Me;
}

export function useMe() {
  return useQuery({ queryKey: ME_KEY, queryFn: fetchMe, retry: false, staleTime: 30_000 });
}
