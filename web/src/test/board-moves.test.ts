import { describe, expect, it } from 'vitest';
import { canMove, columnsFor } from '../lib/board';
import { STATUSES, type Status } from '../api/types';

/** The board offers exactly the moves the API serves on the ticket (TP-27), minus `closed`, which
 *  needs a typed reason and lives on the ticket page. It keeps no copy of rule 4's table. */
describe('board moves', () => {
  it('offers the moves the ticket carries, and never closing', () => {
    const ticket = { nextStatuses: ['open', 'done', 'closed'] as Status[] };
    for (const to of STATUSES) {
      expect(canMove(ticket, to), `in_progress -> ${to}`).toBe(to === 'open' || to === 'done');
    }
  });

  it('offers nothing when the ticket carries no moves', () => {
    for (const to of STATUSES) {
      expect(canMove({ nextStatuses: [] }, to)).toBe(false);
      expect(canMove({}, to)).toBe(false);
    }
  });

  it('gives human tickets no inbox, because they are created open', () => {
    expect(columnsFor('human').map((c) => c.status)).toEqual(['open', 'in_progress', 'done']);
    expect(columnsFor('ai').map((c) => c.status)).toEqual(['triage', 'open', 'in_progress', 'done']);
    expect(columnsFor(undefined).map((c) => c.status)).toEqual(['triage', 'open', 'in_progress', 'done']);
  });
});
