import { For, Show, createSignal } from 'solid-js';
import { label } from '../lib/labels';
import { parseCriteria, pendingProposals, type Proposal } from '../lib/proposals';
import { relativeTime } from '../lib/time';
import type { CriterionInput, CriterionTier, FoundationInput, Ticket, Verdict } from '../api/types';

const TIERS: CriterionTier[] = ['behavioural', 'measurement', 'gate'];
const FIELD_LABEL: Record<string, string> = {
  intent: 'Intent',
  constraints: 'Constraints',
  non_goals: 'Non-goals',
  criteria: 'Success criteria',
};

/** The human's section of a ticket: what they want, the boundaries, and how they will know it is
 *  done. Only the human edits it; Claude's proposals to it wait here until applied or left alone. */
/** What a verdict means to the reader. A miss is the urgent hue because it has filed work; a
 *  deviation is neutral, since it was a choice with a reason, not a failure. */
const VERDICT: Record<Verdict, { text: string; chip: string }> = {
  pass: { text: 'Met', chip: 'chip--fixed' },
  fail: { text: 'Missed', chip: 'chip--priority chip--urgent' },
  deviation: { text: 'Met differently', chip: 'chip--type' },
};

export function FoundationPanel(props: { ticket: Ticket; saving: boolean; onSave: (input: FoundationInput) => void }) {
  const t = () => props.ticket;
  const [editing, setEditing] = createSignal(false);
  const [intent, setIntent] = createSignal('');
  const [constraints, setConstraints] = createSignal('');
  const [nonGoals, setNonGoals] = createSignal('');
  const [criteria, setCriteria] = createSignal<CriterionInput[]>([]);

  const startEditing = () => {
    setIntent(t().intent ?? '');
    setConstraints(t().constraints ?? '');
    setNonGoals(t().nonGoals ?? '');
    setCriteria((t().criteria ?? []).map((c) => ({ tier: c.tier, text: c.text, tripwireId: c.tripwire?.id })));
    setEditing(true);
  };
  const save = () => {
    props.onSave({ intent: intent(), constraints: constraints(), nonGoals: nonGoals(), criteria: criteria().filter((c) => c.text.trim()) });
    setEditing(false);
  };
  const apply = (p: Proposal) => {
    if (p.field === 'criteria') props.onSave({ criteria: parseCriteria(p.proposed) });
    else if (p.field === 'intent') props.onSave({ intent: p.proposed });
    else if (p.field === 'constraints') props.onSave({ constraints: p.proposed });
    else props.onSave({ nonGoals: p.proposed });
  };
  const setCriterion = (i: number, patch: Partial<CriterionInput>) =>
    setCriteria(criteria().map((c, j) => (j === i ? { ...c, ...patch } : c)));

  const empty = () => !t().intent && !t().constraints && !t().nonGoals && (t().criteria?.length ?? 0) === 0;
  const proposals = () => pendingProposals(t().events ?? []);

  return (
    <section class="panel foundation" aria-labelledby="foundation-head">
      <div class="panel__head">
        <h2 id="foundation-head">What you want</h2>
        <Show when={!editing()}>
          <button type="button" class="btn btn--small" onClick={startEditing}>
            {empty() ? 'Write it' : 'Edit'}
          </button>
        </Show>
      </div>

      <Show
        when={editing()}
        fallback={
          <Show
            when={!empty()}
            fallback={<p class="meta">Not written yet. Claude reads this before the work; without it, it guesses.</p>}
          >
            <dl class="foundation__fields">
              <Show when={t().intent}>
                <div><dt>Intent</dt><dd>{t().intent}</dd></div>
              </Show>
              <Show when={t().constraints}>
                <div><dt>Constraints</dt><dd>{t().constraints}</dd></div>
              </Show>
              <Show when={t().nonGoals}>
                <div><dt>Non-goals</dt><dd>{t().nonGoals}</dd></div>
              </Show>
            </dl>
            <Show when={(t().criteria?.length ?? 0) > 0}>
              <h3 class="foundation__sub">Done when</h3>
              <ol class="criteria">
                <For each={t().criteria}>
                  {(c) => (
                    <li>
                      <span class="chip chip--type">{label(c.tier)}</span> {c.text}
                      <Show when={c.tripwire}>
                        {(tw) => (
                          <span class="meta criteria__tripwire">
                            guarded by {tw().testRef}
                            {tw().confirmedAt ? '' : ' (meaning not confirmed yet)'}
                          </span>
                        )}
                      </Show>
                      {/* Only the newest verdict counts; the rest are history on the ticket's events. */}
                      <Show when={c.verdicts?.[0]}>
                        {(v) => (
                          <span class="criteria__verdict">
                            <span class={`chip ${VERDICT[v().verdict].chip}`}>{VERDICT[v().verdict].text}</span>
                            <Show when={v().reason}>{(reason) => <span class="meta">{reason()}</span>}</Show>
                          </span>
                        )}
                      </Show>
                    </li>
                  )}
                </For>
              </ol>
            </Show>
          </Show>
        }
      >
        <div class="foundation__form">
          <label>
            <span>Intent: the outcome you want, in your words</span>
            <textarea rows={2} value={intent()} onInput={(e) => setIntent(e.currentTarget.value)} />
          </label>
          <label>
            <span>Constraints: boundaries, not code</span>
            <textarea rows={2} value={constraints()} onInput={(e) => setConstraints(e.currentTarget.value)} />
          </label>
          <label>
            <span>Non-goals: what this deliberately does not do</span>
            <textarea rows={2} value={nonGoals()} onInput={(e) => setNonGoals(e.currentTarget.value)} />
          </label>
          <fieldset class="criteria-editor">
            <legend>Done when: how you will check it in under five minutes, without reading the diff</legend>
            <For each={criteria()}>
              {(c, i) => (
                <div class="criteria-editor__row">
                  <select
                    aria-label={`Criterion ${i() + 1} tier`}
                    value={c.tier}
                    onChange={(e) => setCriterion(i(), { tier: e.currentTarget.value as CriterionTier })}
                  >
                    <For each={TIERS}>{(tier) => <option value={tier}>{label(tier)}</option>}</For>
                  </select>
                  <input
                    aria-label={`Criterion ${i() + 1}`}
                    value={c.text}
                    onInput={(e) => setCriterion(i(), { text: e.currentTarget.value })}
                  />
                  <button type="button" class="btn btn--small btn--quiet" onClick={() => setCriteria(criteria().filter((_, j) => j !== i()))}>
                    Remove
                  </button>
                </div>
              )}
            </For>
            <button type="button" class="btn btn--small" onClick={() => setCriteria([...criteria(), { tier: 'behavioural', text: '' }])}>
              Add a criterion
            </button>
          </fieldset>
          <div class="foundation__actions">
            <button type="button" class="btn btn--primary" disabled={props.saving} onClick={save}>
              Save
            </button>
            <button type="button" class="btn btn--quiet" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </div>
      </Show>

      <Show when={(t().checks?.length ?? 0) > 0}>
        <h3 class="foundation__sub">Public checks</h3>
        <ul class="criteria">
          <For each={t().checks}>
            {(c) => (
              <li>
                {c.description}
                <Show when={c.command}>{(cmd) => <code class="code"> {cmd()}</code>}</Show>
              </li>
            )}
          </For>
        </ul>
      </Show>

      <Show when={proposals().length > 0}>
        <div class="proposals">
          <h3 class="foundation__sub">Claude proposes</h3>
          <p class="meta">Nothing has changed. Apply one to take it; leaving it alone declines it.</p>
          <For each={proposals()}>
            {(p) => (
              <div class="proposal">
                <p class="proposal__head">
                  <strong>{FIELD_LABEL[p.field]}</strong>
                  <span class="meta">
                    {p.actor}, {relativeTime(p.createdAt)}
                  </span>
                </p>
                <pre class="proposal__text">{p.proposed}</pre>
                <button type="button" class="btn btn--small btn--primary" disabled={props.saving} onClick={() => apply(p)}>
                  Apply
                </button>
              </div>
            )}
          </For>
        </div>
      </Show>
    </section>
  );
}
