import { For, Show } from 'solid-js';
import { isUncited, supersededIds } from '../lib/decisions';
import { label } from '../lib/labels';
import { relativeTime } from '../lib/time';
import type { Decision } from '../api/types';

/** What has been decided about the ticket, newest first. Replaced decisions stay, dimmed: the
 *  record is how a changed mind is explained, not something to tidy away. */
export function DecisionsPanel(props: { decisions: Decision[] }) {
  const superseded = () => supersededIds(props.decisions);

  return (
    <section class="panel" aria-labelledby="decisions-head">
      <div class="panel__head">
        <h2 id="decisions-head">Decisions</h2>
      </div>
      <ol class="decisions">
        <For each={props.decisions}>
          {(d) => (
            <li class={['decision', { 'decision--superseded': superseded().has(d.id) }]}>
              <p class="decision__head">
                <span class={`chip decision__kind decision__kind--${d.kind}`}>{label(d.kind)}</span>
                <span>{d.madeBy === 'human' ? 'You' : 'Claude Code'}</span>
                <time class="meta" datetime={d.createdAt} title={d.createdAt}>
                  {relativeTime(d.createdAt)}
                </time>
                <Show when={superseded().has(d.id)}>
                  <span class="meta">Replaced</span>
                </Show>
                <Show when={isUncited(d, superseded())}>
                  <span class="chip chip--attention">No evidence cited</span>
                </Show>
              </p>
              <Show when={d.rationale}>
                <p class="decision__rationale">{d.rationale}</p>
              </Show>
              <Show when={d.revisitTrigger}>
                {(trigger) => (
                  <p class="decision__trigger">
                    <span class="meta">Revisit</span> {trigger()}
                  </p>
                )}
              </Show>
              <Show when={d.evidenceRefs.length > 0}>
                <ul class="evidence">
                  <For each={d.evidenceRefs}>{(ref) => <li><code class="code">{ref}</code></li>}</For>
                </ul>
              </Show>
            </li>
          )}
        </For>
      </ol>
    </section>
  );
}
