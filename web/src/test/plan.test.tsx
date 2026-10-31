import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { Batch, Roadmap, Ticket } from '../api/types';

const NOW = new Date().toISOString();

function ticket(n: number, over: Partial<Ticket>): Ticket {
  return {
    id: `t${n}`,
    key: `TP-${n}`,
    number: n,
    title: `Ticket ${n}`,
    type: 'feature',
    priority: 'high',
    status: 'open',
    source: 'ai',
    area: null,
    claimedBy: null,
    claimedAt: null,
    batchPosition: n,
    tags: [],
    version: 1,
    createdAt: NOW,
    updatedAt: NOW,
    ...over,
  };
}

const PLAN: Batch = {
  id: 'b1',
  kind: 'plan',
  actor: 'claude-code',
  title: 'Ordering',
  what: 'Batches get an order.',
  how: 'A position column and a rule on claim.',
  brief: 'So ticket 5 cannot be done before tickets 1 to 4.',
  gitRef: null,
  summary: null,
  findingsCount: 0,
  startedAt: NOW,
  finishedAt: null,
  roadmapId: 'r1',
  sequence: 1,
  checkpointDecision: null,
  checkpointNote: null,
  checkpointBy: null,
  checkpointAt: null,
  guidance: [],
  tickets: [
    ticket(1, { status: 'in_progress', tags: ['architecture'] }),
    ticket(2, { status: 'triage' }),
    ticket(3, { type: 'eval', title: 'Eval: Ordering', status: 'triage' }),
  ],
  progress: { next: 'TP-1', lockedBy: null, blockedBy: [{ key: 'TP-2', behind: 'TP-1' }, { key: 'TP-3', behind: 'TP-1' }] },
};

const FINISHED: Batch = { ...PLAN, finishedAt: NOW, tickets: [], progress: { next: null, lockedBy: null, blockedBy: [] } };

const ROADMAP: Roadmap = {
  id: 'r1',
  title: 'Ticket manager evolution',
  spec: 'The master brief.',
  createdAt: NOW,
  archivedAt: null,
  batches: [
    { batch: FINISHED, state: 'awaiting_checkpoint', work: 2, workDone: 2 },
    { batch: { ...PLAN, id: 'b2', title: 'Ticket sections', sequence: 2 }, state: 'locked', work: 3, workDone: 0 },
  ],
};

let sent: { query: string; variables?: Record<string, unknown> }[] = [];
let batch: Batch = PLAN;

function mockApi() {
  sent = [];
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query: string; variables?: Record<string, unknown> };
    sent.push(body);
    let data: unknown = {};
    if (body.query.includes('query Projects')) data = { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
    else if (body.query.includes('query Areas')) data = { areas: [] };
    else if (body.query.includes('query Tickets')) data = { tickets: { items: [], total: 0 } };
    else if (body.query.includes('query Batches')) data = { batches: [batch] };
    else if (body.query.includes('query Guidance')) data = { guidance: [] };
    else if (body.query.includes('query Roadmaps')) data = { roadmaps: [ROADMAP] };
    else if (body.query.includes('query Tags')) data = { tags: [] };
    else if (body.query.includes('query NeedsYou')) data = { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };
    else if (body.query.includes('query Batch(')) data = { batch };
    else if (body.query.includes('mutation AcceptBatch')) data = { acceptBatch: { id: batch.id } };
    else if (body.query.includes('mutation CheckpointBatch'))
      data = { checkpointBatch: { id: 'b1', checkpointDecision: 'adjust' } };
    return new Response(JSON.stringify({ data }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  }) as typeof fetch;
}

async function show(to: string, params?: Record<string, string>) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(() => (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  ));
  await router.navigate({ to, params } as never);
}

describe('A plan batch', () => {
  beforeEach(() => {
    batch = PLAN;
    mockApi();
  });

  it('reads as what, how and why', async () => {
    await show('/b/$batchId', { batchId: 'b1' });
    expect(await screen.findByRole('heading', { name: 'What' })).toBeInTheDocument();
    expect(screen.getByText('Batches get an order.')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'How' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Why' })).toBeInTheDocument();
  });

  it('numbers its tickets and says which is next and what the rest wait on', async () => {
    await show('/b/$batchId', { batchId: 'b1' });
    expect(await screen.findByText('Next')).toBeInTheDocument();
    expect(screen.getAllByText('Waiting on TP-1')).toHaveLength(2);
    expect(screen.getByText('Eval gate')).toBeInTheDocument();
    expect(screen.getByLabelText('Position 3')).toHaveTextContent('3');
    expect(screen.getByText('architecture')).toBeInTheDocument();
  });

  it('accepts every triage ticket in one move', async () => {
    await show('/b/$batchId', { batchId: 'b1' });
    fireEvent.click(await screen.findByRole('button', { name: 'Accept batch' }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation AcceptBatch'))).toBe(true));
    const accept = sent.find((b) => b.query.includes('mutation AcceptBatch'));
    expect(accept?.variables?.id).toBe('b1');
  });

  it('asks for a checkpoint only once it has finished', async () => {
    await show('/b/$batchId', { batchId: 'b1' });
    await screen.findByText('Next');
    expect(screen.queryByRole('heading', { name: 'Checkpoint' })).toBeNull();
  });
});

describe('The roadmap', () => {
  beforeEach(() => {
    batch = PLAN;
    mockApi();
  });

  it('shows each batch in order with where it stands, and takes the checkpoint', async () => {
    await show('/roadmaps');
    expect(await screen.findByText('Needs your checkpoint')).toBeInTheDocument();
    expect(screen.getByText('Locked')).toBeInTheDocument();
    expect(screen.getByText('2/2 done')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText(/does it change the plan/), { target: { value: 'swap 3 and 4' } });
    // Solid 2 applies signal writes on the next microtask; a person cannot click inside the same one.
    await Promise.resolve();
    fireEvent.click(screen.getByRole('button', { name: 'Adjust' }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation CheckpointBatch'))).toBe(true));
    const cp = sent.find((b) => b.query.includes('mutation CheckpointBatch'));
    expect(cp?.variables).toMatchObject({ id: 'b1', decision: 'adjust', note: 'swap 3 and 4' });
  });
});
