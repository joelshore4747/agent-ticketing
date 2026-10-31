import { For } from 'solid-js';
import { relativeTime } from '../lib/time';
import type { TicketEvent } from '../api/types';

function describe(event: TicketEvent): string {
  const value = (v: string | null) => (v && v.length > 80 ? `${v.slice(0, 80)}…` : v);
  switch (event.field) {
    case 'created':
      return `${event.actor} opened this ticket`;
    case 'seen_again':
      return `${event.actor} reported this again, ${event.newValue ?? '?'} times now`;
    case 'claimed':
      return `${event.actor} took this ticket`;
    case 'comment':
      return `${event.actor} commented`;
    default: {
      const field = event.field.replace(/_/g, ' ');
      const from = event.oldValue ? ` from ${value(event.oldValue)}` : '';
      const to = event.newValue ? ` to ${value(event.newValue)}` : '';
      return `${event.actor} changed ${field}${from}${to}`;
    }
  }
}

export function ActivityLog(props: { events: TicketEvent[] }) {
  return (
    <ul>
      <For each={props.events} fallback={<p class="empty-state">Nothing has happened yet.</p>}>
        {(event) => (
          <li class="activity-row">
            <span>{describe(event)}</span>
            <time datetime={event.createdAt}>{relativeTime(event.createdAt)}</time>
          </li>
        )}
      </For>
    </ul>
  );
}
