import { For, Show, createSignal } from 'solid-js';
import { Link, useParams } from '@tanstack/solid-router';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql, isVersionConflict } from '../api/client';
import { renderMarkdown } from '../lib/markdown';
import { whatYouDoSection } from '../lib/whatYouDo';
import {
  ACCEPT_TICKET_MUTATION,
  APPROVE_APPROACH_MUTATION,
  DISMISS_TICKET_MUTATION,
  SET_FOUNDATION_MUTATION,
  TRANSITION_TICKET_MUTATION,
  UPDATE_TICKET_MUTATION,
} from '../api/queries';
import { ticketKey, useAreasQuery, useProjectKey, useTicketQuery } from '../api/hooks';
import { InlineNotice } from '../components/InlineNotice';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { SkeletonLines } from '../components/SkeletonLines';
import { InlineEditText } from '../components/InlineEditText';
import { StatusControl } from '../components/StatusControl';
import { DescriptionEditor } from '../components/DescriptionEditor';
import { LocationsList } from '../components/LocationsList';
import { CommentsPanel } from '../components/CommentsPanel';
import { ActivityLog } from '../components/ActivityLog';
import { ApproachPanel } from '../components/ApproachPanel';
import { FoundationPanel } from '../components/FoundationPanel';
import { DecisionsPanel } from '../components/DecisionsPanel';
import { Icon } from '../components/Icon';
import { areaHue } from '../lib/areaHue';
import { label } from '../lib/labels';
import { relativeTime } from '../lib/time';
import {
  PRIORITIES,
  TICKET_TYPES,
  type Priority as PriorityValue,
  type FoundationInput,
  type Status,
  type Ticket,
  type TicketType,
  type UpdateTicketInput,
} from '../api/types';

export function TicketDetail() {
  const params = useParams({ strict: false });
  const key = () => (params() as { key?: string }).key ?? '';

  const ticketQuery = useTicketQuery(key);
  const areasQuery = useAreasQuery();
  const queryClient = useQueryClient();
  const projectKey = useProjectKey();
  const [notice, setNotice] = createSignal('');
  const [error, setError] = createSignal('');

  const ticket = () => ticketQuery.data ?? undefined;

  // Accepting, dismissing and applying a proposal all change what is waiting on the human.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ticketKey(projectKey(), key()) });
    void queryClient.invalidateQueries({ queryKey: ['needsYou'] });
  };

  const onMutationError = (err: unknown) => {
    if (isVersionConflict(err)) {
      setNotice('Ticket changed elsewhere, reloaded');
      refresh();
    } else {
      setError(err instanceof Error ? err.message : 'Something went wrong');
    }
  };

  const updateMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number; patch: UpdateTicketInput }) =>
      gql<{ updateTicket: Ticket }>(UPDATE_TICKET_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const transitionMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number; status: Status; resolution?: string }) =>
      gql<{ transitionTicket: Ticket }>(TRANSITION_TICKET_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const acceptMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number }) => gql<{ acceptTicket: Ticket }>(ACCEPT_TICKET_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const dismissMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number }) => gql<{ dismissTicket: Ticket }>(DISMISS_TICKET_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const foundationMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number; input: FoundationInput }) => gql(SET_FOUNDATION_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const approveMutation = useMutation(() => ({
    mutationFn: (vars: { id: string; version: number }) => gql(APPROVE_APPROACH_MUTATION, vars),
    onSuccess: refresh,
    onError: onMutationError,
  }));

  const savePatch = (patch: UpdateTicketInput) => {
    const t = ticket();
    if (!t) return;
    setError('');
    updateMutation.mutate({ id: t.id, version: t.version, patch });
  };

  return (
    <>
      <Topbar
        leading={
          <Link to="/" search={keepProject} class="btn btn--primary">
            <Icon name="back" size={17} />
            Back
          </Link>
        }
      />

      <div class="view view--narrow">
        <Show when={!ticketQuery.isPending} fallback={<SkeletonLines rows={8} />}>
          <Show when={ticket()} fallback={<p class="empty-state">There is no ticket {key()}.</p>}>
            {(t) => (
              <>
                <nav class="crumbs" aria-label="Breadcrumb">
                  <Link to="/" search={keepProject}>
                    Tickets
                  </Link>
                  <span aria-hidden="true">›</span>
                  <Link to={t().source === 'ai' ? '/ai' : '/human'} search={keepProject}>
                    {t().source === 'ai' ? 'AI' : 'Human'}
                  </Link>
                  <span aria-hidden="true">›</span>
                  <span>{t().key}</span>
                </nav>

                <Show when={notice()}>
                  <InlineNotice message={notice()} />
                </Show>
                <Show when={error()}>
                  <p class="error-text">{error()}</p>
                </Show>

                <article class="ticket">
                  <header class="ticket__head">
                    {/* The one thing a needs-joel ticket exists to say, shown before anything else
                        on the page (TP-240): a ticket without the section renders exactly as before. */}
                    <Show when={whatYouDoSection(t().description)}>
                      {(section) => (
                        <section class="panel">
                          <div class="panel__head">
                            <h2>What you do</h2>
                          </div>
                          <div class="markdown-body" innerHTML={renderMarkdown(section())} />
                        </section>
                      )}
                    </Show>

                    <Show when={t().area}>
                      {(area) => (
                        <p class="eyebrow" style={{ color: areaHue(area().name) }}>
                          {area().name}
                        </p>
                      )}
                    </Show>
                    <h1>
                      <InlineEditText
                        value={t().title}
                        inputClass="ticket__title-input"
                        onSave={(value) => value && savePatch({ title: value })}
                      />
                    </h1>
                    {/* The key is in the breadcrumb and the area in the eyebrow, so what is left
                        here is what classifies the work: how urgent, and what kind. */}
                    <div class="ticket__tags">
                      <span class={`chip chip--priority chip--${t().priority}`}>{label(t().priority)}</span>
                      <span class="chip chip--type">{label(t().type)}</span>
                      <span class="chip chip--source" data-source={t().source}>
                        {t().source === 'ai' ? 'Claude Code' : 'You'}
                      </span>
                      <Show when={(t().seenCount ?? 1) > 1}>
                        <span class="chip chip--type">Reported {t().seenCount} times</span>
                      </Show>
                      <For each={t().tags ?? []}>{(tag) => <span class="chip chip--tag">{tag}</span>}</For>
                    </div>
                    <Show when={t().summaryForHuman}>{(summary) => <p class="ticket__summary">{summary()}</p>}</Show>
                    <Show when={t().priority === 'urgent' && t().status !== 'done' && t().status !== 'closed'}>
                      <div class="notice">
                        <strong>Urgent</strong> Work this before anything else in its area.
                      </div>
                    </Show>
                  </header>

                  <div class="ticket__main">
                    <FoundationPanel
                      ticket={t()}
                      saving={foundationMutation.isPending}
                      onSave={(input) => {
                        setError('');
                        foundationMutation.mutate({ id: t().id, version: t().version, input });
                      }}
                    />

                    <section class="panel">
                      <DescriptionEditor value={t().description ?? ''} onSave={(value) => savePatch({ description: value })} />
                    </section>

                    <Show when={t().approach || t().verificationPlan || t().workedExample}>
                      <ApproachPanel
                        ticket={t()}
                        saving={approveMutation.isPending}
                        onApprove={() => approveMutation.mutate({ id: t().id, version: t().version })}
                      />
                    </Show>

                    <Show when={(t().decisions?.length ?? 0) > 0}>
                      <DecisionsPanel decisions={t().decisions ?? []} />
                    </Show>

                    {/* The outcome reads as an entry in the ticket's story, not as a field in a
                        sidebar — it is a sentence somebody wrote, like a comment. */}
                    <Show when={t().resolution}>
                      {(resolution) => (
                        <section class="resolved">
                          <p class="resolved__head">
                            <span class="check">
                              <Icon name="check" size={12} />
                            </span>
                            {t().status === 'closed' ? 'Closed' : 'Fixed'}
                            <Show when={t().completedAt}>
                              {(at) => <time datetime={at()}>{relativeTime(at())}</time>}
                            </Show>
                          </p>
                          <p>{resolution()}</p>
                          <Show when={t().verifiedBy || t().measuredDelta || t().gatePassed != null}>
                            <dl class="resolved__evidence">
                              <Show when={t().verifiedBy}>
                                <div><dt>Verified by</dt><dd><pre class="proposal__text">{t().verifiedBy}</pre></dd></div>
                              </Show>
                              <Show when={t().measuredDelta}>
                                <div><dt>Measured</dt><dd>{t().measuredDelta}</dd></div>
                              </Show>
                              <Show when={t().gatePassed != null}>
                                <div><dt>Held-out gate</dt><dd>{t().gatePassed ? 'Passed' : 'Failed'}</dd></div>
                              </Show>
                            </dl>
                          </Show>
                        </section>
                      )}
                    </Show>

                    <section class="panel">
                      <div class="panel__head">
                        <h2>Where in the code</h2>
                        {/* The branch belongs with the files, not in a list of fields. */}
                        <span class="branch">
                          <Icon name="branch" size={15} />
                          <InlineEditText
                            value={t().branch ?? ''}
                            placeholder="Add a branch"
                            onSave={(value) => savePatch({ branch: value })}
                          />
                        </span>
                      </div>
                      <Show
                        when={(t().locations?.length ?? 0) > 0}
                        fallback={<p class="meta">No file locations recorded.</p>}
                      >
                        <LocationsList locations={t().locations ?? []} />
                      </Show>
                    </section>

                    <section class="panel">
                      <div class="panel__head">
                        <h2>Comments</h2>
                      </div>
                      <CommentsPanel ticketId={t().id} ticketKeyStr={t().key} comments={t().comments ?? []} />
                    </section>

                    <section class="panel">
                      <div class="panel__head">
                        <h2>History</h2>
                      </div>
                      <ActivityLog events={t().events ?? []} />
                    </section>
                  </div>

                  <aside class="side" aria-label="Ticket details">
                    {/* No status badge: the actions say it. "Start work" means open, "Mark done"
                        means in progress, "Accept" means the inbox, and a ticket that is finished
                        has its resolution note in the main column. A box that only ever repeated
                        one of those was a row of chrome. */}
                    <StatusControl
                      status={t().status}
                      nextStatuses={t().nextStatuses ?? []}
                      onAccept={() => acceptMutation.mutate({ id: t().id, version: t().version })}
                      onDismiss={() => dismissMutation.mutate({ id: t().id, version: t().version })}
                      onTransition={(target, resolution) =>
                        transitionMutation.mutate({ id: t().id, version: t().version, status: target, resolution })
                      }
                    />

                    <div class="side__fields">
                      <label class="side__field">
                        <span>Priority</span>
                        <select
                          class="select--priority"
                          data-priority={t().priority}
                          value={t().priority}
                          onChange={(event) => savePatch({ priority: event.currentTarget.value as PriorityValue })}
                        >
                          <For each={PRIORITIES}>{(p) => <option value={p}>{label(p)}</option>}</For>
                        </select>
                      </label>
                      <label class="side__field">
                        <span>Type</span>
                        <select
                          value={t().type}
                          onChange={(event) => savePatch({ type: event.currentTarget.value as TicketType })}
                        >
                          <For each={TICKET_TYPES}>{(type) => <option value={type}>{label(type)}</option>}</For>
                        </select>
                      </label>
                      <label class="side__field">
                        <span>Area</span>
                        <select
                          value={t().area?.id ?? ''}
                          onChange={(event) => {
                            const value = event.currentTarget.value;
                            savePatch(value ? { areaId: value } : { clearArea: true });
                          }}
                        >
                          <option value="">No area</option>
                          <For each={areasQuery.data}>
                            {(area) => (
                              <option value={area.id} selected={area.id === t().area?.id}>
                                {area.name}
                              </option>
                            )}
                          </For>
                        </select>
                      </label>
                    </div>

                    {/* Only shown once there is one: "None yet" is a row that never earns its space. */}
                    <Show when={t().prUrl}>
                      {(url) => (
                        <a class="side__pr" href={url()} target="_blank" rel="noreferrer">
                          <Icon name="branch" size={15} />
                          {url().replace(/^https?:\/\/(www\.)?/, '')}
                        </a>
                      )}
                    </Show>

                    <dl class="side__meta">
                      <div>
                        <dt>Opened</dt>
                        <dd title={t().createdAt}>{relativeTime(t().createdAt)}</dd>
                      </div>
                      <div>
                        <dt>Updated</dt>
                        <dd title={t().updatedAt}>{relativeTime(t().updatedAt)}</dd>
                      </div>
                      <Show when={t().completedAt}>
                        {(completed) => (
                          <div>
                            <dt>Finished</dt>
                            <dd title={completed()}>{relativeTime(completed())}</dd>
                          </div>
                        )}
                      </Show>
                    </dl>
                  </aside>
                </article>
              </>
            )}
          </Show>
        </Show>
      </div>
    </>
  );
}
