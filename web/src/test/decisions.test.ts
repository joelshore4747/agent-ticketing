import { describe, expect, it } from 'vitest';
import { isUncited, supersededIds } from '../lib/decisions';
import type { Decision } from '../api/types';

const d = (over: Partial<Decision>): Decision => ({
  id: 'x', kind: 'accepted', madeBy: 'ai', evidenceRefs: [], rationale: '', revisitTrigger: null,
  supersedes: null, citesRevisionId: null, actor: 'claude-code', createdAt: '2026-09-24T12:00:00Z', ...over,
});

describe('uncited decisions', () => {
  it('are Claude’s, in force, not deferrals, and cite nothing', () => {
    const none = new Set<string>();
    expect(isUncited(d({}), none)).toBe(true);
    expect(isUncited(d({ madeBy: 'human' }), none)).toBe(false);
    expect(isUncited(d({ kind: 'deferred', revisitTrigger: 'later' }), none)).toBe(false);
    expect(isUncited(d({ evidenceRefs: ['TP-1'] }), none)).toBe(false);
    expect(isUncited(d({ id: 'old' }), supersededIds([d({ id: 'new', supersedes: 'old' })]))).toBe(false);
  });
});
