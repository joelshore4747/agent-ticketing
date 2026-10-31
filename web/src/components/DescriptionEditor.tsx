import { Show, createMemo, createSignal } from 'solid-js';
import { renderMarkdown } from '../lib/markdown';

export function DescriptionEditor(props: { value: string; onSave: (value: string) => void }) {
  const [editing, setEditing] = createSignal(false);
  const [draft, setDraft] = createSignal(props.value);
  const html = createMemo(() => renderMarkdown(props.value));

  const startEdit = () => {
    setDraft(props.value);
    setEditing(true);
  };

  const save = () => {
    setEditing(false);
    if (draft() !== props.value) props.onSave(draft());
  };

  return (
    <div class="description-editor">
      <div class="panel__head">
        <h2>Description</h2>
        <div class="btn-row">
          <Show when={editing()}>
            <button type="button" class="btn btn--small btn--quiet" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </Show>
          <button type="button" class="btn btn--small" onClick={() => (editing() ? save() : startEdit())}>
            {editing() ? 'Save' : 'Edit'}
          </button>
        </div>
      </div>
      <Show
        when={!editing()}
        fallback={
          <textarea
            value={draft()}
            autofocus
            aria-label="Description, markdown"
            onInput={(event) => setDraft(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.preventDefault();
                setEditing(false);
              }
              if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) save();
            }}
          />
        }
      >
        <Show when={props.value.trim()} fallback={<p class="meta">No description. Edit to add one; markdown works.</p>}>
          <div class="markdown-body" innerHTML={html()} />
        </Show>
      </Show>
    </div>
  );
}
