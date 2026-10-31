import { For, Show, createSignal } from 'solid-js';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { CREATE_TICKET_MUTATION } from '../api/queries';
import { useProjectKey } from '../api/hooks';
import { PRIORITIES, TICKET_TYPES } from '../api/types';
import type { Area, CreateTicketInput, Priority, Ticket, TicketType } from '../api/types';
import { label } from '../lib/labels';

export function NewTicketForm(props: { areas: Area[] | undefined; onDone: () => void }) {
  const [title, setTitle] = createSignal('');
  const [type, setType] = createSignal<TicketType>('bug');
  const [priority, setPriority] = createSignal<Priority>('medium');
  const [areaId, setAreaId] = createSignal('');
  const [description, setDescription] = createSignal('');
  const queryClient = useQueryClient();

  const projectKey = useProjectKey();

  const createTicket = useMutation(() => ({
    mutationFn: (input: CreateTicketInput) => gql<{ createTicket: Ticket }>(CREATE_TICKET_MUTATION, { project: projectKey(), input }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['tickets'] });
      props.onDone();
    },
  }));

  const submit = (event: Event) => {
    event.preventDefault();
    if (!title().trim()) return;
    createTicket.mutate({
      title: title().trim(),
      description: description(),
      type: type(),
      priority: priority(),
      source: 'human',
      areaId: areaId() || undefined,
    });
  };

  return (
    <form class="form-panel" onSubmit={submit}>
      <div class="field">
        <label for="new-ticket-title">Title</label>
        <input
          id="new-ticket-title"
          type="text"
          value={title()}
          autofocus
          onInput={(event) => setTitle(event.currentTarget.value)}
        />
      </div>
      <div class="field">
        <label for="new-ticket-type">Type</label>
        <select id="new-ticket-type" value={type()} onChange={(event) => setType(event.currentTarget.value as TicketType)}>
          <For each={TICKET_TYPES}>{(t) => <option value={t}>{label(t)}</option>}</For>
        </select>
      </div>
      <div class="field">
        <label for="new-ticket-priority">Priority</label>
        <select
          id="new-ticket-priority"
          value={priority()}
          onChange={(event) => setPriority(event.currentTarget.value as Priority)}
        >
          <For each={PRIORITIES}>{(p) => <option value={p}>{label(p)}</option>}</For>
        </select>
      </div>
      <div class="field">
        <label for="new-ticket-area">Area</label>
        <select id="new-ticket-area" value={areaId()} onChange={(event) => setAreaId(event.currentTarget.value)}>
          <option value="">No area</option>
          <For each={props.areas}>{(area) => <option value={area.id}>{area.name}</option>}</For>
        </select>
      </div>
      <div class="field">
        <label for="new-ticket-description">Description</label>
        <textarea
          id="new-ticket-description"
          value={description()}
          onInput={(event) => setDescription(event.currentTarget.value)}
        />
      </div>
      <Show when={createTicket.isError}>
        <p class="error-text">{(createTicket.error as Error | null)?.message}</p>
      </Show>
      <div class="btn-row">
        <button type="submit" class="btn btn--primary" disabled={createTicket.isPending}>
          Create ticket
        </button>
        <button type="button" class="btn" onClick={() => props.onDone()}>
          Cancel
        </button>
      </div>
    </form>
  );
}
