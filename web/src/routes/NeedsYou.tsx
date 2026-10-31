import { For, Show, createSignal } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Link } from '@tanstack/solid-router';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { useNeedsJoelQuery, useNeedsYouQuery, useProjectKey, useProjectsQuery } from '../api/hooks';
import { ACCEPT_TICKET_MUTATION, CONFIRM_TRIPWIRE_MUTATION, TRANSITION_TICKET_MUTATION } from '../api/queries';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { label } from '../lib/labels';
import { renderMarkdown } from '../lib/markdown';
import { relativeTime } from '../lib/time';
import { whatYouDoSection } from '../lib/whatYouDo';
import type { Batch, Ticket, Tripwire } from '../api/types';

const DONE_RESOLUTION = 'Done: Joel ticked this off in Needs you.';

/** Everything waiting on a decision only you can make. The things only you can do come first, one
 *  box each with a tick; the findings to triage come after, folded, because a long list of them
 *  buried the tripwires below it (Joel, 2026-10-29: "really hard to distinguish"). */
export function NeedsYou() {
  const needs = useNeedsYouQuery();
  const needsJoel = useNeedsJoelQuery();
  const project = useProjectKey();
  const projects = useProjectsQuery();
  // No project in the URL means the API's default, the oldest project: name it rather than say "default".
  const projectName = () => project() ?? projects.data?.[0]?.key ?? '';
  const n = () => needs.data;

  // One to-do per needs-joel ticket: the triage ones needsYou already carries, plus the accepted ones
  // only the tag query finds. A failed tag query still leaves the triage ones.
  const todoTickets = (): Ticket[] => {
    const byKey = new Map<string, Ticket>();
    for (const t of n()?.triage ?? []) if (t.tags?.includes('needs-joel')) byKey.set(t.key, t);
    for (const t of needsJoel.data ?? []) byKey.set(t.key, t);
    return [...byKey.values()];
  };
  const findings = () => (n()?.triage ?? []).filter((t) => !t.tags?.includes('needs-joel'));
  const todoCount = () => (n()?.tripwires.length ?? 0) + todoTickets().length + (n()?.checkpoints.length ?? 0);

  return (
    <>
      <Topbar
        title="Needs you"
        lead={`Everything waiting on a decision only you can make${projectName() ? `, in project ${projectName()}` : ''}.`}
      />
      <div class="view view--narrow">
        <Show when={needs.error}>
          <p class="error-text">The queue could not be loaded.</p>
        </Show>
        <Show when={!needs.isPending} fallback={<SkeletonLines rows={5} />}>
          <Show
            when={(n()?.total ?? 0) + todoTickets().length > 0}
            fallback={<p class="empty-state">Nothing is waiting on you.</p>}
          >
            <div class="needs-page">
              <Show when={todoCount() > 0}>
                <section class="panel todo" aria-labelledby="todo-heading">
                  <div class="panel__head">
                    <h2 id="todo-heading">Your to-do list</h2>
                    <span class="rail__count rail__count--attention">{todoCount()}</span>
                  </div>
                  <p class="todo__lead">Only you can do these. Each box says what to do; tick it off when it is done.</p>
                  <ul class="todo__list">
                    <For each={n()?.tripwires}>{(tw) => <TripwireTodo tripwire={tw} />}</For>
                    <For each={todoTickets()}>{(t) => <TicketTodo ticket={t} />}</For>
                    <For each={n()?.checkpoints}>{(b) => <CheckpointTodo batch={b} />}</For>
                  </ul>
                </section>
              </Show>

              <Group title="Findings to accept or dismiss" count={findings().length} folded>
                <For each={findings()}>
                  {(t) => <Row to={t.key} key={t.key} text={t.summaryForHuman || t.title} meta={`filed ${relativeTime(t.createdAt)}`} />}
                </For>
              </Group>

              <Group title="Proposals to read" count={n()?.proposals.length ?? 0}>
                <For each={n()?.proposals}>
                  {(p) => (
                    <Row
                      to={p.ticketKey}
                      key={p.ticketKey}
                      text={`${label(p.field.replace('_', ' '))}: ${p.proposed}`}
                      meta={`${p.ticketTitle} · proposed ${relativeTime(p.createdAt)}`}
                    />
                  )}
                </For>
              </Group>

              <Group title="Claude's decisions with no evidence" count={n()?.uncited.length ?? 0}>
                <For each={n()?.uncited}>
                  {(u) => (
                    <Row
                      to={u.ticketKey}
                      key={u.ticketKey}
                      text={`${label(u.decision.kind)}${u.decision.rationale ? `: ${u.decision.rationale}` : ''}`}
                      meta={`${u.ticketTitle} · ${relativeTime(u.decision.createdAt)}`}
                    />
                  )}
                </For>
              </Group>
            </div>
          </Show>
        </Show>
      </div>
    </>
  );
}

function Group(props: { title: string; count: number; folded?: boolean; children: unknown }) {
  const head = (
    <div class="panel__head">
      <h2>{props.title}</h2>
      <span class="rail__count">{props.count}</span>
    </div>
  );
  return (
    <Show when={props.count > 0}>
      <section class="panel">
        <Show when={props.folded} fallback={<>{head}<ul class="needs">{props.children as never}</ul></>}>
          <details class="needs__fold">
            <summary>
              {head}
              <span class="needs__fold-hint">Show them</span>
            </summary>
            <ul class="needs">{props.children as never}</ul>
          </details>
        </Show>
      </section>
    </Show>
  );
}

function Row(props: { to: string; key: string; text: string; meta: string; children?: JSX.Element }) {
  return (
    <li class="needs__row">
      <Link to="/t/$key" params={{ key: props.to }} search={keepProject} class="needs__link">
        <span class="key">{props.key}</span>
        <span>{props.text}</span>
      </Link>
      <span class="meta">{props.meta}</span>
      {props.children}
    </li>
  );
}

/** One box on the to-do list: what kind of task, what to do, and the tick. */
function TodoBox(props: { kind: string; title: JSX.Element; children?: JSX.Element; action: JSX.Element }) {
  return (
    <li class="todo__box">
      <span class="todo__kind">{props.kind}</span>
      <div class="todo__title">{props.title}</div>
      <div class="todo__body">{props.children}</div>
      <div class="todo__action">{props.action}</div>
    </li>
  );
}

function TripwireTodo(props: { tripwire: Tripwire }) {
  const project = useProjectKey();
  const queryClient = useQueryClient();
  const [error, setError] = createSignal('');
  const confirm = useMutation(() => ({
    mutationFn: () => gql(CONFIRM_TRIPWIRE_MUTATION, { project: project(), id: props.tripwire.id }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['needsYou'] }),
    onError: (err: Error) => setError(err.message),
  }));
  return (
    <TodoBox
      kind="Confirm a safety test"
      title={<>Protects: {props.tripwire.meaning}</>}
      action={
        <>
          <button type="button" class="btn btn--primary todo__tick" disabled={confirm.isPending} onClick={() => confirm.mutate()}>
            ✓ Confirm
          </button>
          <Show when={error()}>{(message) => <p class="error-text">{message()}</p>}</Show>
        </>
      }
    >
      <p>
        The loop will change the code this test covers. Confirm that the test still says what must stay true, and the
        loop may go ahead.
      </p>
      <code class="code">{props.tripwire.testRef}</code>
    </TodoBox>
  );
}

function TicketTodo(props: { ticket: Ticket }) {
  const queryClient = useQueryClient();
  const [armed, setArmed] = createSignal(false);
  const [error, setError] = createSignal('');
  // Ticking off closes the ticket. A triage ticket is accepted first, because only an open ticket may
  // close as done rather than be dismissed (dismissing records a rejection, which this is not).
  const tickOff = useMutation(() => ({
    mutationFn: async () => {
      let version = props.ticket.version;
      if (props.ticket.status === 'triage') {
        const accepted = await gql<{ acceptTicket: Ticket }>(ACCEPT_TICKET_MUTATION, { id: props.ticket.id, version });
        version = accepted.acceptTicket.version;
      }
      return gql(TRANSITION_TICKET_MUTATION, { id: props.ticket.id, version, status: 'closed', resolution: DONE_RESOLUTION });
    },
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['needsYou'] }),
    onError: (err: Error) => {
      setArmed(false);
      setError(err.message);
    },
  }));
  const steps = () => whatYouDoSection(props.ticket.description);
  return (
    <TodoBox
      kind="Do this"
      title={
        <Link to="/t/$key" params={{ key: props.ticket.key }} search={keepProject} class="needs__link">
          <span class="key">{props.ticket.key}</span>
          <span>{todoTitle(props.ticket.title)}</span>
        </Link>
      }
      action={
        <>
          <Show
            when={armed()}
            fallback={
              <button type="button" class="btn todo__tick" onClick={() => setArmed(true)}>
                ✓ Done, tick off
              </button>
            }
          >
            <button type="button" class="btn btn--primary todo__tick" disabled={tickOff.isPending} onClick={() => tickOff.mutate()}>
              Yes, it is done
            </button>
            <button type="button" class="btn btn--small" onClick={() => setArmed(false)}>
              Not yet
            </button>
          </Show>
          <Show when={error()}>{(message) => <p class="error-text">{message()}</p>}</Show>
        </>
      }
    >
      <Show when={steps()} fallback={<p>Open the ticket for what to do.</p>}>
        {(section) => <div class="needs__steps markdown-body" innerHTML={renderMarkdown(section())} />}
      </Show>
    </TodoBox>
  );
}

/** A needs-joel title without its "Joel:" prefix (the box already says it is yours), capitalised. */
function todoTitle(title: string): string {
  const bare = title.replace(/^Joel:\s*/, '');
  return bare.charAt(0).toUpperCase() + bare.slice(1);
}

function CheckpointTodo(props: { batch: Batch }) {
  return (
    <TodoBox
      kind="Sign off a finished batch"
      title={
        <>
          Batch {props.batch.sequence}: {props.batch.title}
        </>
      }
      action={
        <Link to="/b/$batchId" params={{ batchId: props.batch.id }} search={keepProject} class="btn todo__tick">
          Read its report
        </Link>
      }
    >
      <p>
        Finished {props.batch.finishedAt ? relativeTime(props.batch.finishedAt) : ''}. Read its report, then continue, adjust or
        stop it. The loop's approver signs it for you if you leave it, and you can overrule that.
      </p>
    </TodoBox>
  );
}
