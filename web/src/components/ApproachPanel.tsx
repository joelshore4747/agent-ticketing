import { For, Show } from 'solid-js';
import { relativeTime } from '../lib/time';
import type { Ticket } from '../api/types';

/** Claude's proposal for how to do the work, and the human's approval of it. Editing an approved
 *  approach clears the approval server-side, so what is shown approved is what was approved. */
export function ApproachPanel(props: { ticket: Ticket; saving: boolean; onApprove: () => void }) {
  const t = () => props.ticket;
  const needsExample = () => t().type === 'feature' && !t().workedExample?.trim();
  const blocked = () => (!t().approach?.trim() ? 'Claude has not written an approach yet.' : needsExample() ? 'A feature needs a worked example before its approach is approved.' : '');

  return (
    <section class="panel approach" aria-labelledby="approach-head">
      <div class="panel__head">
        <h2 id="approach-head">How Claude will do it</h2>
        <Show
          when={t().approachApprovedAt}
          fallback={
            <button type="button" class="btn btn--small btn--primary" disabled={props.saving || !!blocked()} onClick={props.onApprove}>
              Approve
            </button>
          }
        >
          {(at) => (
            <span class="chip chip--fixed">
              Approved by {t().approachApprovedBy} {relativeTime(at())}
            </span>
          )}
        </Show>
      </div>
      <Show when={blocked() && !t().approachApprovedAt}>
        <p class="meta">{blocked()}</p>
      </Show>
      <dl class="foundation__fields">
        <Show when={t().approach}><div><dt>Approach</dt><dd>{t().approach}</dd></div></Show>
        <Show when={t().riskAndRollback}><div><dt>Risk and rollback</dt><dd>{t().riskAndRollback}</dd></div></Show>
        <Show when={t().workedExample}><div><dt>Worked example</dt><dd><pre class="proposal__text">{t().workedExample}</pre></dd></div></Show>
        <Show when={t().verificationPlan}><div><dt>Verification plan</dt><dd><pre class="proposal__text">{t().verificationPlan}</pre></dd></div></Show>
        <Show when={t().estimatedTokens != null}>
          <div>
            <dt>Estimate</dt>
            <dd>
              {t().estimatedTokens?.toLocaleString()} tokens
              <Show when={t().estimatedCostUsd != null}> · ${t().estimatedCostUsd?.toFixed(2)} (estimate)</Show>
            </dd>
          </div>
        </Show>
      </dl>
      <Show when={(t().evidenceRefs?.length ?? 0) > 0}>
        <h3 class="foundation__sub">Evidence</h3>
        <ul class="evidence">
          <For each={t().evidenceRefs}>{(ref) => <li><code class="code">{ref}</code></li>}</For>
        </ul>
      </Show>
    </section>
  );
}
