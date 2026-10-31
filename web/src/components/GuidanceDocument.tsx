import { Show } from 'solid-js';
import { label } from '../lib/labels';
import { renderMarkdown } from '../lib/markdown';
import type { GuidanceDoc } from '../api/types';

/** One document, collapsed. Five of these open at once is a wall of markdown, and the first
 *  question a reader has is which documents went out, not what they say. */
export function GuidanceDocument(props: { doc: GuidanceDoc }) {
  const doc = () => props.doc;

  return (
    <details class="doc">
      <summary class="doc__head">
        <span>{doc().title}</span>
        <span class="key">
          {doc().slug} r{doc().revision}
        </span>
        <span class="chip chip--type">{label(doc().kind)}</span>
        <Show when={!doc().inline}>
          <span class="chip chip--type">Fetched on request</span>
        </Show>
        <Show when={doc().stale}>
          <span class="chip chip--stale">Stale</span>
        </Show>
      </summary>

      {/* Stale is never a reason to hide a document — no guidance is worse than old guidance — so
          it is said loudly and the body stays readable underneath. */}
      <Show when={doc().stale}>
        <p class="doc__stale">
          <strong>The code this describes has moved.</strong>{' '}
          {doc().staleReason || 'It was last reviewed against a commit that is no longer current.'}
        </p>
      </Show>

      <Show when={doc().sourcePaths.length > 0}>
        <p class="doc__paths code">{doc().sourcePaths.join('   ')}</p>
      </Show>

      <div class="markdown-body" innerHTML={renderMarkdown(doc().body)} />
    </details>
  );
}
