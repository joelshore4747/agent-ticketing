import { For } from 'solid-js';

export function SkeletonLines(props: { rows?: number }) {
  const rows = () => Array.from({ length: props.rows ?? 3 });
  return (
    <div class="skeleton-lines" aria-hidden="true">
      <For each={rows()}>{() => <div class="skeleton-line" />}</For>
    </div>
  );
}
