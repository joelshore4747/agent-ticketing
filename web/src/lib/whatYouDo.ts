// Mirrors the heading match in api/internal/service/needsjoel.go (rule 27), so the web app pulls
// exactly the section the API requires on a needs-joel ticket. Only a markdown heading ends the
// section — a bold line does not, because TP-242 writes "**What counts as OK:**" between two of
// its numbered steps, and ending the section there would hide half of it.
const HEADING = /^[ \t]{0,3}(?:#{1,6}[ \t]*|\*\*[ \t]*|__[ \t]*)what you do\b.*$/im;
const NEXT_HEADING = /^[ \t]{0,3}#{1,6}[ \t]/m;

/**
 * The raw markdown of a description's "What you do" section, heading excluded, or null when
 * there is none. This only finds the substring; it is rendered through the app's one markdown
 * renderer (`renderMarkdown`), not a second parser.
 */
export function whatYouDoSection(description: string | null | undefined): string | null {
  if (!description) return null;
  const heading = HEADING.exec(description);
  if (!heading) return null;

  const rest = description.slice(heading.index + heading[0].length);
  const next = NEXT_HEADING.exec(rest);
  const body = (next ? rest.slice(0, next.index) : rest).trim();
  return body || null;
}
