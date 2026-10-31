import type { CriterionInput, CriterionTier, TicketEvent } from '../api/types';

export type ProposableField = 'intent' | 'constraints' | 'non_goals' | 'criteria';

export interface Proposal {
  id: string;
  field: ProposableField;
  proposed: string;
  actor: string;
  createdAt: string;
}

const FIELDS: ProposableField[] = ['intent', 'constraints', 'non_goals', 'criteria'];

/** Claude's proposals to the human-owned section that are still pending: a proposal is settled once
 *  its field has changed after it, whether or not the change took the proposal's words. The raw text
 *  comes from the `<field>_proposed` event, not the markdown comment written for reading. */
export function pendingProposals(events: TicketEvent[]): Proposal[] {
  const lastChange = new Map<string, string>();
  for (const e of events) {
    if (FIELDS.includes(e.field as ProposableField) && e.createdAt > (lastChange.get(e.field) ?? '')) {
      lastChange.set(e.field, e.createdAt);
    }
  }
  return events
    .filter((e) => e.field.endsWith('_proposed'))
    .map((e) => ({
      id: e.id,
      field: e.field.replace(/_proposed$/, '') as ProposableField,
      proposed: e.newValue ?? '',
      actor: e.actor,
      createdAt: e.createdAt,
    }))
    .filter((p) => FIELDS.includes(p.field) && p.createdAt > (lastChange.get(p.field) ?? ''));
}

const TIERS: CriterionTier[] = ['behavioural', 'measurement', 'gate'];

/** Reads criteria in the form the API writes them ("1. [gate] just test", one per line). A line
 *  without a recognised tier becomes a behavioural criterion rather than being dropped. */
export function parseCriteria(text: string): CriterionInput[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const m = /^(?:\d+\.\s*)?(?:\[(\w+)\]\s*)?(.+)$/.exec(line);
      const tier = (m?.[1] ?? '') as CriterionTier;
      return { tier: TIERS.includes(tier) ? tier : 'behavioural', text: (m?.[2] ?? line).trim() };
    });
}
