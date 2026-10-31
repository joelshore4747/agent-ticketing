import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { Batch, GuidanceDoc, Ticket } from '../api/types';

const NOW = new Date().toISOString();

const TICKET: Ticket = {
  id: 't1',
  key: 'TP-42',
  number: 42,
  title: 'Filed by the audit',
  type: 'bug',
  priority: 'high',
  status: 'triage',
  source: 'ai',
  area: null,
  claimedBy: null,
  claimedAt: null,
  version: 1,
  createdAt: NOW,
  updatedAt: NOW,
};

const STALE_DOC: GuidanceDoc = {
  id: 'g1',
  slug: 'go-service-layer',
  title: 'The Go service layer',
  kind: 'convention',
  body: 'Query with pgx directly. There is no ORM here.',
  revision: 3,
  inline: true,
  weight: 0,
  sourcePaths: ['api/internal/service/**'],
  sourceCommit: 'abc1234',
  reviewedAt: null,
  reviewedBy: null,
  stale: true,
  staleReason: 'api/src/service.rs no longer exists.',
  driftReportedAt: null,
  updatedAt: NOW,
};

const BATCH: Batch = {
  id: 'b1',
  kind: 'audit',
  actor: 'claude-code',
  title: 'Post-port audit',
  brief: 'The Rust port left three documents describing files that are gone.',
  gitRef: 'main',
  summary: null,
  findingsCount: 3,
  startedAt: NOW,
  finishedAt: null,
  what: '',
  how: '',
  roadmapId: null,
  sequence: null,
  checkpointDecision: null,
  checkpointNote: null,
  checkpointBy: null,
  checkpointAt: null,
  guidance: [STALE_DOC],
  tickets: [TICKET],
};

function mockApi() {
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query: string };

    let data: unknown = {};
    if (body.query.includes('query Projects')) data = { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
    else if (body.query.includes('query Areas')) data = { areas: [] };
    else if (body.query.includes('query Tickets')) data = { tickets: { items: [], total: 0 } };
    else if (body.query.includes('query Batches')) data = { batches: [BATCH] };
    else if (body.query.includes('query Guidance')) data = { guidance: BATCH.guidance ?? [] };
    else if (body.query.includes('query Roadmaps')) data = { roadmaps: [] };
    else if (body.query.includes('query Tags')) data = { tags: [] };
    else if (body.query.includes('query NeedsYou')) data = { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };
    else if (body.query.includes('query Batch(')) data = { batch: BATCH };

    return new Response(JSON.stringify({ data }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }) as typeof fetch;
}

async function renderBatch() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(() => (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  ));
  await router.navigate({ to: '/b/$batchId', params: { batchId: 'b1' } });
}

describe('Batch page', () => {
  beforeEach(() => {
    mockApi();
  });

  it('says why the work was done and what came out of it', async () => {
    await renderBatch();

    expect(await screen.findByRole('heading', { name: 'Post-port audit' })).toBeInTheDocument();
    expect(screen.getByText(/three documents describing files that are gone/)).toBeInTheDocument();
    expect(screen.getByText('Filed by the audit')).toBeInTheDocument();
  });

  // findings_count counts findings reported; a repeat is deduped onto the ticket that already
  // exists and still bumps it. The page says so rather than letting the two numbers disagree.
  it('explains the gap between findings reported and tickets filed', async () => {
    await renderBatch();

    expect(await screen.findByText(/3 findings were reported into this batch/)).toBeInTheDocument();
  });

  it('serves a stale document with its body, marked rather than withheld', async () => {
    await renderBatch();

    expect(await screen.findByText('The Go service layer')).toBeInTheDocument();
    expect(screen.getByText('go-service-layer r3')).toBeInTheDocument();
    expect(screen.getByText(/api\/src\/service\.rs no longer exists/)).toBeInTheDocument();
    expect(screen.getByText(/Query with pgx directly/)).toBeInTheDocument();
  });

  it('leads a finished batch with its digest instead of repeating what, how and why', async () => {
    Object.assign(BATCH, {
      finishedAt: NOW, what: 'An order for the work', how: 'A position column',
      digest: '## What\n\nAn order for the work\n\n## Deferred\n\n- **TP-9** deferred by Claude Code — revisit when TP-37 lands',
      digestAt: NOW,
    });
    try {
      await renderBatch();
      expect(await screen.findByRole('heading', { name: 'Digest' })).toBeInTheDocument();
      // The digest's sections sit a level under the panel heading, and the separate panels are gone.
      expect(screen.getByRole('heading', { name: 'Deferred', level: 3 })).toBeInTheDocument();
      expect(screen.getByText(/revisit when TP-37 lands/)).toBeInTheDocument();
      expect(screen.queryByRole('heading', { name: 'How', level: 2 })).toBeNull();
      expect(screen.queryByText('A position column')).toBeNull();
    } finally {
      Object.assign(BATCH, { finishedAt: null, what: '', how: '', digest: null, digestAt: null });
    }
  });

  it('shows the plain-English line on a card, keeping the title a hover away', async () => {
    TICKET.summaryForHuman = 'Claims now follow the batch order';
    try {
      await renderBatch();
      const title = await screen.findByText('Claims now follow the batch order');
      expect(title.getAttribute('title')).toBe('Filed by the audit');
    } finally {
      delete TICKET.summaryForHuman;
    }
  });

  // TP-109: the key goals a batch serves, and its before and after project reviews (goal 2).
  it('shows the goals its tickets serve and the project reviews', async () => {
    TICKET.goals = [2, 3];
    Object.assign(BATCH, {
      reviewsRequired: true,
      reviews: [{ id: 'r1', phase: 'before', commit: 'abc1234def', body: '## Structure\n\nTwo packages; the digest is long.', actor: 'architecture-scout', createdAt: NOW }],
    });
    try {
      await renderBatch();
      expect(await screen.findByRole('heading', { name: 'Key goals it serves' })).toBeInTheDocument();
      expect(screen.getByText('Goal 2 · 1 ticket')).toBeInTheDocument();
      expect(screen.getAllByText('Goal 3').length).toBeGreaterThan(0); // the card's chip
      expect(screen.getByRole('heading', { name: 'Project reviews' })).toBeInTheDocument();
      expect(screen.getByText('Two packages; the digest is long.')).toBeInTheDocument();
      expect(screen.getByText('abc1234')).toBeInTheDocument();
      expect(screen.getByText(/the batch cannot finish without it/)).toBeInTheDocument();
    } finally {
      delete TICKET.goals;
      Object.assign(BATCH, { reviewsRequired: false, reviews: [] });
    }
  });

  // The cards here are a record of what the batch produced, not a board: there is nowhere to drop
  // one, and accepting a finding is a decision that belongs on the page that queues it.
  it('lists its tickets as read-only cards', async () => {
    await renderBatch();

    const card = (await screen.findByText('Filed by the audit')).closest('.card');
    expect(card).not.toBeNull();
    expect(card?.getAttribute('draggable')).toBeNull();
    expect(within(card as HTMLElement).queryByRole('button')).toBeNull();
  });
});
