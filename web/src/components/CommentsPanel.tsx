import { For, Show, createSignal } from 'solid-js';
import { useMutation, useQueryClient } from '@tanstack/solid-query';
import { gql } from '../api/client';
import { ADD_COMMENT_MUTATION } from '../api/queries';
import { ticketKey, useProjectKey } from '../api/hooks';
import { relativeTime } from '../lib/time';
import type { Comment } from '../api/types';

/** Comments, and the box for adding one.
 *
 *  A ticket with no comments shows no list and no "No comments yet" placeholder — an empty state
 *  that only ever says "nothing here" is a box the eye has to cross to reach the thing it wants.
 *  What is left is the one text box you came for. */
export function CommentsPanel(props: { ticketId: string; ticketKeyStr: string; comments: Comment[] }) {
  const [body, setBody] = createSignal('');
  const queryClient = useQueryClient();
  const projectKey = useProjectKey();

  const addComment = useMutation(() => ({
    mutationFn: (text: string) =>
      gql<{ addComment: Comment }>(ADD_COMMENT_MUTATION, { ticketId: props.ticketId, body: text }),
    onSuccess: () => {
      setBody('');
      void queryClient.invalidateQueries({ queryKey: ticketKey(projectKey(), props.ticketKeyStr) });
    },
  }));

  const submit = (event: Event) => {
    event.preventDefault();
    const text = body().trim();
    if (!text) return;
    addComment.mutate(text);
  };

  return (
    <div>
      <Show when={props.comments.length > 0}>
        <ul class="comments">
          <For each={props.comments}>
            {(comment) => (
              <li class={['comment', { 'comment--system': comment.kind === 'system' }]}>
                <div class="comment__meta">
                  <span>{comment.author}</span>
                  <time datetime={comment.createdAt}>{relativeTime(comment.createdAt)}</time>
                </div>
                <div>{comment.body}</div>
              </li>
            )}
          </For>
        </ul>
      </Show>

      <form onSubmit={submit} class="comment-form">
        <textarea
          id="new-comment"
          aria-label="Add a comment"
          placeholder="Write a comment. Cmd/Ctrl+Enter to post."
          value={body()}
          onInput={(event) => setBody(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) submit(event);
          }}
        />
        <Show when={body().trim()}>
          <div class="btn-row">
            <button type="submit" class="btn btn--primary btn--small" disabled={addComment.isPending}>
              Add comment
            </button>
          </div>
        </Show>
      </form>
    </div>
  );
}
