import { For, Show, createSignal } from 'solid-js';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { ARCHIVE_AREA_MUTATION, CREATE_AREA_MUTATION, UNARCHIVE_AREA_MUTATION, UPDATE_AREA_MUTATION } from '../api/queries';
import { useAreasQuery, useProjectKey } from '../api/hooks';
import { InlineEditText } from '../components/InlineEditText';
import { SkeletonLines } from '../components/SkeletonLines';
import { Icon } from '../components/Icon';
import { Topbar } from '../components/Topbar';
import { keepProject } from '../lib/filters';
import { Link } from '@tanstack/solid-router';
import { areaHue } from '../lib/areaHue';
import type { Area, AreaInput, AreaPatch } from '../api/types';

export function Areas() {
  const areasQuery = useAreasQuery(() => true);
  const queryClient = useQueryClient();
  const projectKey = useProjectKey();
  const [showNewArea, setShowNewArea] = createSignal(false);
  const [error, setError] = createSignal('');

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['areas'] });
  const onError = (err: unknown) => setError(err instanceof Error ? err.message : 'Something went wrong');

  const createArea = useMutation(() => ({
    mutationFn: (input: AreaInput) => gql<{ createArea: Area }>(CREATE_AREA_MUTATION, { project: projectKey(), input }),
    onSuccess: () => {
      setError('');
      setShowNewArea(false);
      invalidate();
    },
    onError,
  }));

  const updateArea = useMutation(() => ({
    mutationFn: (vars: { id: string; patch: AreaPatch }) => gql<{ updateArea: Area }>(UPDATE_AREA_MUTATION, { project: projectKey(), ...vars }),
    onSuccess: invalidate,
    onError,
  }));

  const archiveArea = useMutation(() => ({
    mutationFn: (id: string) => gql(ARCHIVE_AREA_MUTATION, { project: projectKey(), id }),
    onSuccess: invalidate,
    onError,
  }));

  const unarchiveArea = useMutation(() => ({
    mutationFn: (id: string) => gql(UNARCHIVE_AREA_MUTATION, { project: projectKey(), id }),
    onSuccess: invalidate,
    onError,
  }));

  return (
    <>
      <Topbar
        title="Areas"
        lead="Parts of the codebase. Findings are routed to an area by matching file paths against its globs."
      >
        <button type="button" class="btn btn--primary" onClick={() => setShowNewArea((v) => !v)}>
          {showNewArea() ? 'Cancel' : 'New area'}
        </button>
      </Topbar>

      <div class="view view--narrow">
        <Show when={error()}>
          <p class="error-text">{error()}</p>
        </Show>

        <Show when={showNewArea()}>
          <NewAreaForm onCreate={(input) => createArea.mutate(input)} pending={createArea.isPending} />
        </Show>

        <Show when={!areasQuery.isPending} fallback={<SkeletonLines rows={5} />}>
          <Show when={(areasQuery.data?.length ?? 0) > 0} fallback={<p class="empty-state">No areas yet. Add one to start routing tickets.</p>}>
            <ul class="boxes boxes--wide">
              <For each={areasQuery.data}>
                {(area) => (
                  <li class={['box', { 'box--archived': !!area.archivedAt }]} style={{ '--hue-area': areaHue(area.name) }}>
                    <div class="box__name">
                      <Icon name="area" size={18} />
                      <span class="code">
                        <InlineEditText
                          value={area.name}
                          onSave={(value) => value && updateArea.mutate({ id: area.id, patch: { name: value } })}
                        />
                      </span>
                    </div>
                    <div class="box__desc">
                      <InlineEditText
                        value={area.description}
                        placeholder="Add a description"
                        onSave={(value) => updateArea.mutate({ id: area.id, patch: { description: value } })}
                      />
                    </div>
                    <div class="box__globs code">
                      {area.pathGlobs.length > 0 ? area.pathGlobs.join('   ') : 'No path globs, so nothing routes here automatically'}
                    </div>
                    <div class="box__foot">
                      <Link to="/a/$areaId" params={{ areaId: area.id }} search={keepProject} class="box__board">
                      {area.openCount === 1 ? '1 open' : `${area.openCount} open`} →
                    </Link>
                      <label class="toggle">
                        <input
                          type="checkbox"
                          checked={area.autoAcceptAi}
                          onChange={(event) =>
                            updateArea.mutate({ id: area.id, patch: { autoAcceptAi: event.currentTarget.checked } })
                          }
                        />
                        Skip the inbox
                      </label>
                      <Show
                        when={!area.archivedAt}
                        fallback={
                          <button type="button" class="btn btn--small" onClick={() => unarchiveArea.mutate(area.id)}>
                            Restore
                          </button>
                        }
                      >
                        <button type="button" class="btn btn--small btn--danger" onClick={() => archiveArea.mutate(area.id)}>
                          Archive
                        </button>
                      </Show>
                    </div>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </Show>
      </div>
    </>
  );
}

function NewAreaForm(props: { onCreate: (input: AreaInput) => void; pending: boolean }) {
  const [name, setName] = createSignal('');
  const [description, setDescription] = createSignal('');
  const [globs, setGlobs] = createSignal('');
  const [autoAccept, setAutoAccept] = createSignal(false);

  const submit = (event: Event) => {
    event.preventDefault();
    if (!name().trim()) return;
    props.onCreate({
      name: name().trim(),
      description: description(),
      pathGlobs: globs()
        .split(',')
        .map((g) => g.trim())
        .filter(Boolean),
      autoAcceptAi: autoAccept(),
    });
  };

  return (
    <form class="form-panel" onSubmit={submit}>
      <div class="field">
        <label for="area-name">Name, like api/auth</label>
        <input id="area-name" type="text" value={name()} autofocus onInput={(event) => setName(event.currentTarget.value)} />
      </div>
      <div class="field">
        <label for="area-description">Description</label>
        <input
          id="area-description"
          type="text"
          value={description()}
          onInput={(event) => setDescription(event.currentTarget.value)}
        />
      </div>
      <div class="field">
        <label for="area-globs">Path globs, comma separated, like api/src/auth/**</label>
        <input id="area-globs" type="text" value={globs()} onInput={(event) => setGlobs(event.currentTarget.value)} />
      </div>
      <div class="checkbox-field">
        <input
          id="area-auto-accept"
          type="checkbox"
          checked={autoAccept()}
          onChange={(event) => setAutoAccept(event.currentTarget.checked)}
        />
        <label for="area-auto-accept">Skip the inbox: findings here open straight away</label>
      </div>
      <div class="btn-row">
        <button type="submit" class="btn btn--primary" disabled={props.pending}>
          Add area
        </button>
      </div>
    </form>
  );
}
