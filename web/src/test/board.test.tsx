import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { Status, Ticket } from '../api/types';

// What the API serves as nextStatuses for each status (rule 4); the board reads it off the ticket.
const NEXT: Record<Status, Status[]> = {
  triage: ['open', 'closed'], open: ['in_progress', 'closed'], in_progress: ['open', 'done', 'closed'],
  done: ['open'], closed: ['open'],
};

function makeTicket(overrides: Partial<Ticket>): Ticket {
  const now = new Date().toISOString();
  return {
    id: '1',
    key: 'TP-1',
    number: 1,
    title: 'Default title',
    type: 'bug',
    priority: 'medium',
    status: 'open',
    source: 'human',
    area: null,
    claimedBy: null,
    claimedAt: null,
    version: 3,
    createdAt: now,
    updatedAt: now,
    ...overrides,
    nextStatuses: overrides.nextStatuses ?? NEXT[overrides.status ?? 'open'],
  };
}

interface GraphQLBody {
  query: string;
  variables?: { project?: string; filter?: { status?: Status[] }; id?: string; version?: number };
}

const BY_STATUS: Partial<Record<Status, Ticket[]>> = {
  triage: [makeTicket({ id: 'a', key: 'TP-10', title: 'Unreviewed finding', source: 'ai', status: 'triage' })],
  open: [makeTicket({ id: 'b', key: 'TP-11', title: 'Queued chore' })],
  in_progress: [makeTicket({ id: 'c', key: 'TP-12', title: 'Being worked on now', status: 'in_progress' })],
  done: [makeTicket({ id: 'd', key: 'TP-13', title: 'Already fixed', status: 'done', resolution: 'Fixed it' })],
};

/** Tickets that exist beyond the page the board loaded, per status. */
const EXTRA_TOTAL: Partial<Record<Status, number>> = {};

/** Every GraphQL request the board makes, answered from BY_STATUS. Returns the bodies it saw so a
 *  test can assert what a gesture actually sent. */
function mockApi(): GraphQLBody[] {
  const sent: GraphQLBody[] = [];
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as GraphQLBody;
    sent.push(body);

    let data: unknown = {};
    if (body.query.includes('query Projects')) {
      data = { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
    } else if (body.query.includes('query Areas')) {
      data = { areas: [] };
    } else if (body.query.includes('query Batches')) {
      data = { batches: [] };
    } else if (body.query.includes('query Guidance')) {
      data = { guidance: [] };
    } else if (body.query.includes('query Roadmaps')) {
      data = { roadmaps: [] };
    } else if (body.query.includes('query Tags')) {
      data = { tags: [] };
    } else if (body.query.includes('query NeedsYou')) {
      data = { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };
    } else if (body.query.includes('query Tickets')) {
      const items = BY_STATUS[body.variables?.filter?.status?.[0] ?? 'open'] ?? [];
      const status = body.variables?.filter?.status?.[0] ?? 'open';
      data = { tickets: { items, total: items.length + (EXTRA_TOTAL[status] ?? 0) } };
    } else {
      data = { acceptTicket: makeTicket({ status: 'open' }) };
    }

    return new Response(JSON.stringify({ data }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }) as typeof fetch;
  return sent;
}

function renderApp() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(() => (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  ));
}

describe('Board', () => {
  beforeEach(() => {
    mockApi();
  });

  it('puts each ticket in the column for its status', async () => {
    renderApp();

    const inProgress = await screen.findByRole('region', { name: 'In progress' });
    expect(await within(inProgress).findByText('Being worked on now')).toBeInTheDocument();

    const todo = screen.getByRole('region', { name: 'To do' });
    expect(within(todo).getByText('Queued chore')).toBeInTheDocument();

    const inbox = screen.getByRole('region', { name: 'Inbox' });
    expect(within(inbox).getByText('Unreviewed finding')).toBeInTheDocument();

    const done = screen.getByRole('region', { name: 'Done' });
    expect(within(done).getByText('Already fixed')).toBeInTheDocument();
  });

  // TP-28: several findings accepted or dismissed together, each through the human's own mutation.
  it('accepts or dismisses picked findings together', async () => {
    const second = makeTicket({ id: 'e', key: 'TP-14', title: 'Another finding', source: 'ai', status: 'triage', version: 7 });
    BY_STATUS.triage = [...(BY_STATUS.triage ?? []), second];
    try {
      const sent = mockApi();
      renderApp();
      const inbox = await screen.findByRole('region', { name: 'Inbox' });
      fireEvent.click(await within(inbox).findByRole('checkbox', { name: 'Select TP-14' }));
      await Promise.resolve();
      fireEvent.click(within(inbox).getByRole('button', { name: 'Accept 1' }));
      await vi.waitFor(() => expect(sent.filter((b) => b.query.includes('mutation AcceptTicket'))).toHaveLength(1));
      expect(sent.find((b) => b.query.includes('mutation AcceptTicket'))?.variables).toMatchObject({ id: 'e', version: 7 });

      // The run clears its selection when every request has come back.
      await vi.waitFor(() => expect(within(inbox).queryByRole('button', { name: 'Accept 1' })).toBeNull());
      fireEvent.click(within(inbox).getByRole('checkbox', { name: 'Select every finding in the inbox' }));
      await Promise.resolve();
      fireEvent.click(within(inbox).getByRole('button', { name: 'Dismiss 2' }));
      await vi.waitFor(() => expect(sent.filter((b) => b.query.includes('mutation DismissTicket'))).toHaveLength(2));
    } finally {
      BY_STATUS.triage = BY_STATUS.triage?.filter((t) => t.id !== 'e');
    }
  });

  // TP-5: "?" lists the shortcuts, Esc closes the list, and typing "?" into a field is just typing.
  it('shows the keyboard shortcuts on ?', async () => {
    renderApp();
    await screen.findByRole('region', { name: 'To do' });
    fireEvent.keyDown(window, { key: '?' });
    const dialog = await screen.findByRole('dialog', { name: 'Keyboard shortcuts' });
    expect(within(dialog).getByText('Search tickets (jumps to the board first)')).toBeInTheDocument();
    fireEvent.keyDown(dialog, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog', { name: 'Keyboard shortcuts' })).toBeNull());

    const search = screen.getByRole('searchbox');
    fireEvent.keyDown(search, { key: '?' });
    await Promise.resolve();
    expect(screen.queryByRole('dialog', { name: 'Keyboard shortcuts' })).toBeNull();
  });

  // TP-18: a column cut at its page size says so, instead of looking complete.
  it('says when a column holds fewer cards than exist', async () => {
    EXTRA_TOTAL.open = 140;
    try {
      renderApp();
      const todo = await screen.findByRole('region', { name: 'To do' });
      expect(await within(todo).findByText('Showing 1 of 141. Search or filter to find the rest.')).toBeInTheDocument();
      expect(within(screen.getByRole('region', { name: 'Done' })).queryByText(/Showing/)).toBeNull();
    } finally {
      delete EXTRA_TOTAL.open;
    }
  });

  it('accepts a finding from the inbox with the ticket version', async () => {
    const sent = mockApi();
    renderApp();

    const inbox = await screen.findByRole('region', { name: 'Inbox' });
    fireEvent.click(within(inbox).getByRole('button', { name: 'Accept' }));

    await vi.waitFor(() => {
      const accept = sent.find((body) => body.query.includes('mutation AcceptTicket'));
      expect(accept?.variables).toMatchObject({ id: 'a', version: 3 });
    });
  });

  // Work is started in the project, not here; the board finds out. The inbox is the one place a
  // card still carries buttons, because accepting or dismissing a finding is nobody else's call.
  it('offers no way to start or finish work outside the inbox', async () => {
    renderApp();

    const todo = await screen.findByRole('region', { name: 'To do' });
    expect(within(todo).queryByRole('button')).toBeNull();

    const inProgress = screen.getByRole('region', { name: 'In progress' });
    expect(within(inProgress).queryByRole('button')).toBeNull();

    const inbox = screen.getByRole('region', { name: 'Inbox' });
    expect(within(inbox).getByRole('button', { name: 'Accept' })).toBeInTheDocument();
    expect(within(inbox).getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('tags every card with its priority and carries no clock', async () => {
    renderApp();

    const todo = await screen.findByRole('region', { name: 'To do' });
    const card = within(todo).getByText('Queued chore').closest('.card');
    expect(card?.querySelector('.chip--priority')?.textContent).toBe('Medium');
    expect(card?.querySelector('.card__time')).toBeNull();
  });
});
