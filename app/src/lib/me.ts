import { useQuery } from '@tanstack/react-query';
import { apiFetch } from './api';

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

/** The signed-in learner, or null when the API answers 401 (signed out). Anything else is an error. */
export async function fetchMe(): Promise<Me | null> {
  const res = await apiFetch('/v1/me');
  if (res.status === 401) return null;
  if (!res.ok) throw new Error(`GET /v1/me answered ${res.status}`);
  return (await res.json()) as Me;
}

export function useMe() {
  return useQuery({ queryKey: ME_KEY, queryFn: fetchMe, retry: false, staleTime: 30_000 });
}
