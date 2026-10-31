import { Show, createSignal } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { SkeletonLines } from './SkeletonLines';
import type { Column } from '../lib/board';

/** One stop on a ticket's journey. The column knows only whether the ticket currently being
 *  dragged may land in it; the board works that out from the API's transition rules. */
export function BoardColumn(props: {
  column: Column;
  total: number | undefined;
  /** How many cards the column actually holds; fewer than total means the list was cut short. */
  shown: number;
  isLoading: boolean;
  isEmpty: boolean;
  dragActive: boolean;
  droppable: boolean;
  onDrop: () => void;
  /** Controls that act on the whole column, shown under its heading. */
  toolbar?: JSX.Element;
  children: JSX.Element;
}) {
  const [over, setOver] = createSignal(false);

  return (
    <section
      class={`column column--${props.column.status}`}
      data-droppable={props.dragActive ? String(props.droppable) : undefined}
      data-over={over() ? 'true' : undefined}
      aria-label={props.column.name}
      onDragOver={(event) => {
        if (!props.droppable) return;
        event.preventDefault();
        if (event.dataTransfer) event.dataTransfer.dropEffect = 'move';
        setOver(true);
      }}
      onDragLeave={(event) => {
        // Leaving for a child of this column is not leaving the column.
        if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
        setOver(false);
      }}
      onDrop={(event) => {
        setOver(false);
        if (!props.droppable) return;
        event.preventDefault();
        props.onDrop();
      }}
    >
      <div class="column__head">
        <span class="column__dot" />
        <span class="column__name">{props.column.name}</span>
        <Show when={props.total !== undefined}>
          <span class="column__count">{props.total}</span>
        </Show>
      </div>
      {props.toolbar}
      <ul class="column__list">
        <Show when={!props.isLoading} fallback={
            <li>
              <SkeletonLines rows={3} />
            </li>
          }>
          <Show when={!props.isEmpty} fallback={<li class="column__empty">{props.column.empty}</li>}>
            {props.children}
          </Show>
        </Show>
      </ul>
      {/* The count says how many exist; without this, a column cut at its page size looks complete (TP-18). */}
      <Show when={!props.isLoading && (props.total ?? 0) > props.shown}>
        <p class="column__more">
          Showing {props.shown} of {props.total}. Search or filter to find the rest.
        </p>
      </Show>
    </section>
  );
}
