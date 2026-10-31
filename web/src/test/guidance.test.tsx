import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import type { GuidanceDoc } from '../api/types';

const NOW = new Date().toISOString();

function doc(overrides: Partial<GuidanceDoc>): GuidanceDoc {
  return {
    id: 'g',
    slug: 'slug',
    title: 'Title',
    kind: 'convention',
    body: 'The body.',
    revision: 1,
    inline: true,
    weight: 0,
    sourcePaths: [],
    sourceCommit: null,
    reviewedAt: null,
    reviewedBy: null,
    stale: false,
    staleReason: null,
    driftReportedAt: null,
    updatedAt: NOW,
    ...overrides,
  };
}

const CORPUS: GuidanceDoc[] = [
  doc({ id: 'g1', slug: 'before-you-finish', title: 'Before you say it is done', kind: 'checklist' }),
  doc({ id: 'g2', slug: 'go-service-layer', title: 'How the Go API is written' }),
  doc({
    id: 'g3',
    slug: 'working-a-ticket',
    title: 'Working a ticket',
    kind: 'workflow',
    stale: true,
    staleReason: 'tickets.go changed at 3213f6e9 and nobody has re-read it since.',
  }),
];

function mockApi(guidance: GuidanceDoc[]) {
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query: string };

    let data: unknown = {};
    if (body.query.includes('query Projects')) data = { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
    else if (body.query.includes('query Areas')) data = { areas: [] };
    else if (body.query.includes('query Tickets')) data = { tickets: { items: [], total: 0 } };
    else if (body.query.includes('query Batches')) data = { batches: [] };
    else if (body.query.includes('query Guidance')) data = { guidance };
    else if (body.query.includes('query Roadmaps')) data = { roadmaps: [] };
    else if (body.query.includes('query Tags')) data = { tags: [] };
    else if (body.query.includes('query NeedsYou')) data = { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };

    return new Response(JSON.stringify({ data }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }) as typeof fetch;
}

async function renderGuidance() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(() => (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  ));
  await router.navigate({ to: '/guidance' });
}

describe('Guidance page', () => {
  beforeEach(() => {
    mockApi(CORPUS);
  });

  // An agent reads the bundle top to bottom, so a human checking what it was given has to see the
  // same order. Alphabetical would be a different document set in a different sequence.
  it('groups the corpus in bundle order, not alphabetically', async () => {
    await renderGuidance();

    const headings = await screen.findAllByRole('heading', { level: 2 });
    expect(headings.map((h) => h.textContent)).toEqual(['Workflow', 'Convention', 'Checklist']);
  });

  it('says loudly when a document describes code that has moved, and still shows it', async () => {
    await renderGuidance();

    expect(await screen.findByText(/1 document describes code that has moved|One document/)).toBeInTheDocument();
    expect(screen.getByText(/nobody has re-read it since/)).toBeInTheDocument();
    // Never withheld: no guidance is worse than old guidance.
    expect(screen.getByText('Working a ticket')).toBeInTheDocument();
  });

  it('points at how to load the corpus when there is none', async () => {
    mockApi([]);
    await renderGuidance();

    expect(await screen.findByText(/just guidance/)).toBeInTheDocument();
  });
});
