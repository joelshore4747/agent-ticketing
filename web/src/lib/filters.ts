import { PRIORITIES, TICKET_TYPES, type Priority, type TicketType } from '../api/types';

/** How far back a board looks. Kept as a name rather than a timestamp so the URL stays readable
 *  and, more importantly, so "today" is resolved when the query runs — a board left open overnight
 *  means the new today, not yesterday's. */
export type Since = 'today' | 'week' | 'month';

export const SINCE_LABELS: Record<Since, string> = {
  today: 'Today',
  week: 'Last 7 days',
  month: 'Last 30 days',
};

const SINCE_DAYS: Record<Since, number> = { today: 0, week: 7, month: 30 };

/** What the board is narrowed to, carried in the URL so a filtered board can be linked and
 *  survives a reload. Status is not here: on a board the columns are the statuses. */
export interface Filters {
  type?: TicketType;
  area?: string;
  priority?: Priority;
  since?: Since;
  tag?: string;
}

/** The project a view is scoped to, alongside its filters. Every route carries it, so following a
 *  link never silently drops you back into the default project. */
export interface ViewSearch extends Filters {
  project?: string;
}

export function parseSearch(raw: Record<string, unknown>): ViewSearch {
  return {
    project: asKey(raw.project),
    type: asOneOf(raw.type, TICKET_TYPES),
    priority: asOneOf(raw.priority, PRIORITIES),
    since: asOneOf(raw.since, ['today', 'week', 'month'] as const),
    area: typeof raw.area === 'string' && raw.area.length > 0 ? raw.area : undefined,
    tag: typeof raw.tag === 'string' && /^[a-z0-9][a-z0-9-]{0,31}$/.test(raw.tag) ? raw.tag : undefined,
  };
}

/** Carries the project across a link that changes nothing else. */
export function keepProject(prev: Record<string, unknown>): { project?: string } {
  return { project: asKey(prev.project) };
}

export function hasFilters(filters: Filters): boolean {
  return !!(filters.type || filters.area || filters.priority || filters.since || filters.tag);
}

/** `since` as the instant the API compares against: local midnight for today, otherwise N days back. */
export function sinceInstant(since: Since | undefined): string | undefined {
  if (!since) return undefined;
  const from = new Date();
  from.setHours(0, 0, 0, 0);
  from.setDate(from.getDate() - SINCE_DAYS[since]);
  return from.toISOString();
}

function asKey(value: unknown): string | undefined {
  return typeof value === 'string' && /^[A-Z][A-Z0-9]{1,9}$/.test(value) ? value : undefined;
}

function asOneOf<T extends string>(value: unknown, allowed: readonly T[]): T | undefined {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? (value as T) : undefined;
}
