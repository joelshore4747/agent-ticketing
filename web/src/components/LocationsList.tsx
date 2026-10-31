import { For } from 'solid-js';
import type { TicketLocation } from '../api/types';

export function LocationsList(props: { locations: TicketLocation[] }) {
  return (
    <ul class="locations-list">
      <For each={props.locations} fallback={<p class="empty-state">No file locations attached.</p>}>
        {(loc) => (
          <li class="location-row code">
            {loc.filePath}
            {loc.lineStart ? `:${loc.lineStart}` : ''}
            {loc.lineEnd && loc.lineEnd !== loc.lineStart ? `-${loc.lineEnd}` : ''}
          </li>
        )}
      </For>
    </ul>
  );
}
