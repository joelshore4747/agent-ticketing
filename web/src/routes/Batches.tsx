import { For, Show } from 'solid-js';
import { Link } from '@tanstack/solid-router';
import { useBatchesQuery } from '../api/hooks';
import { Icon } from '../components/Icon';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { label } from '../lib/labels';
import { relativeTime } from '../lib/time';
import type { Batch } from '../api/types';

/** Batches: the provenance side of the tracker. A batch says which tickets were produced together
 *  and why, and holds the guidance whoever picks one of them up is handed along with the work.
 *
 *  Read-only on purpose. A batch is opened and closed by the thing doing the work — start_batch and
 *  finish_batch over MCP — so a New batch button here would only ever produce an empty one. */
export function Batches() {
  const batches = useBatchesQuery();

  return (
    <>
      <Topbar
        title="Batches"
        lead="Tickets that were filed together, why they were, and the guidance that went out with them."
      />

      <div class="view view--narrow">
        <Show when={batches.error}>
          <p class="error-text">The batches could not be loaded.</p>
        </Show>

        <Show when={!batches.isPending} fallback={<SkeletonLines rows={4} />}>
          <Show
            when={(batches.data?.length ?? 0) > 0}
            fallback={
              <p class="empty-state">
                No batches yet. One is opened with <span class="code">start_batch</span> over MCP, or{' '}
                <span class="code">POST /api/batches</span>, and closed when its work has been filed.
              </p>
            }
          >
            <ul class="boxes boxes--wide">
              <For each={batches.data}>{(batch) => <BatchBox batch={batch} />}</For>
            </ul>
          </Show>
        </Show>
      </div>
    </>
  );
}

function BatchBox(props: { batch: Batch }) {
  const batch = () => props.batch;
  const findings = () => (batch().findingsCount === 1 ? '1 finding' : `${batch().findingsCount} findings`);

  return (
    <li class="box box--link">
      <Link to="/b/$batchId" params={{ batchId: batch().id }} search={keepProject} class="box__name">
        <Icon name="batch" size={18} />
        <span>{batch().title || `Untitled ${batch().kind}`}</span>
      </Link>

      <p class="box__desc">{batch().brief || 'No brief was written for this batch.'}</p>

      <div class="box__foot">
        <span class="chip chip--type">{label(batch().kind)}</span>
        <Show
          when={batch().finishedAt}
          fallback={<span class="chip chip--live">In flight</span>}
        >
          <span class="chip chip--fixed">
            <Icon name="check" size={11} />
            Finished
          </span>
        </Show>
        <span class="meta">{findings()}</span>
        <time class="meta box__when" datetime={batch().startedAt}>
          {relativeTime(batch().startedAt)}
        </time>
      </div>
    </li>
  );
}
