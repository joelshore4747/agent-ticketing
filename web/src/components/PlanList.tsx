import { For, Show, createMemo, createSignal } from 'solid-js';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { useProjectKey } from '../api/hooks';
import { ACCEPT_BATCH_MUTATION } from '../api/queries';
import { BoardCard } from './BoardCard';
import type { Batch, Ticket } from '../api/types';

/** A plan batch's tickets, numbered in the order they must be worked. Each row says whether it is
 *  next, what it is waiting behind, or that it is the eval ticket that closes the batch — so the
 *  order is something you can see, not something you have to know. */
export function PlanList(props: { batch: Batch; tickets: Ticket[] }) {
  const project = useProjectKey();
  const queryClient = useQueryClient();
  const [error, setError] = createSignal('');

  const behind = createMemo(() => {
    const out = new Map<string, string>();
    for (const entry of props.batch.progress?.blockedBy ?? []) out.set(entry.key, entry.behind);
    return out;
  });
  const inTriage = () => props.tickets.filter((t) => t.status === 'triage').length;

  const accept = useMutation(() => ({
    mutationFn: () => gql(ACCEPT_BATCH_MUTATION, { project: project(), id: props.batch.id }),
    onSuccess: () => {
      setError('');
      void queryClient.invalidateQueries({ queryKey: ['batch'] });
      void queryClient.invalidateQueries({ queryKey: ['tickets'] });
      void queryClient.invalidateQueries({ queryKey: ['roadmaps'] });
    },
    onError: (err: Error) => setError(err.message),
  }));

  return (
    <>
      <Show when={props.batch.progress?.lockedBy}>
        {(reason) => (
          <p class="notice plan__locked">
            <strong>Locked.</strong> Nothing here can start: {reason()}.
          </p>
        )}
      </Show>

      <Show when={inTriage() > 0 && !props.batch.finishedAt}>
        <div class="plan__accept">
          <p class="meta">
            {inTriage() === 1 ? '1 ticket is' : `${inTriage()} tickets are`} waiting for you to accept them. A
            ticket still in triage holds up every ticket after it.
          </p>
          <button type="button" class="btn btn--primary" disabled={accept.isPending} onClick={() => accept.mutate()}>
            Accept batch
          </button>
        </div>
      </Show>
      <Show when={error()}>{(message) => <p class="error-text">{message()}</p>}</Show>

      <ol class="plan">
        <For each={props.tickets}>
          {(ticket) => (
            <li class="plan__row" data-status={ticket.status}>
              <span class="plan__position" aria-label={`Position ${ticket.batchPosition ?? ''}`}>
                {ticket.batchPosition ?? '·'}
              </span>
              <div class="plan__card">
                <ul class="results__list">
                  <BoardCard ticket={ticket} showSource showArea />
                </ul>
                <div class="plan__state">
                  <Show when={ticket.type === 'eval'}>
                    <span class="chip chip--eval">Eval gate</span>
                  </Show>
                  <Show when={props.batch.progress?.next === ticket.key}>
                    <span class="chip chip--next">Next</span>
                  </Show>
                  <Show when={behind().get(ticket.key)}>
                    {(key) => <span class="meta">Waiting on {key()}</span>}
                  </Show>
                </div>
              </div>
            </li>
          )}
        </For>
      </ol>
    </>
  );
}
