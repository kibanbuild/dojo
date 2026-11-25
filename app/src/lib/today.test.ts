import { describe, expect, it } from 'vitest';
import { ago, capitalize, parseBrief, sameDay, shortCommit } from './today';

describe('parseBrief', () => {
  it('reads sender, team and the first paragraph', () => {
    const b = 'Ticket FND-001, from Marta (team lead):\n\nHi! A customer says\ntheir payment failed.\n\nWrite the path.\n';
    expect(parseBrief(b)).toEqual({ ref: 'FND-001', sender: 'Marta', team: 'team lead', text: 'Hi! A customer says their payment failed.' });
  });
  it('allows a sender with no team', () => {
    expect(parseBrief('Ticket A-1, from Javi:\n\nHello.')?.team).toBeNull();
  });
  it('gives null for any other shape', () => {
    expect(parseBrief('Just text.')).toBeNull();
    expect(parseBrief('')).toBeNull();
  });
});

describe('small helpers', () => {
  it('words relative time', () => {
    const now = Date.parse('2026-11-25T12:00:00Z');
    expect(ago('2026-11-25T11:57:00Z', now)).toBe('3 min ago');
    expect(ago(null, now)).toBe('');
  });
  it('detects the same local day', () => {
    const now = new Date(2026, 10, 25, 20);
    expect(sameDay(new Date(2026, 10, 25, 1).toISOString(), now)).toBe(true);
    expect(sameDay(new Date(2026, 10, 24, 23).toISOString(), now)).toBe(false);
  });
  it('shortens and capitalizes', () => {
    expect(shortCommit('3f9a1c2deadbeef')).toBe('3f9a1c2');
    expect(capitalize('irene')).toBe('Irene');
  });
});
