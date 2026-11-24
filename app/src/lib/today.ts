/** Pure helpers for the Today page: no React, no fetching. */

export interface BriefMessage {
  /** Ticket reference as the brief writes it, e.g. `FND-001`. */
  ref: string;
  sender: string;
  /** The parenthesized role after the sender, e.g. `team lead`. */
  team: string | null;
  /** First paragraph of the message, line breaks folded. */
  text: string;
}

/** Reads the client message off a brief that opens "Ticket REF, from NAME (TEAM):". Anything else gives null. */
export function parseBrief(brief: string): BriefMessage | null {
  const m = /^Ticket (\S+?), from ([^(:\n]+?)(?: \(([^)\n]*)\))?:\r?\n\r?\n([\s\S]+)$/.exec(brief.trim());
  if (!m) return null;
  const text = (m[4] ?? '').split(/\r?\n\s*\r?\n/)[0]?.replace(/\s*\r?\n\s*/g, ' ').trim() ?? '';
  if (!text) return null;
  return { ref: m[1] ?? '', sender: (m[2] ?? '').trim(), team: m[3]?.trim() || null, text };
}

export function ago(iso: string | null, now = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return '';
  const min = Math.max(0, Math.round((now - t) / 60_000));
  if (min < 1) return 'just now';
  if (min < 60) return `${min} min ago`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}

const dayKey = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;

export const sameDay = (iso: string | null, now = new Date()): boolean => {
  const t = iso ? Date.parse(iso) : NaN;
  return !Number.isNaN(t) && dayKey(new Date(t)) === dayKey(now);
};

export const shortCommit = (sha: string) => sha.slice(0, 7);

export const initial = (name: string) => (name.trim()[0] ?? '?').toUpperCase();

export const capitalize = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);
