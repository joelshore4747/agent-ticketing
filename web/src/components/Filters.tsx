import { For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { PRIORITIES, TICKET_TYPES, type Area, type Priority, type Tag, type TicketType } from '../api/types';
import { label } from '../lib/labels';
import { SINCE_LABELS, hasFilters, type Filters as FilterValues, type Since } from '../lib/filters';

const SINCE_VALUES: Since[] = ['today', 'week', 'month'];

export function Filters(props: {
  value: FilterValues;
  areas: Area[] | undefined;
  tags?: Tag[] | undefined;
  hideArea?: boolean;
  onChange: (patch: Partial<FilterValues>) => void;
  children?: JSX.Element;
}) {
  return (
    <div class="filters-row">
      {props.children}

      <select
        aria-label="Filter by when it was last touched"
        value={props.value.since ?? ''}
        onChange={(event) => props.onChange({ since: (event.currentTarget.value as Since) || undefined })}
      >
        <option value="">Any time</option>
        <For each={SINCE_VALUES}>{(since) => <option value={since}>{SINCE_LABELS[since]}</option>}</For>
      </select>

      <select
        aria-label="Filter by type"
        value={props.value.type ?? ''}
        onChange={(event) => props.onChange({ type: (event.currentTarget.value as TicketType) || undefined })}
      >
        <option value="">All types</option>
        <For each={TICKET_TYPES}>{(type) => <option value={type}>{label(type)}</option>}</For>
      </select>

      <Show when={!props.hideArea}>
        <select
          aria-label="Filter by area"
          value={props.value.area ?? ''}
          onChange={(event) => props.onChange({ area: event.currentTarget.value || undefined })}
        >
          <option value="">All areas</option>
          <For each={props.areas}>{(area) => <option value={area.id}>{area.name}</option>}</For>
        </select>
      </Show>

      <select
        aria-label="Filter by priority"
        value={props.value.priority ?? ''}
        onChange={(event) => props.onChange({ priority: (event.currentTarget.value as Priority) || undefined })}
      >
        <option value="">All priorities</option>
        <For each={PRIORITIES}>{(priority) => <option value={priority}>{label(priority)}</option>}</For>
      </select>

      <Show when={(props.tags?.length ?? 0) > 0}>
        <select
          aria-label="Filter by tag"
          value={props.value.tag ?? ''}
          onChange={(event) => props.onChange({ tag: event.currentTarget.value || undefined })}
        >
          <option value="">All tags</option>
          <For each={props.tags}>{(tag) => <option value={tag.name}>{tag.name}</option>}</For>
        </select>
      </Show>

      <Show when={hasFilters(props.value)}>
        <button
          type="button"
          class="clear"
          onClick={() =>
            props.onChange({ type: undefined, area: undefined, priority: undefined, since: undefined, tag: undefined })
          }
        >
          Clear filters
        </button>
      </Show>
    </div>
  );
}
