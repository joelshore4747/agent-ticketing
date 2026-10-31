import { useQuery } from '@tanstack/solid-query';
import { useSearch } from '@tanstack/solid-router';
import { gql } from './client';
import {
  AREAS_QUERY,
  BATCH_QUERY,
  BATCHES_QUERY,
  GUIDANCE_QUERY,
  NEEDS_JOEL_QUERY,
  NEEDS_YOU_QUERY,
  PROJECTS_QUERY,
  ROADMAPS_QUERY,
  TAGS_QUERY,
  TICKET_QUERY,
  TICKETS_QUERY,
} from './queries';
import { parseSearch } from '../lib/filters';
import type { Area, Batch, GuidanceDoc, NeedsYou, Project, Roadmap, Tag, Ticket, TicketFilter, TicketPage } from './types';

/** The project every scoped query runs against, read from the URL rather than passed down. Scoping
 *  is a property of where you are, not of each component, and threading it through every caller
 *  was how the first version leaked the default project into half the queries. Undefined means the
 *  API's default, which is the oldest project. */
export function useProjectKey(): () => string | undefined {
  const search = useSearch({ strict: false });
  return () => parseSearch(search() as unknown as Record<string, unknown>).project;
}

export const ticketsKey = (
  project: string | undefined,
  filter: TicketFilter | undefined,
  search: string | undefined,
  limit: number,
  offset: number,
) => ['tickets', project ?? null, filter ?? null, search ?? null, limit, offset] as const;

export const ticketKey = (project: string | undefined, key: string) =>
  ['ticket', project ?? null, key] as const;

export const areasKey = (project: string | undefined, includeArchived: boolean) =>
  ['areas', project ?? null, includeArchived] as const;

export const batchesKey = (project: string | undefined) => ['batches', project ?? null] as const;

export const guidanceKey = (project: string | undefined) => ['guidance', project ?? null] as const;


export const batchKey = (project: string | undefined, id: string) =>
  ['batch', project ?? null, id] as const;

export const roadmapsKey = (project: string | undefined) => ['roadmaps', project ?? null] as const;

export const tagsKey = (project: string | undefined) => ['tags', project ?? null] as const;

export const needsYouKey = (project: string | undefined) => ['needsYou', project ?? null] as const;

// Under needsYou's key, so everything that refreshes Needs you refreshes the to-do list too.
export const needsJoelKey = (project: string | undefined) => ['needsYou', project ?? null, 'needsJoel'] as const;

export function useTicketsQuery(
  args: () => { filter?: TicketFilter; search?: string; limit?: number; offset?: number; enabled?: boolean },
) {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const { filter, search, limit = 50, offset = 0, enabled = true } = args();
    const project = projectKey();
    return {
      queryKey: ticketsKey(project, filter, search, limit, offset),
      queryFn: () =>
        gql<{ tickets: TicketPage }>(TICKETS_QUERY, { project, filter, search, limit, offset }).then(
          (d) => d.tickets,
        ),
      enabled,
    };
  });
}

export function useTicketQuery(key: () => string) {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: ticketKey(project, key()),
      queryFn: () =>
        gql<{ ticket: Ticket | null }>(TICKET_QUERY, { project, key: key() }).then((d) => d.ticket),
    };
  });
}

export function useAreasQuery(includeArchived: () => boolean = () => false) {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: areasKey(project, includeArchived()),
      queryFn: () =>
        gql<{ areas: Area[] }>(AREAS_QUERY, { project, includeArchived: includeArchived() }).then(
          (d) => d.areas,
        ),
    };
  });
}

export function useProjectsQuery() {
  return useQuery(() => ({
    queryKey: ['projects'] as const,
    queryFn: () => gql<{ projects: Project[] }>(PROJECTS_QUERY).then((d) => d.projects),
    staleTime: 60_000,
  }));
}

export function useGuidanceQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: guidanceKey(project),
      queryFn: () => gql<{ guidance: GuidanceDoc[] }>(GUIDANCE_QUERY, { project }).then((d) => d.guidance),
    };
  });
}

export function useBatchesQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: batchesKey(project),
      queryFn: () => gql<{ batches: Batch[] }>(BATCHES_QUERY, { project }).then((d) => d.batches),
    };
  });
}

export function useBatchQuery(id: () => string) {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: batchKey(project, id()),
      queryFn: () =>
        gql<{ batch: Batch | null }>(BATCH_QUERY, { project, id: id() }).then((d) => d.batch),
    };
  });
}

export function useRoadmapsQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: roadmapsKey(project),
      queryFn: () => gql<{ roadmaps: Roadmap[] }>(ROADMAPS_QUERY, { project }).then((d) => d.roadmaps),
    };
  });
}

export function useTagsQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: tagsKey(project),
      queryFn: () => gql<{ tags: Tag[] }>(TAGS_QUERY, { project }).then((d) => d.tags),
    };
  });
}

export function useNeedsYouQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: needsYouKey(project),
      queryFn: () => gql<{ needsYou: NeedsYou }>(NEEDS_YOU_QUERY, { project }).then((d) => d.needsYou),
    };
  });
}

/** Every needs-joel ticket not yet done, whatever its status: the to-do list on Needs you. */
export function useNeedsJoelQuery() {
  const projectKey = useProjectKey();
  return useQuery(() => {
    const project = projectKey();
    return {
      queryKey: needsJoelKey(project),
      queryFn: () =>
        gql<{ tickets: { items: Ticket[] } }>(NEEDS_JOEL_QUERY, { project }).then((d) => d.tickets?.items ?? []),
    };
  });
}
