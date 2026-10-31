import { Show } from 'solid-js';
import type { JSX } from '@solidjs/web';

/** Names the view and holds whatever acts on it, in the header band.
 *
 *  `leading` takes the place of the title for a view that does not need naming twice: the ticket
 *  page puts its Back button here, because the breadcrumb underneath already says which ticket
 *  you are on. */
export function Topbar(props: {
  title?: string;
  lead?: string;
  eyebrow?: string;
  leading?: JSX.Element;
  children?: JSX.Element;
}) {
  return (
    <header class="topbar">
      <Show when={props.leading}>{props.leading}</Show>
      <Show when={props.title}>
        <div class="topbar__title">
          <Show when={props.eyebrow}>
            <p class="eyebrow">{props.eyebrow}</p>
          </Show>
          <h1>{props.title}</h1>
          <Show when={props.lead}>
            <p>{props.lead}</p>
          </Show>
        </div>
      </Show>
      <Show when={props.children}>
        <div class="topbar__actions">{props.children}</div>
      </Show>
    </header>
  );
}
