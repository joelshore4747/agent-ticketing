import { For, Show, createSignal } from 'solid-js';
import type { Status } from '../api/types';

interface TransitionOption {
  label: string;
  target: Status;
}

// How each move reads as a button. Which moves are offered is the API's call: the ticket carries its
// nextStatuses (rule 4, TP-27), and triage has its own accept/dismiss actions.
const OPTIONS: Partial<Record<Status, TransitionOption[]>> = {
  open: [
    { label: 'Start work', target: 'in_progress' },
    { label: 'Close without fixing', target: 'closed' },
  ],
  in_progress: [
    { label: 'Stop work', target: 'open' },
    { label: 'Mark done', target: 'done' },
    { label: 'Close without fixing', target: 'closed' },
  ],
  done: [{ label: 'Reopen', target: 'open' }],
  closed: [{ label: 'Reopen', target: 'open' }],
};

function needsResolution(target: Status): boolean {
  return target === 'done' || target === 'closed';
}

export function StatusControl(props: {
  status: Status;
  nextStatuses: Status[];
  onAccept: () => void;
  onDismiss: () => void;
  onTransition: (target: Status, resolution?: string) => void;
}) {
  const [pendingTarget, setPendingTarget] = createSignal<Status | null>(null);
  const [resolution, setResolution] = createSignal('');

  const selectTarget = (target: Status) => {
    if (needsResolution(target)) {
      setPendingTarget(target);
      setResolution('');
    } else {
      props.onTransition(target);
    }
  };

  const confirmResolution = () => {
    const target = pendingTarget();
    if (!target || !resolution().trim()) return;
    props.onTransition(target, resolution().trim());
    setPendingTarget(null);
  };

  return (
    <div>
      <Show
        when={props.status !== 'triage'}
        fallback={
          <div class="btn-row">
            <button type="button" class="btn btn--primary" onClick={props.onAccept}>
              Accept
            </button>
            <button type="button" class="btn" onClick={props.onDismiss}>
              Dismiss
            </button>
          </div>
        }
      >
        <div class="btn-row">
          <For each={(OPTIONS[props.status] ?? []).filter((o) => props.nextStatuses.includes(o.target))}>
            {(option) => (
              <button
                type="button"
                class={['btn', { 'btn--primary': option.target === 'in_progress' || option.target === 'done' }]}
                onClick={() => selectTarget(option.target)}
              >
                {option.label}
              </button>
            )}
          </For>
        </div>
      </Show>

      <Show when={pendingTarget()}>
        <div class="resolution-form">
          <label for="resolution-text">What was done, in one line. Enter saves, Escape cancels.</label>
          <textarea
            id="resolution-text"
            ref={(el) => queueMicrotask(() => el.focus())}
            value={resolution()}
            onInput={(event) => setResolution(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') setPendingTarget(null);
              if (event.key === 'Enter' && !event.shiftKey) {
                event.preventDefault();
                confirmResolution();
              }
            }}
          />
          <div class="btn-row">
            <button type="button" class="btn btn--primary" disabled={!resolution().trim()} onClick={confirmResolution}>
              Confirm
            </button>
            <button type="button" class="btn" onClick={() => setPendingTarget(null)}>
              Cancel
            </button>
          </div>
        </div>
      </Show>
    </div>
  );
}
