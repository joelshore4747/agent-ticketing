import type { Decision } from '../api/types';

/** Ids of decisions a later one has replaced. */
export function supersededIds(decisions: Decision[]): Set<string> {
  return new Set(decisions.flatMap((d) => (d.supersedes ? [d.supersedes] : [])));
}

/** Claude's decision resting on nothing. A deferral is exempt: its revisit trigger is what it rests
 *  on. Mirrors the server's uncited listing, which counts only decisions still in force. */
export function isUncited(d: Decision, superseded: Set<string>): boolean {
  return d.madeBy === 'ai' && d.kind !== 'deferred' && d.evidenceRefs.length === 0 && !superseded.has(d.id);
}
