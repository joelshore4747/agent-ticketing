import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { Ticket } from '../api/types';

const NOW = '2026-09-24T12:00:00Z';

const TICKET: Ticket = {
  id: 't1', key: 'TP-9', number: 9, title: 'Refuse unknown fields', type: 'feature', priority: 'high',
  status: 'open', nextStatuses: ['in_progress', 'closed'], source: 'ai', area: null, version: 4, createdAt: NOW, updatedAt: NOW,
  tags: ['consistency'], intent: 'An agent learns at once when it guesses a field name.', constraints: '',
  nonGoals: 'GraphQL', criteria: [{ id: 'c1', tier: 'gate', ordinal: 1, text: 'just test', tripwire: null }],
  checks: [], approach: 'Disallow unknown fields in the decoder.', workedExample: '', riskAndRollback: '',
  verificationPlan: '', evidenceRefs: [], approachApprovedAt: null, approachApprovedBy: null,
  comments: [], locations: [], pullRequests: [],
  events: [{ id: 'e1', actor: 'claude-code', field: 'intent_proposed', oldValue: '', newValue: 'Agents fail loudly on unknown fields.', createdAt: NOW }],
};

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
    else if (body.query.includes('query NeedsYou')) data = { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };
    else if (body.query.includes('query Ticket(')) data = { ticket: TICKET };
    else if (body.query.includes('mutation SetTicketFoundation')) data = { setTicketFoundation: { id: 't1', version: 5 } };
    else if (body.query.includes('mutation ApproveApproach')) data = { approveApproach: { id: 't1', version: 5 } };
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
  await router.navigate({ to: '/t/$key', params: { key: 'TP-9' } } as never);
}

describe('A ticket page', () => {
  beforeEach(mockApi);

  it('leads with what the human wants and how they will know it is done', async () => {
    await show();
    expect(await screen.findByRole('heading', { name: 'What you want' })).toBeInTheDocument();
    expect(screen.getByText('An agent learns at once when it guesses a field name.')).toBeInTheDocument();
    expect(screen.getByText('just test')).toBeInTheDocument();
    expect(screen.getByText('consistency')).toBeInTheDocument();
  });

  // TP-247, following TP-240's rule 27: a needs-joel ticket's steps are the first thing on the page.
  it('renders the What you do section first when the description has one', async () => {
    TICKET.description = [
      'Some context an agent wrote for another agent.',
      '',
      '## What you do',
      '1. Go to http://localhost:5173 → Needs you.',
      '2. Click Confirm.',
      '',
      '## Why',
      'Because it matters.',
    ].join('\n');
    try {
      await show();
      // The heading also appears a second time, inside the untouched Description panel below —
      // the raw markdown still has its own "## What you do" heading in it. Ours renders first.
      const [heading] = await screen.findAllByRole('heading', { name: 'What you do' });
      const whatYouWant = screen.getByRole('heading', { name: 'What you want' });
      // "first" means before the rest of the page, including the human's own section.
      expect(heading.compareDocumentPosition(whatYouWant) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      const section = heading.closest('section')!;
      // textContent rather than getByText: marked autolinks the bare URL, splitting the line
      // across an <a> and text nodes.
      const steps = within(section).getAllByRole('listitem').map((li) => li.textContent);
      expect(steps[0]).toContain('Go to http://localhost:5173 → Needs you.');
      expect(steps[1]).toContain('Click Confirm.');
      // The section stops at the next heading; it is not the whole description repeated.
      expect(section.textContent).not.toContain('Because it matters.');
    } finally {
      delete TICKET.description;
    }
  });

  it('renders exactly as before when the description has no What you do section', async () => {
    await show();
    await screen.findByRole('heading', { name: 'What you want' });
    expect(screen.queryByRole('heading', { name: 'What you do' })).toBeNull();
  });

  it('applies a proposal through the human-only mutation', async () => {
    await show();
    expect(await screen.findByText('Agents fail loudly on unknown fields.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    await vi.waitFor(() => expect(sent.some((b) => b.query.includes('mutation SetTicketFoundation'))).toBe(true));
    const set = sent.find((b) => b.query.includes('mutation SetTicketFoundation'));
    expect(set?.variables).toMatchObject({ id: 't1', version: 4, input: { intent: 'Agents fail loudly on unknown fields.' } });
  });

  it('shows decisions newest first and flags the one Claude made without evidence', async () => {
    TICKET.decisions = [
      { id: 'd3', kind: 'rejected', madeBy: 'ai', evidenceRefs: [], rationale: 'Too broad for one ticket.', revisitTrigger: null, supersedes: 'd2', citesRevisionId: null, actor: 'claude-code', createdAt: NOW },
      { id: 'd2', kind: 'accepted', madeBy: 'ai', evidenceRefs: [], rationale: 'Looked fine.', revisitTrigger: null, supersedes: 'd1', citesRevisionId: null, actor: 'claude-code', createdAt: NOW },
      { id: 'd1', kind: 'deferred', madeBy: 'human', evidenceRefs: [], rationale: '', revisitTrigger: 'after the harness runs nightly', supersedes: null, citesRevisionId: null, actor: 'joel', createdAt: NOW },
    ];
    try {
      await show();
      expect(await screen.findByRole('heading', { name: 'Decisions' })).toBeInTheDocument();
      const items = screen.getByRole('heading', { name: 'Decisions' }).closest('section')!.querySelectorAll('li.decision');
      expect([...items].map((li) => li.querySelector('.decision__kind')?.textContent)).toEqual(['Rejected', 'Accepted', 'Deferred']);
      // Only the decision still in force is flagged; the replaced uncited one is dimmed instead.
      expect(screen.getAllByText('No evidence cited')).toHaveLength(1);
      expect(items[0].textContent).toContain('No evidence cited');
      expect(items[1].classList.contains('decision--superseded')).toBe(true);
      expect(screen.getByText('after the harness runs nightly')).toBeInTheDocument();
    } finally {
      delete TICKET.decisions;
    }
  });

  it('shows the newest verdict on each criterion, a miss in the urgent hue', async () => {
    TICKET.criteria = [
      { id: 'c1', tier: 'gate', ordinal: 1, text: 'just test', tripwire: null, verdicts: [
        { id: 'v2', verdict: 'fail', reason: 'the web suite is red', evidence: [], judgedBy: 'converger', createdAt: NOW },
        { id: 'v1', verdict: 'pass', reason: '', evidence: [], judgedBy: 'converger', createdAt: NOW },
      ] },
      { id: 'c2', tier: 'measurement', ordinal: 2, text: 'names the field', tripwire: null, verdicts: [
        { id: 'v3', verdict: 'deviation', reason: 'names the JSON path', evidence: [], judgedBy: 'converger', createdAt: NOW },
      ] },
    ];
    try {
      await show();
      const missed = await screen.findByText('Missed');
      expect(missed.className).toContain('chip--urgent');
      expect(screen.getByText('the web suite is red')).toBeInTheDocument();
      expect(screen.queryByText('Met')).toBeNull(); // the older pass no longer counts
      expect(screen.getByText('Met differently')).toBeInTheDocument();
    } finally {
      TICKET.criteria = [{ id: 'c1', tier: 'gate', ordinal: 1, text: 'just test', tripwire: null }];
    }
  });

  // TP-27: the status buttons are the moves the API serves on the ticket, not a table kept here.
  it('offers only the moves the ticket carries', async () => {
    TICKET.nextStatuses = ['closed'];
    try {
      await show();
      expect(await screen.findByRole('button', { name: 'Close without fixing' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Start work' })).toBeNull();
    } finally {
      TICKET.nextStatuses = ['in_progress', 'closed'];
    }
  });

  it('will not approve a feature that has no worked example', async () => {
    await show();
    const approve = await screen.findByRole('button', { name: 'Approve' });
    expect(approve).toBeDisabled();
    expect(screen.getByText(/needs a worked example/)).toBeInTheDocument();
  });
});
