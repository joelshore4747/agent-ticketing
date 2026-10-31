import { describe, expect, it } from 'vitest';
import { parseCriteria, pendingProposals } from '../lib/proposals';
import type { TicketEvent } from '../api/types';

const ev = (id: string, field: string, newValue: string, createdAt: string): TicketEvent => ({
  id, actor: 'claude-code', field, oldValue: null, newValue, createdAt,
});

describe('pending proposals', () => {
  it('keeps a proposal until its field changes after it', () => {
    const events = [
      ev('1', 'intent_proposed', 'Faster claims', '2026-09-24T10:00:00Z'),
      ev('2', 'constraints_proposed', 'No new deps', '2026-09-24T10:01:00Z'),
      ev('3', 'constraints', 'No new deps', '2026-09-24T10:02:00Z'),
    ];
    const pending = pendingProposals(events);
    expect(pending.map((p) => p.field)).toEqual(['intent']);
    expect(pending[0].proposed).toBe('Faster claims');
  });
});

describe('parseCriteria', () => {
  it('reads the API form and keeps untagged lines as behavioural', () => {
    expect(parseCriteria('1. [gate] just test\n2. [measurement] p95 under 200ms\nit feels right')).toEqual([
      { tier: 'gate', text: 'just test' },
      { tier: 'measurement', text: 'p95 under 200ms' },
      { tier: 'behavioural', text: 'it feels right' },
    ]);
  });
});
