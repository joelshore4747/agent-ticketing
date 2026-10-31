// Minimal GraphQL client. Posts to /graphql (proxied to the API in dev),
// throws on a GraphQL error so callers can catch a single Error type.

import { createSignal } from 'solid-js';

const TOKEN_KEY = 'ticketing-token';

// The pasted token, kept in localStorage and mirrored into a signal so the rail can show or hide
// its "Log out" control reactively (TP-320). In local mode (no TICKETING_TOKENS on the API) nothing
// here is ever read: the API answers every request and needsLogin never flips.
export const [token, setToken] = createSignal<string | null>(localStorage.getItem(TOKEN_KEY));

// Flipped by gql() on a 401. The router shows one field in place of the whole app while this is
// true, because nothing on screen works without a credential once the API is asking for one.
export const [needsLogin, setNeedsLogin] = createSignal(false);

export function login(value: string): void {
  localStorage.setItem(TOKEN_KEY, value);
  setToken(value);
  setNeedsLogin(false);
}

export function logout(): void {
  localStorage.removeItem(TOKEN_KEY);
  setToken(null);
  setNeedsLogin(true);
}

interface GraphQLResponse<T> {
  data?: T;
  errors?: { message: string }[];
}

export class GraphQLRequestError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'GraphQLRequestError';
  }
}

export async function gql<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const current = token();
  if (current) headers.Authorization = `Bearer ${current}`;

  const res = await fetch('/graphql', {
    method: 'POST',
    headers,
    body: JSON.stringify({ query, variables }),
  });

  if (res.status === 401) {
    setNeedsLogin(true);
    throw new GraphQLRequestError('No credential: log in again.');
  }

  if (!res.ok) {
    throw new GraphQLRequestError(`Request failed: ${res.status} ${res.statusText}`);
  }

  const json = (await res.json()) as GraphQLResponse<T>;

  if (json.errors && json.errors.length > 0) {
    throw new GraphQLRequestError(json.errors.map((e) => e.message).join('; '));
  }

  if (json.data === undefined) {
    throw new GraphQLRequestError('Response had no data');
  }

  return json.data;
}

/** True when an error looks like the optimistic-locking conflict described in docs/api.md. */
export function isVersionConflict(error: unknown): boolean {
  return error instanceof Error && error.message.toLowerCase().includes('version');
}
