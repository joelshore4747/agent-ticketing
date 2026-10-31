import { For, Show, createMemo, createSignal } from 'solid-js';
import { useNavigate, useSearch } from '@tanstack/solid-router';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { useAreasQuery, useTagsQuery, useTicketsQuery } from '../api/hooks';
import { gql, isVersionConflict } from '../api/client';
import { ACCEPT_TICKET_MUTATION, DISMISS_TICKET_MUTATION, TRANSITION_TICKET_MUTATION } from '../api/queries';
import { BoardCard } from '../components/BoardCard';
import { BoardColumn } from '../components/BoardColumn';
import { Filters } from '../components/Filters';
import { Icon } from '../components/Icon';
import { InlineNotice } from '../components/InlineNotice';
import { NewTicketForm } from '../components/NewTicketForm';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { canMove, columnsFor, needsResolution } from '../lib/board';
import { parseSearch, sinceInstant, type Filters as FilterValues, type ViewSearch } from '../lib/filters';
import type { Source, Status, Ticket } from '../api/types';

type NavigateFn = (opts: {
  search: (prev: Record<string, unknown>) => Record<string, unknown>;
  replace?: boolean;
}) => void;

const COPY: Record<'all' | Source, { title: string; lead: string }> = {
  all: { title: 'Board', lead: 'Everything on the go, left to right.' },
  ai: { title: 'AI findings', lead: 'Filed by Claude Code from audits, reviews and prompts. Accept one to make it workable.' },
  human: { title: 'Human tickets', lead: 'The work you decided on yourself.' },
};

/** The board. Columns are statuses, so where a ticket is in its life is a position on screen rather
 *  than a word you have to read. Scoped to everything, to one source, or to one area.
 *
 *  It does not start work. Work is started in the project, by whoever is doing it, and the board
 *  finds out — so the only buttons here are the inbox's, where accepting or dismissing a finding is
 *  a decision nobody else can make for you. Anything else moves by dragging. */
export function Board(props: { source?: Source; areaId?: string }) {
  const rawSearch = useSearch({ strict: false });
  const navigate = useNavigate() as unknown as NavigateFn;
  const queryClient = useQueryClient();

  const [query, setQuery] = createSignal('');
  const [dragged, setDragged] = createSignal<Ticket | null>(null);
  const [pending, setPending] = createSignal<{ ticket: Ticket; target: Status } | null>(null);
  const [resolution, setResolution] = createSignal('');
  const [notice, setNotice] = createSignal('');
  // Inbox tickets picked for a bulk accept or dismiss, by id (TP-28).
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = createSignal(false);
  const [showNewTicket, setShowNewTicket] = createSignal(false);

  const search = createMemo<ViewSearch>(() => parseSearch(rawSearch() as unknown as Record<string, unknown>));
  const isSearching = () => query().trim().length > 0;
  const areasQuery = useAreasQuery();
  const tagsQuery = useTagsQuery();

  /** On an area board the area is the board, not a filter you can change. */
  const areaId = () => props.areaId || search().area;
  const area = () => areasQuery.data?.find((a) => a.id === props.areaId);

  const copy = () =>
    props.areaId
      ? { title: area()?.name ?? 'Area', lead: area()?.description || 'Everything routed to this area.' }
      : COPY[props.source ?? 'all'];

  const ticketFilter = (status: Status) => ({
    source: props.source,
    status: [status],
    type: search().type,
    areaId: areaId(),
    priority: search().priority,
    updatedAfter: sinceInstant(search().since),
    tag: search().tag,
  });

  // The columns for this board are fixed by its source, so their queries can be built once.
  const columns = columnsFor(props.source).map((column) => ({
    column,
    query: useTicketsQuery(() => ({ filter: ticketFilter(column.status), limit: 100 })),
  }));

  const searchResults = useTicketsQuery(() => ({
    filter: { source: props.source, areaId: areaId() },
    search: query(),
    limit: 30,
    enabled: isSearching(),
  }));

  const updateFilters = (patch: Partial<FilterValues>) => {
    navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  };

  // Accepting or dismissing from the inbox also changes what is waiting on the human.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['tickets'] });
    void queryClient.invalidateQueries({ queryKey: ['needsYou'] });
  };

  const onError = (error: unknown) => {
    setNotice(
      isVersionConflict(error)
        ? 'That ticket changed somewhere else, so the board reloaded. Try again.'
        : error instanceof Error
          ? error.message
          : 'Something went wrong',
    );
    refresh();
  };

  const acceptMutation = useMutation(() => ({
    mutationFn: (ticket: Ticket) => gql(ACCEPT_TICKET_MUTATION, { id: ticket.id, version: ticket.version }),
    onSuccess: refresh,
    onError,
  }));

  const dismissMutation = useMutation(() => ({
    mutationFn: (ticket: Ticket) => gql(DISMISS_TICKET_MUTATION, { id: ticket.id, version: ticket.version }),
    onSuccess: refresh,
    onError,
  }));

  const transitionMutation = useMutation(() => ({
    mutationFn: (vars: { ticket: Ticket; target: Status; resolution?: string }) =>
      gql(TRANSITION_TICKET_MUTATION, {
        id: vars.ticket.id,
        version: vars.ticket.version,
        status: vars.target,
        resolution: vars.resolution,
      }),
    onSuccess: refresh,
    onError,
  }));

  /** One way in for every move, whether it arrived as a drag or as an inbox button. */
  const move = (ticket: Ticket, target: Status, text?: string) => {
    setNotice('');
    if (ticket.status === 'triage' && target === 'open') {
      acceptMutation.mutate(ticket);
    } else if (needsResolution(target) && !text) {
      setResolution('');
      setPending({ ticket, target });
    } else {
      transitionMutation.mutate({ ticket, target, resolution: text });
    }
  };

  const confirmPending = () => {
    const p = pending();
    const text = resolution().trim();
    if (!p || !text) return;
    setPending(null);
    move(p.ticket, p.target, text);
  };

  const droppableInto = (target: Status) => {
    const ticket = dragged();
    return !!ticket && canMove(ticket, target);
  };

  const renderCard = (ticket: Ticket) => (
    <BoardCard
      ticket={ticket}
      showSource={!props.source}
      showArea={!props.areaId}
      dragging={dragged()?.id === ticket.id}
      onDragStart={setDragged}
      onDragEnd={() => setDragged(null)}
      onAccept={(t) => move(t, 'open')}
      onDismiss={(t) => dismissMutation.mutate(t)}
      selected={selected().has(ticket.id)}
      onSelect={(t, on) => {
        const next = new Set(selected());
        if (on) next.add(t.id);
        else next.delete(t.id);
        setSelected(next);
      }}
    />
  );

  /** Accept or dismiss every picked finding, one at a time through the human's mutations, so each
   *  files its own decision and one version conflict does not sink the rest. */
  const bulk = async (tickets: Ticket[], action: 'accept' | 'dismiss') => {
    setBulkBusy(true);
    setNotice('');
    let failed = 0;
    for (const t of tickets) {
      try {
        await gql(action === 'accept' ? ACCEPT_TICKET_MUTATION : DISMISS_TICKET_MUTATION, { id: t.id, version: t.version });
      } catch {
        failed++;
      }
    }
    setSelected(new Set<string>());
    setBulkBusy(false);
    if (failed > 0) {
      setNotice(`${failed} of ${tickets.length} could not be ${action === 'accept' ? 'accepted' : 'dismissed'}; they changed elsewhere and are still in the inbox.`);
    }
    refresh();
  };

  const inboxToolbar = (items: Ticket[] | undefined) => {
    const all = items ?? [];
    const picked = () => all.filter((t) => selected().has(t.id));
    return (
      <Show when={all.length > 1}>
        <div class="column__bulk">
          <label class="column__bulk-all">
            <input
              type="checkbox"
              aria-label="Select every finding in the inbox"
              checked={picked().length === all.length}
              onClick={(event) => setSelected(new Set(event.currentTarget.checked ? all.map((t) => t.id) : []))}
            />
            All
          </label>
          <Show when={picked().length > 0}>
            <button type="button" class="btn btn--small btn--quiet" disabled={bulkBusy()} onClick={() => void bulk(picked(), 'dismiss')}>
              Dismiss {picked().length}
            </button>
            <button type="button" class="btn btn--small btn--primary" disabled={bulkBusy()} onClick={() => void bulk(picked(), 'accept')}>
              Accept {picked().length}
            </button>
          </Show>
        </div>
      </Show>
    );
  };

  return (
    <>
      <Topbar title={copy().title} lead={copy().lead} eyebrow={props.areaId ? 'Area' : undefined}>
        <label class="search">
          <Icon name="search" size={17} />
          <input
            id="ticket-search"
            type="search"
            placeholder="Search tickets"
            value={query()}
            onInput={(event) => setQuery(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') setQuery('');
            }}
            aria-label="Search tickets"
          />
        </label>
        <Show when={props.source === 'human'}>
          <button type="button" class="btn btn--primary" onClick={() => setShowNewTicket((open) => !open)}>
            <Show when={!showNewTicket()} fallback="Cancel">
              <Icon name="plus" size={17} />
              New ticket
            </Show>
          </button>
        </Show>
      </Topbar>

      <div class="view view--board">
        <Show when={notice()}>
          <InlineNotice message={notice()} />
        </Show>

        <Show when={showNewTicket()}>
          <NewTicketForm areas={areasQuery.data} onDone={() => setShowNewTicket(false)} />
        </Show>

        <Show
          when={!isSearching()}
          fallback={
            <div class="results">
              <p class="eyebrow">
                {searchResults.data?.total ?? 0} matching {props.source ? copy().title.toLowerCase() : 'tickets'}
              </p>
              <Show when={!searchResults.isPending} fallback={<SkeletonLines rows={4} />}>
                <Show
                  when={(searchResults.data?.items.length ?? 0) > 0}
                  fallback={<p class="empty-state">Nothing matches. Try a word from the title, or a key like TP-8.</p>}
                >
                  <ul class="results__list">
                    <For each={searchResults.data?.items}>{renderCard}</For>
                  </ul>
                </Show>
              </Show>
            </div>
          }
        >
          <Filters
            value={search()}
            areas={areasQuery.data}
            tags={tagsQuery.data}
            hideArea={!!props.areaId}
            onChange={updateFilters}
          />

          <div class="board" data-dragging={dragged() ? 'true' : undefined}>
            <For each={columns}>
              {({ column, query: columnQuery }) => (
                <BoardColumn
                  column={column}
                  total={columnQuery.data?.total}
                  shown={columnQuery.data?.items.length ?? 0}
                  isLoading={columnQuery.isPending}
                  isEmpty={(columnQuery.data?.items.length ?? 0) === 0}
                  dragActive={!!dragged()}
                  droppable={droppableInto(column.status)}
                  toolbar={column.status === 'triage' ? inboxToolbar(columnQuery.data?.items) : undefined}
                  onDrop={() => {
                    const ticket = dragged();
                    setDragged(null);
                    if (ticket) move(ticket, column.status);
                  }}
                >
                  <For each={columnQuery.data?.items}>{renderCard}</For>
                </BoardColumn>
              )}
            </For>
          </div>
        </Show>
      </div>

      <Show when={pending()}>
        {(p) => (
          <div
            class="dialog"
            role="dialog"
            aria-modal="true"
            aria-label={`Mark ${p().ticket.key} done`}
            onClick={(event) => {
              if (event.target === event.currentTarget) setPending(null);
            }}
          >
            <div class="dialog__panel">
              <p class="eyebrow">{p().ticket.key}</p>
              <h2>What was done?</h2>
              <p class="meta">
                One line. It goes on the card and stays on the ticket — the API asks for it before anything may be
                marked done.
              </p>
              <textarea
                ref={(el) => queueMicrotask(() => el.focus())}
                value={resolution()}
                onInput={(event) => setResolution(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') setPending(null);
                  if (event.key === 'Enter' && !event.shiftKey) {
                    event.preventDefault();
                    confirmPending();
                  }
                }}
                aria-label="Resolution"
              />
              <div class="btn-row">
                <button type="button" class="btn btn--primary" disabled={!resolution().trim()} onClick={confirmPending}>
                  Mark done
                </button>
                <button type="button" class="btn" onClick={() => setPending(null)}>
                  Cancel
                </button>
              </div>
            </div>
          </div>
        )}
      </Show>
    </>
  );
}
