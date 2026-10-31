import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { NeedsYou, Ticket } from '../api/types';

const NOW = new Date().toISOString();

const FINDING: Ticket = {
  id: 't1', key: 'TP-7', number: 7, title: 'Claims can skip the order', type: 'bug', priority: 'high',
  status: 'triage', source: 'ai', area: null, version: 1, createdAt: NOW, updatedAt: NOW,
  summaryForHuman: 'Work can be started out of order',
};

const QUEUE: NeedsYou = {
  total: 4,
  triage: [FINDING],
  checkpoints: [],
  proposals: [{ ticketKey: 'TP-8', ticketTitle: 'Refuse unknown fields', field: 'non_goals', proposed: 'GraphQL', actor: 'claude-code', createdAt: NOW }],
  uncited: [{
    ticketKey: 'TP-9', ticketTitle: 'Rename handlers',
    decision: { id: 'd1', kind: 'rejected', madeBy: 'ai', evidenceRefs: [], rationale: 'Churn', revisitTrigger: null, supersedes: null, citesRevisionId: null, actor: 'claude-code', createdAt: NOW },
  }],
  tripwires: [{ id: 'tw1', testRef: 'api/order_test.go:TestOrder', meaning: 'the batch order holds', confirmedBy: null, confirmedAt: null }],
};

let queue: NeedsYou = QUEUE;
let needsJoel: Ticket[] = [];
let sent: { query: string; variables?: Record<string, unknown> }[] = [];

function mockApi() {
  sent = [];
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query: string; variables?: Record<string, unknown> };
    sent.push(body);
    let data: unknown = {};
    if (body.query.includes('query Projects')) data = { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
    else if (body.query.includes('query Areas')) data = { areas: [] };
    else if (body.query.includes('query Tickets')) data = { tickets: { items: [], total: 0 } };
    else if (body.query.includes('query Batches')) data = { batches: [] };
    else if (body.query.includes('query Guidance')) data = { guidance: [] };
    else if (body.query.includes('query Roadmaps')) data = { roadmaps: [] };
    else if (body.query.includes('query Tags')) data = { tags: [] };
    else if (body.query.includes('query NeedsYou')) data = { needsYou: queue };
    else if (body.query.includes('query NeedsJoel')) data = { tickets: { items: needsJoel } };
    else if (body.query.includes('mutation AcceptTicket')) data = { acceptTicket: { ...FINDING, status: 'open', version: 2 } };
    else if (body.query.includes('mutation TransitionTicket')) data = { transitionTicket: { ...FINDING, status: 'closed', version: 3 } };
    else if (body.query.includes('mutation ConfirmTripwire')) data = { confirmTripwire: { id: 'tw1', confirmedBy: 'joel', confirmedAt: NOW } };
    return new Response(JSON.stringify({ data }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  }) as typeof fetch;
}

async function show() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(() => (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  ));
  await router.navigate({ to: '/needs-you' } as never);
}

describe('Needs you', () => {
  beforeEach(() => {
    queue = QUEUE;
    needsJoel = [];
    mockApi();
  });

  it('lists what waits on the human, grouped, each linked to where it is decided', async () => {
    await show();
    expect(await screen.findByRole('heading', { name: 'Findings to accept or dismiss' })).toBeInTheDocument();
    expect(screen.getByText('Work can be started out of order').closest('a')?.getAttribute('href')).toContain('/t/TP-7');
    expect(screen.getByText('Non goals: GraphQL').closest('a')?.getAttribute('href')).toContain('/t/TP-8');
    expect(screen.getByText('Rejected: Churn')).toBeInTheDocument();
    // An empty group is not shown at all.
    expect(screen.queryByRole('heading', { name: 'Checkpoints to sign' })).toBeNull();
    // The rail carries the total in the attention colour.
    const link = screen.getByRole('link', { name: /Needs you/ });
    expect(link.querySelector('.rail__count--attention')?.textContent).toBe('4');
  });

  // TP-247, following TP-240's rule 27: a needs-joel finding carries its steps right into the queue.
  it("shows a needs-joel finding's What you do steps under it", async () => {
    queue = {
      ...QUEUE,
      triage: [{
        ...FINDING,
        key: 'TP-11',
        tags: ['needs-joel'],
        description: '**What you do:**\n1. Open the batch page.\n2. Read the size note, then click Accept.',
      }],
    };
    mockApi();
    await show();
    const row = (await screen.findByText('Claims can skip the order')).closest('li')!;
    expect(within(row).getByText('Open the batch page.')).toBeInTheDocument();
    expect(within(row).getByText('Read the size note, then click Accept.')).toBeInTheDocument();
  });

  it('shows no steps under a finding that has no needs-joel tag', async () => {
    await show();
    const row = (await screen.findByText('Work can be started out of order')).closest('li')!;
    expect(within(row).queryByRole('listitem')).toBeNull();
  });

  it('confirms a tripwire in place, over the human-only mutation', async () => {
    await show();
    fireEvent.click(await screen.findByRole('button', { name: /Confirm/ }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation ConfirmTripwire'))).toBe(true));
    expect(sent.find((b) => b.query.includes('mutation ConfirmTripwire'))?.variables).toMatchObject({ id: 'tw1' });
  });

  // Joel, 2026-10-29: tripwires were "really hard to distinguish" under a long findings list.
  it('puts the to-do list first, before the findings, with the tripwire in its own box', async () => {
    await show();
    const todo = await screen.findByRole('heading', { name: 'Your to-do list' });
    const findings = screen.getByRole('heading', { name: 'Findings to accept or dismiss' });
    expect(todo.compareDocumentPosition(findings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const box = screen.getByText('Confirm a safety test').closest('li')!;
    expect(within(box).getByText('Protects: the batch order holds')).toBeInTheDocument();
    expect(within(box).getByText('api/order_test.go:TestOrder')).toBeInTheDocument();
  });

  it('folds the findings so the to-do list is not buried', async () => {
    await show();
    const heading = await screen.findByRole('heading', { name: 'Findings to accept or dismiss' });
    expect(heading.closest('details')?.open).toBe(false);
  });

  it('lists an accepted needs-joel ticket too, and ticks it off only after a second click', async () => {
    needsJoel = [{
      ...FINDING, id: 't56', key: 'FS-56', status: 'open', version: 4, tags: ['needs-joel'],
      title: 'Joel: Forward Email credentials and one real send',
      description: '## What you do\n1. Create the account.\n2. Send one real email.',
    }];
    mockApi();
    await show();
    const box = (await screen.findByText('Forward Email credentials and one real send')).closest('li')!;
    expect(within(box).getByText('Create the account.')).toBeInTheDocument();
    fireEvent.click(within(box).getByRole('button', { name: /Done, tick off/ }));
    expect(sent.some((b) => b.query.includes('mutation TransitionTicket'))).toBe(false);
    fireEvent.click(await within(box).findByRole('button', { name: 'Yes, it is done' }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation TransitionTicket'))).toBe(true));
    // An open ticket closes directly; nothing is accepted first.
    expect(sent.some((b) => b.query.includes('mutation AcceptTicket'))).toBe(false);
    expect(sent.find((b) => b.query.includes('mutation TransitionTicket'))?.variables).toMatchObject({
      id: 't56', version: 4, status: 'closed',
    });
  });

  it('accepts a needs-joel ticket still in triage before closing it, so it is not recorded as dismissed', async () => {
    queue = { ...QUEUE, triage: [{ ...FINDING, tags: ['needs-joel'], description: '**What you do:**\n1. Do it.' }] };
    mockApi();
    await show();
    const box = (await screen.findByText(FINDING.title)).closest('li')!;
    fireEvent.click(within(box).getByRole('button', { name: /Done, tick off/ }));
    fireEvent.click(await within(box).findByRole('button', { name: 'Yes, it is done' }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation TransitionTicket'))).toBe(true));
    const accept = sent.findIndex((b) => b.query.includes('mutation AcceptTicket'));
    const close = sent.findIndex((b) => b.query.includes('mutation TransitionTicket'));
    expect(accept).toBeGreaterThan(-1);
    expect(accept).toBeLessThan(close);
    expect(sent[close].variables).toMatchObject({ version: 2, status: 'closed' });
  });

  it('says so when nothing is waiting', async () => {
    queue = { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] };
    mockApi();
    await show();
    expect(await screen.findByText('Nothing is waiting on you.')).toBeInTheDocument();
  });
});
