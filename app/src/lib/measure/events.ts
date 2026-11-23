// The only events analytics can send. Each declares its allowed props and the kind of value each takes;
// anything not declared here is dropped. Add an event here before calling track() with it.
//   'boolean' | 'number'        a boolean or a finite number
//   'id'                        a short coded string ([A-Za-z0-9_.:-], up to 64), such as a kata id
//   readonly string[]           one of these exact strings
export type PropKind = 'boolean' | 'number' | 'id' | readonly string[];
export type EventSchema = Record<string, PropKind>;

export const EVENTS = {
  kata_started: { kata: 'id', attempt: 'number' },
} as const satisfies Record<string, EventSchema>;

export type EventName = keyof typeof EVENTS;

export function isEventName(name: string): name is EventName {
  return Object.hasOwn(EVENTS, name);
}
