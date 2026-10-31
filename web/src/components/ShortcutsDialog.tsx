import { For, onSettled } from 'solid-js';

/** Every shortcut the app has, in one place. Kept as a list next to the code rather than generated,
 *  because the handlers live in six components; adding a shortcut means adding its line here. */
const SHORTCUTS: { keys: string[]; what: string }[] = [
  { keys: ['/'], what: 'Search tickets (jumps to the board first)' },
  { keys: ['?'], what: 'Show this list' },
  { keys: ['Esc'], what: 'Clear the search, or cancel an edit or a dialog' },
  { keys: ['Enter'], what: 'Save a title or branch edit; confirm a resolution' },
  { keys: ['Shift', 'Enter'], what: 'New line in a resolution' },
  { keys: ['Ctrl', 'Enter'], what: 'Post a comment; save a description (⌘ on a Mac)' },
];

export function ShortcutsDialog(props: { onClose: () => void }) {
  let close!: HTMLButtonElement;
  onSettled(() => close.focus());
  return (
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      onClick={(event) => {
        if (event.target === event.currentTarget) props.onClose();
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') props.onClose();
      }}
    >
      <div class="dialog__panel">
        <h2>Keyboard shortcuts</h2>
        <dl class="shortcuts">
          <For each={SHORTCUTS}>
            {(s) => (
              <div>
                <dt>
                  <For each={s.keys}>{(k) => <kbd>{k}</kbd>}</For>
                </dt>
                <dd>{s.what}</dd>
              </div>
            )}
          </For>
        </dl>
        <div class="btn-row">
          <button type="button" class="btn" ref={close} onClick={props.onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
