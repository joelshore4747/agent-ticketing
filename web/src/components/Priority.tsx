import { label } from '../lib/labels';
import type { Priority as PriorityValue } from '../api/types';

/** Priority as a tag. All four are coloured — a ramp from red down to grey — so the level reads at
 *  a glance on a board where every other word is black. */
export function Priority(props: { value: PriorityValue }) {
  return <span class={`chip chip--priority chip--${props.value}`}>{label(props.value)}</span>;
}
