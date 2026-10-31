import { For, Show, createSignal } from 'solid-js';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { useProjectKey } from '../api/hooks';
import { CHECKPOINT_BATCH_MUTATION } from '../api/queries';
import { label } from '../lib/labels';
import { relativeTime } from '../lib/time';
import type { Batch, CheckpointDecision } from '../api/types';

const DECISIONS: { value: CheckpointDecision; hint: string }[] = [
  { value: 'continue', hint: 'The next batch opens as planned.' },
  { value: 'adjust', hint: 'The next batch opens; the ones after it change first. Say how.' },
  { value: 'stop', hint: 'The roadmap halts here. Nothing after this opens.' },
];

/** The human's decision between one batch and the next. It only appears once the batch has
 *  finished, because the eval ticket is the evidence it is decided on; and it is made once, because
 *  later batches were opened on it. */
export function CheckpointControl(props: { batch: Batch }) {
  const project = useProjectKey();
  const queryClient = useQueryClient();
  const [note, setNote] = createSignal('');
  const [error, setError] = createSignal('');

  const checkpoint = useMutation(() => ({
    mutationFn: (decision: CheckpointDecision) =>
      gql(CHECKPOINT_BATCH_MUTATION, { project: project(), id: props.batch.id, decision, note: note() }),
    onSuccess: () => {
      setError('');
      void queryClient.invalidateQueries({ queryKey: ['batch'] });
      void queryClient.invalidateQueries({ queryKey: ['roadmaps'] });
      void queryClient.invalidateQueries({ queryKey: ['batches'] });
      void queryClient.invalidateQueries({ queryKey: ['needsYou'] });
    },
    onError: (err: Error) => setError(err.message),
  }));

  return (
    <Show
      when={!props.batch.checkpointDecision}
      fallback={
        <p class="checkpoint__done">
          <span class={`chip chip--decision chip--decision-${props.batch.checkpointDecision}`}>
            {label(props.batch.checkpointDecision ?? '')}
          </span>
          <span class="meta">
            by {props.batch.checkpointBy}
            <Show when={props.batch.checkpointAt}>{(at) => <> {relativeTime(at())}</>}</Show>
          </span>
          <Show when={props.batch.checkpointNote}>{(n) => <span class="checkpoint__note">{n()}</span>}</Show>
        </p>
      }
    >
      <div class="checkpoint">
        <label class="checkpoint__label" for={`checkpoint-note-${props.batch.id}`}>
          What did this batch show, and does it change the plan?
        </label>
        <textarea
          id={`checkpoint-note-${props.batch.id}`}
          rows={3}
          value={note()}
          onInput={(event) => setNote(event.currentTarget.value)}
          placeholder="Optional for continue; say what changes for adjust or stop."
        />
        <div class="checkpoint__actions">
          <For each={DECISIONS}>
            {(decision) => (
              <button
                type="button"
                class={`btn ${decision.value === 'continue' ? 'btn--primary' : decision.value === 'stop' ? 'btn--danger' : ''}`}
                disabled={checkpoint.isPending}
                onClick={() => checkpoint.mutate(decision.value)}
              >
                {label(decision.value)}
              </button>
            )}
          </For>
        </div>
        {/* Spelled out rather than in tooltips: a title is invisible to keyboard and touch users,
            and the difference between adjust and stop is the whole decision. */}
        <ul class="checkpoint__hints">
          <For each={DECISIONS}>
            {(decision) => (
              <li>
                <strong>{label(decision.value)}:</strong> {decision.hint}
              </li>
            )}
          </For>
        </ul>
        <Show when={error()}>{(message) => <p class="error-text">{message()}</p>}</Show>
      </div>
    </Show>
  );
}
