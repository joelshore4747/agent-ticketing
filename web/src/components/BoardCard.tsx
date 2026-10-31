import { For, Show } from 'solid-js';
import { Link } from '@tanstack/solid-router';
import { Icon } from './Icon';
import { areaHue } from '../lib/areaHue';
import { keepProject } from '../lib/filters';
import { label } from '../lib/labels';
import type { Ticket } from '../api/types';

/** A ticket on the board. Drag it to any column it may legally move to.
 *
 *  The handlers are optional so the same card can be listed somewhere with no columns to drop into
 *  — a batch's tickets, say. Without them it is a read-only card: not draggable, and no inbox
 *  buttons, because a button that cannot do anything is worse than no button.
 *
 *  It carries no clock. A card's age answered a question nobody was asking — the column already
 *  says what is happening to it, and the ticket page has every timestamp for when the date matters.
 *  Removed 16 September 2026 for being noise on every card in exchange for nothing. */
export function BoardCard(props: {
  ticket: Ticket;
  showSource?: boolean;
  showArea?: boolean;
  dragging?: boolean;
  onDragStart?: (ticket: Ticket) => void;
  onDragEnd?: () => void;
  onAccept?: (ticket: Ticket) => void;
  onDismiss?: (ticket: Ticket) => void;
  /** Inbox cards can be picked for a bulk accept or dismiss (TP-28). */
  selected?: boolean;
  onSelect?: (ticket: Ticket, selected: boolean) => void;
}) {
  const isDone = () => props.ticket.status === 'done';
  const isInbox = () => props.ticket.status === 'triage';

  const act = (event: Event, fn: () => void) => {
    event.preventDefault();
    event.stopPropagation();
    fn();
  };

  return (
    <li>
      <Link
        to="/t/$key"
        params={{ key: props.ticket.key }}
        search={keepProject}
        /* A plain string, not the object form Solid 2 allows: Link is a component and forwards
           `class` straight to the DOM, so an object or array arrives as "[object Object]". Solid's
           class composition only applies to native elements. */
        class={`card${props.ticket.priority === 'urgent' && !isDone() ? ' card--urgent' : ''}`}
        draggable={props.onDragStart ? 'true' : undefined}
        data-dragging={props.dragging ? 'true' : undefined}
        onDragStart={(event) => {
          // Text is set as well as the effect so a drag that leaves the window is not a mystery.
          event.dataTransfer?.setData('text/plain', props.ticket.key);
          if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move';
          props.onDragStart?.(props.ticket);
        }}
        onDragEnd={() => props.onDragEnd?.()}
      >
        <div class="card__top">
          <span class="key">{props.ticket.key}</span>
          <Show when={props.showArea && props.ticket.area}>
            {(area) => (
              <span class="chip chip--area" style={{ '--hue-area': areaHue(area().name) }}>
                {area().name}
              </span>
            )}
          </Show>
          <Show when={props.showSource}>
            <span class="chip chip--source" data-source={props.ticket.source}>
              {props.ticket.source === 'ai' ? 'AI' : 'You'}
            </span>
          </Show>
        </div>

        {/* A plain-English line, when Claude wrote one, reads better on a board than a technical
            title; the title stays one hover away. */}
        <div class="card__title" title={props.ticket.summaryForHuman ? props.ticket.title : undefined}>
          {props.ticket.summaryForHuman || props.ticket.title}
        </div>

        {/* Tags are grey on purpose: they sort work, they do not rank it, so they get no colour. */}
        <Show when={(props.ticket.tags?.length ?? 0) + (props.ticket.goals?.length ?? 0) > 0}>
          <div class="card__tags">
            {/* Grey like tags: goals say what the work is for, they do not rank it. */}
            <For each={props.ticket.goals}>{(g) => <span class="chip chip--tag" title={`Key goal ${g}`}>Goal {g}</span>}</For>
            <For each={props.ticket.tags}>{(tag) => <span class="chip chip--tag">{tag}</span>}</For>
          </div>
        </Show>

        <Show when={isDone() && props.ticket.resolution}>
          <p class="card__resolution">{props.ticket.resolution}</p>
        </Show>

        <div class="card__foot">
          <Show
            when={!isDone()}
            fallback={
              <span class="chip chip--fixed">
                <Icon name="check" size={11} />
                Fixed
              </span>
            }
          >
            <span class={`chip chip--priority chip--${props.ticket.priority}`}>
              {label(props.ticket.priority)}
            </span>
          </Show>
          <span class="chip chip--type">{label(props.ticket.type)}</span>

          <Show when={isInbox() && props.onAccept && props.onDismiss}>
            <span class="card__actions">
              <Show when={props.onSelect}>
                <input
                  type="checkbox"
                  class="card__select"
                  aria-label={`Select ${props.ticket.key}`}
                  checked={!!props.selected}
                  onClick={(event) => {
                    // The card is a link; picking it must not open it.
                    event.stopPropagation();
                    props.onSelect?.(props.ticket, event.currentTarget.checked);
                  }}
                />
              </Show>
              <button
                type="button"
                class="btn btn--small btn--quiet"
                onClick={(event) => act(event, () => props.onDismiss?.(props.ticket))}
              >
                Dismiss
              </button>
              <button
                type="button"
                class="btn btn--small btn--primary"
                onClick={(event) => act(event, () => props.onAccept?.(props.ticket))}
              >
                Accept
              </button>
            </span>
          </Show>
        </div>
      </Link>
    </li>
  );
}
