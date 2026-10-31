import { For, Show } from 'solid-js';
import { Link } from '@tanstack/solid-router';
import { useRoadmapsQuery } from '../api/hooks';
import { CheckpointControl } from '../components/CheckpointControl';
import { Icon } from '../components/Icon';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { renderMarkdown } from '../lib/markdown';
import type { Roadmap, RoadmapBatch, RoadmapState } from '../api/types';

/** What each state means to the person reading it. Only two states ask anything of a human, and
 *  only those carry the attention colour: awaiting a checkpoint, and a batch with work to accept. */
const STATE: Record<RoadmapState, { text: string; chip: string }> = {
  locked: { text: 'Locked', chip: 'chip--type' },
  unplanned: { text: 'Not planned yet', chip: 'chip--type' },
  open: { text: 'Open', chip: 'chip--live' },
  at_eval: { text: 'At its eval', chip: 'chip--live' },
  awaiting_checkpoint: { text: 'Needs your checkpoint', chip: 'chip--attention' },
  finished: { text: 'Finished', chip: 'chip--fixed' },
  stopped: { text: 'Stopped', chip: 'chip--stale' },
};

/** Roadmaps: ordered batches following one specification. The next batch opens only when the one
 *  before it has finished and you have signed off its checkpoint, which is where the plan changes
 *  direction. Planning is done over MCP (create_roadmap, plan_batch); deciding is done here. */
export function Roadmaps() {
  const roadmaps = useRoadmapsQuery();

  return (
    <>
      <Topbar
        title="Roadmaps"
        lead="Batches in the order they are worked, and the checkpoint between each one where the plan can change."
      />
      <div class="view view--narrow">
        <Show when={roadmaps.error}>
          <p class="error-text">The roadmaps could not be loaded.</p>
        </Show>
        <Show when={!roadmaps.isPending} fallback={<SkeletonLines rows={4} />}>
          <Show
            when={(roadmaps.data?.length ?? 0) > 0}
            fallback={
              <p class="empty-state">
                No roadmaps yet. One is started with <span class="code">create_roadmap</span> over MCP, and filled
                with <span class="code">plan_batch</span>.
              </p>
            }
          >
            <For each={roadmaps.data}>{(roadmap) => <RoadmapSection roadmap={roadmap} />}</For>
          </Show>
        </Show>
      </div>
    </>
  );
}

function RoadmapSection(props: { roadmap: Roadmap }) {
  return (
    <section class="panel roadmap">
      <div class="panel__head">
        <h2>{props.roadmap.title}</h2>
        <span class="meta">
          {props.roadmap.batches.filter((b) => b.state === 'finished').length} of {props.roadmap.batches.length}{' '}
          finished
        </span>
      </div>
      <Show when={props.roadmap.spec}>
        {(spec) => (
          <details class="roadmap__spec">
            <summary>The specification it follows</summary>
            <div class="markdown-body" innerHTML={renderMarkdown(spec())} />
          </details>
        )}
      </Show>
      <ol class="roadmap__batches">
        <For each={props.roadmap.batches}>{(entry) => <RoadmapRow entry={entry} />}</For>
      </ol>
    </section>
  );
}

function RoadmapRow(props: { entry: RoadmapBatch }) {
  const batch = () => props.entry.batch;
  const state = () => STATE[props.entry.state];
  return (
    <li class="roadmap__row" data-state={props.entry.state}>
      <span class="plan__position">{batch().sequence}</span>
      <div class="roadmap__body">
        <div class="roadmap__line">
          <Link to="/b/$batchId" params={{ batchId: batch().id }} search={keepProject} class="roadmap__title">
            <Icon name="batch" size={16} />
            <span>{batch().title}</span>
          </Link>
          <span class={`chip ${state().chip}`}>{state().text}</span>
          <Show when={props.entry.work > 0}>
            <span class="meta">
              {props.entry.workDone}/{props.entry.work} done
            </span>
          </Show>
        </div>
        <Show when={batch().what}>{(what) => <p class="roadmap__what">{what()}</p>}</Show>
        <Show when={props.entry.state === 'awaiting_checkpoint'}>
          <CheckpointControl batch={batch()} />
        </Show>
        <Show when={batch().checkpointDecision}>
          <CheckpointControl batch={batch()} />
        </Show>
      </div>
    </li>
  );
}
