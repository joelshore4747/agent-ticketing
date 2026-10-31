import { For, Show } from 'solid-js';
import { Link, useParams } from '@tanstack/solid-router';
import { useBatchQuery } from '../api/hooks';
import { BoardCard } from '../components/BoardCard';
import { CheckpointControl } from '../components/CheckpointControl';
import { GuidanceDocument } from '../components/GuidanceDocument';
import { PlanList } from '../components/PlanList';
import { Icon } from '../components/Icon';
import { SkeletonLines } from '../components/SkeletonLines';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { label } from '../lib/labels';
import { renderMarkdown } from '../lib/markdown';
import { relativeTime } from '../lib/time';
import type { Ticket } from '../api/types';

/** One batch: why the work was done, what was handed to whoever did it, and what came out.
 *
 *  The guidance is the point of the page. It is the only place a human can read the documents an
 *  agent was actually served — the same bodies, in the same order — which is what makes a claim
 *  something you can check rather than something you have to trust. */
/** How many of the batch's tickets name each key goal, most-served first. */
function goalCounts(tickets: Ticket[]): [number, number][] {
  const counts = new Map<number, number>();
  for (const t of tickets) for (const g of t.goals ?? []) counts.set(g, (counts.get(g) ?? 0) + 1);
  return [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0] - b[0]);
}

export function BatchDetail() {
  const params = useParams({ strict: false });
  const id = () => (params() as { batchId?: string }).batchId ?? '';
  const batchQuery = useBatchQuery(id);

  const count = (n: number, one: string, many: string) => (n === 1 ? `1 ${one}` : `${n} ${many}`);

  return (
    <>
      <Topbar
        leading={
          <Link to="/batches" search={keepProject} class="btn btn--primary">
            <Icon name="back" size={17} />
            Batches
          </Link>
        }
      />

      <div class="view view--narrow">
        <Show when={!batchQuery.isPending} fallback={<SkeletonLines rows={8} />}>
          <Show when={batchQuery.data} fallback={<p class="empty-state">There is no such batch.</p>}>
            {(batch) => (
              <>
                <nav class="crumbs" aria-label="Breadcrumb">
                  <Link to="/" search={keepProject}>
                    Tickets
                  </Link>
                  <span aria-hidden="true">›</span>
                  <Link to="/batches" search={keepProject}>
                    Batches
                  </Link>
                  <span aria-hidden="true">›</span>
                  <span>{batch().title || label(batch().kind)}</span>
                </nav>

                <article class="batch">
                  <header class="batch__head">
                    <p class="eyebrow">{label(batch().kind)}</p>
                    <h1>{batch().title || `Untitled ${batch().kind}`}</h1>
                    <div class="batch__tags">
                      <Show when={batch().finishedAt} fallback={<span class="chip chip--live">In flight</span>}>
                        {(at) => (
                          <span class="chip chip--fixed">
                            <Icon name="check" size={11} />
                            Finished {relativeTime(at())}
                          </span>
                        )}
                      </Show>
                      <Show when={batch().sequence}>
                        {(n) => <span class="chip chip--type">Roadmap batch {n()}</span>}
                      </Show>
                      <span class="chip chip--source">{batch().actor}</span>
                      <Show when={batch().gitRef}>{(ref) => <span class="key">{ref()}</span>}</Show>
                      <span class="meta">started {relativeTime(batch().startedAt)}</span>
                    </div>
                  </header>

                  {/* A finished batch leads with its digest: what/how/why plus what came of it,
                      computed from the record, so it replaces the three panels below rather than
                      repeating them. Its sections are demoted a level to sit under the panel's own. */}
                  <Show when={batch().digest}>
                    {(digest) => (
                      <section class="panel" aria-labelledby="digest-head">
                        <div class="panel__head">
                          <h2 id="digest-head">Digest</h2>
                          <Show when={batch().digestAt}>
                            {(at) => <span class="meta">written {relativeTime(at())}</span>}
                          </Show>
                        </div>
                        <div class="markdown-body" innerHTML={renderMarkdown(digest().replace(/^## /gm, '### '))} />
                      </section>
                    )}
                  </Show>

                  {/* The human read: what the batch builds, how, and why. Findings batches only
                      carry a why, so the other two panels appear when there is something in them. */}
                  <Show when={!batch().digest && batch().what}>
                    {(what) => (
                      <section class="panel">
                        <div class="panel__head">
                          <h2>What</h2>
                        </div>
                        <div class="markdown-body" innerHTML={renderMarkdown(what())} />
                      </section>
                    )}
                  </Show>
                  <Show when={!batch().digest && batch().how}>
                    {(how) => (
                      <section class="panel">
                        <div class="panel__head">
                          <h2>How</h2>
                        </div>
                        <div class="markdown-body" innerHTML={renderMarkdown(how())} />
                      </section>
                    )}
                  </Show>
                  <Show when={!batch().digest}>
                    <section class="panel">
                      <div class="panel__head">
                        <h2>{batch().what ? 'Why' : 'Why this work'}</h2>
                      </div>
                      <Show
                        when={batch().brief}
                        fallback={<p class="meta">No brief was written, so its tickets carry no reason with them.</p>}
                      >
                        <div class="markdown-body" innerHTML={renderMarkdown(batch().brief)} />
                      </Show>
                    </section>
                  </Show>

                  {/* The summary is written when the batch finishes, once what it did is known
                      rather than guessed at the start — so an unfinished batch has no panel. */}
                  <Show when={batch().summary}>
                    {(summary) => (
                      <section class="panel">
                        <div class="panel__head">
                          <h2>What it did</h2>
                        </div>
                        <div class="markdown-body" innerHTML={renderMarkdown(summary())} />
                      </section>
                    )}
                  </Show>

                  <Show when={goalCounts(batch().tickets ?? []).length > 0}>
                    <section class="panel">
                      <div class="panel__head">
                        <h2>Key goals it serves</h2>
                      </div>
                      <div class="batch__tags">
                        <For each={goalCounts(batch().tickets ?? [])}>
                          {([goal, n]) => (
                            <span class="chip chip--tag">
                              Goal {goal} · {count(n, 'ticket', 'tickets')}
                            </span>
                          )}
                        </For>
                      </div>
                    </section>
                  </Show>

                  {/* Goal 2: the project reviewed before the batch and after it. The after-review is
                      the memory the next batch's review starts from. */}
                  <Show when={batch().reviewsRequired || (batch().reviews?.length ?? 0) > 0}>
                    <section class="panel" aria-labelledby="reviews-head">
                      <div class="panel__head">
                        <h2 id="reviews-head">Project reviews</h2>
                      </div>
                      <For each={['before', 'after'] as const}>
                        {(phase) => {
                          const review = () => [...(batch().reviews ?? [])].reverse().find((r) => r.phase === phase);
                          return (
                            <div class="review">
                              <h3 class="foundation__sub">{phase === 'before' ? 'Before the batch' : 'After the batch'}</h3>
                              <Show
                                when={review()}
                                fallback={
                                  <p class="meta">
                                    {phase === 'before'
                                      ? 'Not reviewed yet: its tickets wait for this.'
                                      : 'Not reviewed yet: the batch cannot finish without it.'}
                                  </p>
                                }
                              >
                                {(r) => (
                                  <>
                                    <p class="meta">
                                      at <span class="key">{r().commit.slice(0, 7)}</span> by {r().actor},{' '}
                                      {relativeTime(r().createdAt)}
                                    </p>
                                    <div class="markdown-body" innerHTML={renderMarkdown(r().body.replace(/^#{1,3} /gm, '#### '))} />
                                  </>
                                )}
                              </Show>
                            </div>
                          );
                        }}
                      </For>
                    </section>
                  </Show>

                  <section class="panel">
                    <div class="panel__head">
                      <h2>Guidance</h2>
                      <span class="meta">{count(batch().guidance?.length ?? 0, 'document', 'documents')}</span>
                    </div>
                    <Show
                      when={(batch().guidance?.length ?? 0) > 0}
                      fallback={
                        <p class="meta">
                          Nothing is attached, so a ticket from this batch is claimed with the ticket alone.
                        </p>
                      }
                    >
                      <For each={batch().guidance}>{(doc) => <GuidanceDocument doc={doc} />}</For>
                    </Show>
                  </section>

                  <section class="panel">
                    <div class="panel__head">
                      <h2>Tickets</h2>
                      <span class="meta">{count(batch().tickets?.length ?? 0, 'ticket', 'tickets')}</span>
                    </div>
                    {/* findingsCount counts findings reported, not tickets filed: a repeat is deduped
                        onto the ticket that already exists and still bumps the count. Saying so is
                        cheaper than explaining the gap every time somebody notices it. */}
                    <Show when={batch().findingsCount !== (batch().tickets?.length ?? 0)}>
                      <p class="meta batch__note">
                        {count(batch().findingsCount, 'finding was', 'findings were')} reported into this batch;
                        repeats were deduped onto tickets that already existed.
                      </p>
                    </Show>
                    <Show
                      when={(batch().tickets?.length ?? 0) > 0}
                      fallback={<p class="meta">This batch has not filed anything.</p>}
                    >
                      <Show
                        when={batch().kind === 'plan'}
                        fallback={
                          <ul class="results__list">
                            <For each={batch().tickets}>
                              {(ticket) => <BoardCard ticket={ticket} showSource showArea />}
                            </For>
                          </ul>
                        }
                      >
                        <PlanList batch={batch()} tickets={batch().tickets ?? []} />
                      </Show>
                    </Show>
                  </section>

                  {/* The checkpoint is decided on the eval ticket's evidence, so it waits for the
                      batch to finish; a batch outside any roadmap has nothing after it to open. */}
                  <Show when={batch().kind === 'plan' && batch().finishedAt && batch().roadmapId}>
                    <section class="panel" id="checkpoint">
                      <div class="panel__head">
                        <h2>Checkpoint</h2>
                      </div>
                      <CheckpointControl batch={batch()} />
                    </section>
                  </Show>
                </article>
              </>
            )}
          </Show>
        </Show>
      </div>
    </>
  );
}
