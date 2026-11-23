import type { EventSchema, PropKind } from './events';
import type { EventProps } from './sinks';

const ID = /^[A-Za-z0-9_.:-]{1,64}$/;

function accepts(kind: PropKind, value: unknown): boolean {
  if (kind === 'boolean') return typeof value === 'boolean';
  if (kind === 'number') return typeof value === 'number' && Number.isFinite(value);
  if (kind === 'id') return typeof value === 'string' && ID.test(value);
  return typeof value === 'string' && kind.includes(value);
}

/** Keeps only the props the event's schema declares, and only when the value has the declared kind. Everything else is dropped. */
export function sanitizeProps(schema: EventSchema, props: Record<string, unknown> | undefined): EventProps {
  const out: EventProps = {};
  if (!props) return out;
  for (const [key, kind] of Object.entries(schema)) {
    const value = props[key];
    if (Object.hasOwn(props, key) && accepts(kind, value)) out[key] = value as string | number | boolean;
  }
  return out;
}
