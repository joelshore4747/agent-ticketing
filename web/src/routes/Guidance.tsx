import { For, Show, createMemo } from 'solid-js';
import { useGuidanceQuery } from '../api/hooks';
import { GuidanceDocument } from '../components/GuidanceDocument';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { label } from '../lib/labels';
import { relativeTime } from '../lib/time';
import type { GuidanceDoc, GuidanceKind } from '../api/types';

// Bundle order: an agent reads these top to bottom, and the page shows them in the order they were
// read rather than alphabetically, so a human is looking at the same document the agent was.
const KIND_ORDER: GuidanceKind[] = ['workflow', 'area', 'convention', 'checklist'];

/** The guidance corpus: what this project tells an agent about how it is written and why.
 *
 *  It exists as a page because guidance rots silently otherwise. `just guidance-check` can flag a
 *  document whose code has moved, but only a person can decide what it should now say, and they
 *  cannot do that without somewhere to read it. */
export function Guidance() {
  const guidance = useGuidanceQuery();

  const grouped = createMemo(() => {
    const docs = guidance.data ?? [];
    return KIND_ORDER.map((kind) => ({
      kind,
      docs: docs
        .filter((doc) => doc.kind === kind)
        .sort((a, b) => a.weight - b.weight || a.slug.localeCompare(b.slug)),
    })).filter((group) => group.docs.length > 0);
  });

  const stale = createMemo(() => (guidance.data ?? []).filter((doc) => doc.stale));

  return (
    <>
      <Topbar
        title="Guidance"
        lead="How this project is written and why. Every one of these rides along with a ticket when it is claimed."
      />

      <div class="view view--narrow">
        <Show when={guidance.error}>
          <p class="error-text">The guidance could not be loaded.</p>
        </Show>

        <Show when={!guidance.isPending} fallback={<SkeletonLines rows={5} />}>
          <Show
            when={(guidance.data?.length ?? 0) > 0}
            fallback={
              <p class="empty-state">
                Nothing yet. <span class="code">just guidance</span> loads the seed corpus from{' '}
                <span class="code">docs/guidance/</span>, and after that the API is the source of truth.
              </p>
            }
          >
            <Show when={stale().length > 0}>
              <p class="doc__stale">
                <strong>
                  {stale().length === 1
                    ? 'One document describes code that has moved.'
                    : `${stale().length} documents describe code that has moved.`}
                </strong>{' '}
                They are still served — no guidance is worse than old guidance — but nobody has
                re-read them since the code changed underneath.
              </p>
            </Show>

            <For each={grouped()}>
              {(group) => (
                <section class="panel">
                  <h2 class="panel__title">{label(group.kind)}</h2>
                  <For each={group.docs}>{(doc) => <GuidanceEntry doc={doc} />}</For>
                </section>
              )}
            </For>
          </Show>
        </Show>
      </div>
    </>
  );
}

/** A document plus the two dates that say whether anyone still stands behind it. */
function GuidanceEntry(props: { doc: GuidanceDoc }) {
  return (
    <div class="doc-entry">
      <GuidanceDocument doc={props.doc} />
      <p class="meta doc-entry__meta">
        <Show when={props.doc.reviewedAt} fallback={<span>Never reviewed</span>}>
          {(at) => <span>Reviewed {relativeTime(at())}</span>}
        </Show>
        <Show when={props.doc.driftReportedAt}>
          {(at) => <span>· drift reported {relativeTime(at())}</span>}
        </Show>
      </p>
    </div>
  );
}
