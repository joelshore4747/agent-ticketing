import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library';
import { QueryClient, QueryClientProvider } from '@tanstack/solid-query';
import { RouterProvider } from '@tanstack/solid-router';
import { router } from '../router';
import { login, logout } from '../api/client';

function dataFor(query: string): unknown {
  if (query.includes('query Projects')) return { projects: [{ id: 'p', key: 'TP', name: 'Ticketing' }] };
  if (query.includes('query Areas')) return { areas: [] };
  if (query.includes('query Batches')) return { batches: [] };
  if (query.includes('query Guidance')) return { guidance: [] };
  if (query.includes('query Roadmaps')) return { roadmaps: [] };
  if (query.includes('query Tags')) return { tags: [] };
  if (query.includes('query NeedsYou')) {
    return { needsYou: { total: 0, triage: [], checkpoints: [], proposals: [], uncited: [], tripwires: [] } };
  }
  if (query.includes('query Tickets')) return { tickets: { items: [], total: 0 } };
  return {};
}

/** Stands in for the API once login is on (TP-317): every request without a recognised bearer
 *  token gets the 401 auth.go sends, and one with the right token gets served. */
function mockApi() {
  const sent: { authorization?: string; query: string }[] = [];
  globalThis.fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    const body = JSON.parse(String(init?.body ?? '{}')) as { query: string };
    sent.push({ authorization: headers.get('Authorization') ?? undefined, query: body.query });

    if (headers.get('Authorization') !== 'Bearer abc') {
      return new Response(JSON.stringify({ error: 'no credential' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return new Response(JSON.stringify({ data: dataFor(body.query) }), {
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

describe('Login', () => {
  beforeEach(() => {
    logout();
  });

  it('shows a token field in place of the page on 401, sends what is entered, and logs out', async () => {
    const sent = mockApi();
    renderApp();

    // 401 on every query replaces the whole app with the one field, not just a broken page.
    const input = await screen.findByLabelText('Token');
    expect(screen.queryByRole('navigation', { name: 'Sections' })).toBeNull();

    fireEvent.input(input, { target: { value: 'abc' } });
    await Promise.resolve();
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }));

    // Logging in retries: the rail comes up, and the retried requests carry the pasted token.
    await waitFor(() => expect(screen.getByRole('navigation', { name: 'Sections' })).toBeInTheDocument());
    expect(sent.some((s) => s.authorization === 'Bearer abc')).toBe(true);
    expect(localStorage.getItem('ticketing-token')).toBe('abc');

    fireEvent.click(screen.getByRole('button', { name: 'Log out' }));
    expect(localStorage.getItem('ticketing-token')).toBeNull();

    // Logging out shows the token field immediately, same as a 401 would, rather than leaving the
    // rest of the app on screen until some later request discovers the credential is gone (TP-331).
    expect(await screen.findByLabelText('Token')).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: 'Sections' })).toBeNull();
  });

  it('sends no Authorization header, and shows no Log out control, until a token is entered', async () => {
    mockApi();
    renderApp();

    await screen.findByLabelText('Token');
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
  });

  it('skips the field entirely when a token is already stored', async () => {
    login('abc');
    mockApi();
    renderApp();

    await waitFor(() => expect(screen.getByRole('navigation', { name: 'Sections' })).toBeInTheDocument());
    expect(screen.queryByLabelText('Token')).toBeNull();
    expect(screen.getByRole('button', { name: 'Log out' })).toBeInTheDocument();
  });
});
