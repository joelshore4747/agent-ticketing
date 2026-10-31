import type { Priority, Status, TicketType } from '../api/types';

const TITLE_CASE: Record<string, string> = {
  in_progress: 'In progress',
};

/** "in_progress" -> "In progress", "bug" -> "Bug". */
export function label(value: TicketType | Priority | Status | string): string {
  if (value in TITLE_CASE) return TITLE_CASE[value];
  return value.charAt(0).toUpperCase() + value.slice(1);
}
