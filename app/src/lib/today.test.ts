import { describe, expect, it } from 'vitest';
import { ago, buildHeatGrid, capitalize, heatSummary, parseBrief, shortCommit } from './today';

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
  it('shortens and capitalizes', () => {
    expect(shortCommit('3f9a1c2deadbeef')).toBe('3f9a1c2');
    expect(capitalize('irene')).toBe('Irene');
  });
});

describe('buildHeatGrid', () => {
  it('draws 20 Monday-first weeks ending with the week of today, future days empty', () => {
    const g = buildHeatGrid('2026-11-25', []); // a Wednesday
    expect(g.weeks).toHaveLength(20);
    expect(g.weeks.every((w) => w.length === 7)).toBe(true);
    expect(g.weeks[19]!.map((c) => c.day)).toEqual(['2026-11-23', '2026-11-24', '2026-11-25', '2026-11-26', '2026-11-27', '2026-11-28', '2026-11-29']);
    expect(g.weeks[19]!.map((c) => c.future)).toEqual([false, false, false, true, true, true, true]);
    expect(g.weeks[0]![0]!.day).toBe('2026-07-13');
  });

  it('puts counts on their days and caps the level at 4', () => {
    const g = buildHeatGrid('2026-11-25', [
      { day: '2026-11-25', count: 1 },
      { day: '2026-11-24', count: 7 },
      { day: '2026-11-23', count: 2 },
    ]);
    expect(g.weeks[19]!.slice(0, 3).map((c) => c.level)).toEqual([2, 4, 1]);
    expect(g.weeks[19]![3]!.level).toBe(0);
    expect(g.total).toBe(10);
    expect(g.busiest).toEqual({ day: '2026-11-24', count: 7 });
  });

  it('steps calendar dates, so DST changes never shift or repeat a day', () => {
    const g = buildHeatGrid('2026-11-01', []); // Europe fell back 2026-10-25, US fell back 2026-11-01
    const days = g.weeks.flat().map((c) => c.day);
    expect(new Set(days).size).toBe(140);
    expect(days).toContain('2026-10-25');
    expect(days).toContain('2026-11-01');
    for (let i = 1; i < days.length; i++) expect(Date.parse(days[i]!) - Date.parse(days[i - 1]!)).toBe(86_400_000);
  });

  it('drops days outside the drawn weeks and counts only what is drawn', () => {
    const g = buildHeatGrid('2026-11-25', [{ day: '2026-07-09', count: 5 }, { day: '2026-11-25', count: 1 }]);
    expect(g.total).toBe(1);
  });

  it('puts Sunday last in its week', () => {
    const g = buildHeatGrid('2026-11-29', []);
    expect(g.weeks[19]!.every((c) => !c.future)).toBe(true);
    expect(g.weeks[19]![6]!.day).toBe('2026-11-29');
  });

  it('words the text alternative', () => {
    expect(heatSummary(buildHeatGrid('2026-11-25', []))).toBe('No tickets passed in the last 20 weeks');
    expect(heatSummary(buildHeatGrid('2026-11-25', [{ day: '2026-11-24', count: 1 }]))).toBe('1 ticket passed in the last 20 weeks, most on Nov 24');
  });
});
