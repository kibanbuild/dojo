export type Consent = 'granted' | 'denied' | 'unknown';

/**
 * First-party cookie holding a signed-out visitor's analytics choice.
 * Value: `granted` or `denied` (anything else reads as unknown). Host-only (no Domain), Path=/,
 * Max-Age one year, SameSite=Lax, Secure on https. Never set by or sent to a third party.
 */
export const CONSENT_COOKIE = 'dojo_analytics_consent';
const MAX_AGE_SECONDS = 60 * 60 * 24 * 365;

export function readConsentCookie(doc: Pick<Document, 'cookie'> = document): Consent {
  for (const part of doc.cookie.split(';')) {
    const [name, value] = part.trim().split('=');
    if (name === CONSENT_COOKIE) return value === 'granted' || value === 'denied' ? value : 'unknown';
  }
  return 'unknown';
}

export function writeConsentCookie(choice: 'granted' | 'denied', doc: Pick<Document, 'cookie'> = document, secure = location.protocol === 'https:'): void {
  doc.cookie = `${CONSENT_COOKIE}=${choice}; Path=/; Max-Age=${MAX_AGE_SECONDS}; SameSite=Lax${secure ? '; Secure' : ''}`;
}
