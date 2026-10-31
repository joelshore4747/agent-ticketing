import { describe, expect, it } from 'vitest';
import { keepProject, parseSearch, sinceInstant } from '../lib/filters';

describe('view search', () => {
  it('keeps only values the API would accept', () => {
    expect(parseSearch({ project: 'OPS', type: 'bug', priority: 'urgent', since: 'today' })).toEqual({
      project: 'OPS',
      type: 'bug',
      priority: 'urgent',
      since: 'today',
      area: undefined,
    });
    expect(parseSearch({ type: 'nonsense', priority: '', since: 'yesterday' })).toEqual({
      project: undefined,
      type: undefined,
      priority: undefined,
      since: undefined,
      area: undefined,
    });
  });

  // A project key is what every ticket in it is named after, so a junk one must never reach a query.
  it('rejects a project key the API could not have issued', () => {
    for (const key of ['ops', 'O', 'TOOLONGAKEY', '1ST', 'OP-S', '']) {
      expect(parseSearch({ project: key }).project, key).toBeUndefined();
    }
  });

  it('carries the project across a link and nothing else', () => {
    expect(keepProject({ project: 'OPS', type: 'bug', area: 'x' })).toEqual({ project: 'OPS' });
  });

  it('resolves a window to local midnight, so an overnight board means the new today', () => {
    expect(sinceInstant(undefined)).toBeUndefined();

    const today = new Date(sinceInstant('today')!);
    const midnight = new Date();
    midnight.setHours(0, 0, 0, 0);
    expect(today.getTime()).toBe(midnight.getTime());

    const week = new Date(sinceInstant('week')!);
    expect(Math.round((today.getTime() - week.getTime()) / 86_400_000)).toBe(7);
  });
});
