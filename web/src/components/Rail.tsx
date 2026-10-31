import { For, Show, createMemo, createSignal } from 'solid-js';
import { Link, useNavigate } from '@tanstack/solid-router';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import {
  useAreasQuery,
  useBatchesQuery,
  useGuidanceQuery,
  useNeedsYouQuery,
  useProjectKey,
  useProjectsQuery,
  useRoadmapsQuery,
  useTicketsQuery,
} from '../api/hooks';
import { gql, logout, token } from '../api/client';
import { CREATE_PROJECT_MUTATION } from '../api/queries';
import { areaHue } from '../lib/areaHue';
import { keepProject } from '../lib/filters';
import { Icon } from './Icon';
import type { Project } from '../api/types';

/** The rail: a dark column that is where the chrome stops and the page
 *  begins, its groups naming what kind of thing each destination is. Every row carries a count,
 *  because the question this app is open to answer — what needs doing — should be answerable
 *  without clicking anything. */
export function Rail() {
  const projectKey = useProjectKey();
  const projects = useProjectsQuery();
  const areas = useAreasQuery();
  const inbox = useTicketsQuery(() => ({ filter: { source: 'ai', status: ['triage'] }, limit: 1 }));
  const aiOpen = useTicketsQuery(() => ({ filter: { source: 'ai', status: ['open'] }, limit: 1 }));
  const humanOpen = useTicketsQuery(() => ({ filter: { source: 'human', status: ['open'] }, limit: 1 }));
  const batches = useBatchesQuery();
  const roadmaps = useRoadmapsQuery();
  const guidance = useGuidanceQuery();
  const needsYou = useNeedsYouQuery();

  // Only the stale count is shown, and in the attention colour: a corpus that is fine needs no
  // number, and one that has drifted is the single thing about it worth interrupting anyone for.
  const staleDocs = createMemo(() => guidance.data?.filter((doc) => doc.stale).length ?? 0);

  // The count is batches still open, not batches ever run: a finished batch is history, and this
  // row answers the same question as every other one — what is currently going on.
  const inFlight = createMemo(() => batches.data?.filter((batch) => !batch.finishedAt).length ?? 0);

  // A roadmap only asks something of you at a checkpoint, so that is the only number it shows.
  const checkpoints = createMemo(
    () =>
      roadmaps.data?.reduce(
        (n, roadmap) => n + roadmap.batches.filter((b) => b.state === 'awaiting_checkpoint').length,
        0,
      ) ?? 0,
  );

  // No key in the URL means the API's default, which is the oldest project.
  const current = createMemo<Project | undefined>(() => {
    const key = projectKey();
    return key ? projects.data?.find((p) => p.key === key) : projects.data?.[0];
  });

  return (
    <nav class="rail" aria-label="Sections">
      <ProjectSwitcher projects={projects.data} current={current()} />

      <div class="rail__nav">
        <p class="rail__group">Work</p>
        {/* First, because it is the one row that is only ever about you. */}
        <Link to="/needs-you" search={keepProject} activeProps={{ class: 'active' }} class="rail__link">
          <Icon name="inbox" size={19} />
          <span>Needs you</span>
          <Show when={needsYou.data?.total}>
            {(total) => <span class="rail__count rail__count--attention">{total()}</span>}
          </Show>
        </Link>
        <Link
          to="/"
          search={keepProject}
          activeProps={{ class: 'active' }}
          activeOptions={{ exact: true }}
          class="rail__link"
        >
          <Icon name="board" size={19} />
          <span>Board</span>
        </Link>

        <Link to="/batches" search={keepProject} activeProps={{ class: 'active' }} class="rail__link">
          <Icon name="batch" size={19} />
          <span>Batches</span>
          <Show when={inFlight()}>{(total) => <span class="rail__count">{total()}</span>}</Show>
        </Link>

        <Link to="/roadmaps" search={keepProject} activeProps={{ class: 'active' }} class="rail__link">
          <Icon name="roadmap" size={19} />
          <span>Roadmaps</span>
          <Show when={checkpoints()}>
            {(total) => <span class="rail__count rail__count--attention">{total()}</span>}
          </Show>
        </Link>

        <Link to="/guidance" search={keepProject} activeProps={{ class: 'active' }} class="rail__link">
          <Icon name="guidance" size={19} />
          <span>Guidance</span>
          <Show when={staleDocs()}>
            {(total) => <span class="rail__count rail__count--attention">{total()}</span>}
          </Show>
        </Link>

        <p class="rail__group">Queues</p>
        <Link to="/ai" search={keepProject} activeProps={{ class: 'active' }} class="rail__link rail__link--ai">
          <Icon name="ai" size={19} />
          <span>AI findings</span>
          <Show
            when={inbox.data?.total}
            fallback={<Show when={aiOpen.data?.total}>{(t) => <span class="rail__count">{t()}</span>}</Show>}
          >
            {(total) => <span class="rail__count rail__count--attention">{total()}</span>}
          </Show>
        </Link>
        <Link to="/human" search={keepProject} activeProps={{ class: 'active' }} class="rail__link rail__link--human">
          <Icon name="human" size={19} />
          <span>Human tickets</span>
          <Show when={humanOpen.data?.total}>{(total) => <span class="rail__count">{total()}</span>}</Show>
        </Link>

        <p class="rail__group">Areas</p>
        <For each={areas.data}>
          {(area) => (
            <Link
              to="/a/$areaId"
              params={{ areaId: area.id }}
              search={keepProject}
              activeProps={{ class: 'active' }}
              class="rail__link rail__link--area"
              style={{ '--hue-area': areaHue(area.name) }}
            >
              <span class="rail__dot" />
              <span>{area.name}</span>
              <Show when={area.openCount}>{(count) => <span class="rail__count">{count()}</span>}</Show>
            </Link>
          )}
        </For>
        <Link to="/areas" search={keepProject} activeProps={{ class: 'active' }} class="rail__link rail__link--quiet">
          <Icon name="area" size={19} />
          <span>Manage areas</span>
        </Link>

        {/* Only shown once a token has been pasted in (TP-320): local mode never sets one, so
            nothing new appears for development. */}
        <Show when={token()}>
          <button type="button" class="rail__link rail__link--quiet rail__link--button" onClick={() => logout()}>
            <span>Log out</span>
          </button>
        </Show>
      </div>
    </nav>
  );
}

/** Which project you are in, and the way into the others. The key is what every ticket in the
 *  project is named after, so it is chosen once here and never edited. */
function ProjectSwitcher(props: { projects: Project[] | undefined; current: Project | undefined }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [open, setOpen] = createSignal(false);
  const [creating, setCreating] = createSignal(false);
  const [key, setKey] = createSignal('');
  const [name, setName] = createSignal('');
  const [error, setError] = createSignal('');

  const go = (project: Project) => {
    setOpen(false);
    void navigate({ to: '/', search: { project: project.key } } as never);
  };

  const createProject = useMutation(() => ({
    mutationFn: (vars: { key: string; name: string }) =>
      gql<{ createProject: Project }>(CREATE_PROJECT_MUTATION, vars).then((d) => d.createProject),
    onSuccess: (project) => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] });
      setCreating(false);
      setKey('');
      setName('');
      go(project);
    },
    onError: (err: unknown) => setError(err instanceof Error ? err.message : 'Could not create the project'),
  }));

  return (
    <div class="rail__project">
      <button
        type="button"
        class="rail__switch"
        aria-expanded={open() ? 'true' : 'false'}
        aria-haspopup="menu"
        onClick={() => setOpen((v) => !v)}
      >
        <span class="rail__mark">
          <Icon name="board" size={16} />
        </span>
        <span class="rail__switch-text">
          <b>{props.current?.name ?? 'Tickets'}</b>
          <span>{props.current?.key ?? '—'}</span>
        </span>
        <span class="rail__chevron" aria-hidden="true">
          ▾
        </span>
      </button>

      <Show when={open()}>
        <div class="rail__menu" role="menu">
          <For each={props.projects}>
            {(project) => (
              <button
                type="button"
                role="menuitem"
                class={['rail__menu-item', { on: project.key === props.current?.key }]}
                onClick={() => go(project)}
              >
                <b>{project.name}</b>
                <span class="code">{project.key}</span>
              </button>
            )}
          </For>
          <button type="button" class="rail__menu-item rail__menu-new" onClick={() => setCreating(true)}>
            <Icon name="plus" size={15} />
            New project
          </button>
        </div>
      </Show>

      <Show when={creating()}>
        <div
          class="dialog"
          role="dialog"
          aria-modal="true"
          aria-label="New project"
          onClick={(event) => {
            if (event.target === event.currentTarget) setCreating(false);
          }}
        >
          <form
            class="dialog__panel"
            onSubmit={(event) => {
              event.preventDefault();
              setError('');
              createProject.mutate({ key: key().trim().toUpperCase(), name: name().trim() });
            }}
          >
            <h2>New project</h2>
            <p class="meta">
              The key names every ticket in it, so it is fixed once the project exists. Two to ten characters, a
              letter then letters or digits.
            </p>
            <div class="field">
              <label for="project-key">Key</label>
              <input
                id="project-key"
                type="text"
                value={key()}
                placeholder="OPS"
                autofocus
                onInput={(event) => setKey(event.currentTarget.value)}
              />
            </div>
            <div class="field">
              <label for="project-name">Name</label>
              <input
                id="project-name"
                type="text"
                value={name()}
                placeholder="Operations"
                onInput={(event) => setName(event.currentTarget.value)}
              />
            </div>
            <Show when={error()}>
              <p class="error-text">{error()}</p>
            </Show>
            <div class="btn-row">
              <button
                type="submit"
                class="btn btn--primary"
                disabled={!key().trim() || !name().trim() || createProject.isPending}
              >
                Create project
              </button>
              <button type="button" class="btn" onClick={() => setCreating(false)}>
                Cancel
              </button>
            </div>
          </form>
        </div>
      </Show>
    </div>
  );
}
