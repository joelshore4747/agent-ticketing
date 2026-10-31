import { Show, createSignal } from 'solid-js';

/**
 * Click-to-edit text: shows a span, swaps to an input on click, saves on Enter
 * or blur, discards on Escape. Used for ticket titles, branch names, area names.
 */
export function InlineEditText(props: {
  value: string;
  placeholder?: string;
  inputClass?: string;
  onSave: (value: string) => void;
}) {
  const [editing, setEditing] = createSignal(false);
  const [draft, setDraft] = createSignal(props.value);

  const startEdit = () => {
    setDraft(props.value);
    setEditing(true);
  };

  const commit = () => {
    setEditing(false);
    const next = draft().trim();
    if (next !== props.value) props.onSave(next);
  };

  return (
    <Show
      when={!editing()}
      fallback={
        <input
          class={props.inputClass ?? 'inline-input'}
          value={draft()}
          autofocus
          onInput={(event) => setDraft(event.currentTarget.value)}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              commit();
            } else if (event.key === 'Escape') {
              event.preventDefault();
              setEditing(false);
            }
          }}
        />
      }
    >
      <span
        class={['editable-text', { placeholder: !props.value }]}
        role="button"
        tabindex="0"
        title="Click to edit"
        onClick={startEdit}
        onKeyDown={(event) => {
          if (event.key === 'Enter') startEdit();
        }}
      >
        {props.value || props.placeholder || 'Add'}
      </span>
    </Show>
  );
}
