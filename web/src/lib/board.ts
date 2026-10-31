import type { Source, Status, Ticket } from '../api/types';

export interface Column {
  status: Status;
  name: string;
  /** What fills this column when it is empty, said as the thing that would fill it. */
  empty: string;
}

const INBOX: Column = { status: 'triage', name: 'Inbox', empty: 'Findings from audits and reviews land here first.' };
const TODO: Column = { status: 'open', name: 'To do', empty: 'Nothing queued.' };
const DOING: Column = { status: 'in_progress', name: 'In progress', empty: 'Nothing being worked on right now.' };
const DONE: Column = { status: 'done', name: 'Done', empty: 'Nothing finished yet.' };

/** Human tickets are created open, so they never have an inbox to sit in (docs/api.md rule 3). */
export function columnsFor(source: Source | undefined): Column[] {
  return source === 'human' ? [TODO, DOING, DONE] : [INBOX, TODO, DOING, DONE];
}

/** The board's half of docs/api.md rule 4, read from the ticket itself: the API serves each ticket's
 *  legal moves, so a drop is never accepted and then refused with a 409, and there is no second copy
 *  of the table to fall out of step (TP-27). `closed` is reachable from the ticket page, not from
 *  here: closing wants a reason typed, not a gesture. */
export function canMove(ticket: Pick<Ticket, 'nextStatuses'>, to: Status): boolean {
  return to !== 'closed' && (ticket.nextStatuses ?? []).includes(to);
}

/** Entering done needs a resolution (docs/api.md rule 4), so that move asks before it moves. */
export function needsResolution(target: Status): boolean {
  return target === 'done' || target === 'closed';
}
