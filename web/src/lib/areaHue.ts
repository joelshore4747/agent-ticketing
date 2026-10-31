/** Areas are a list the user manages, so a colour is derived from the name rather than named per
 *  area: adding "cli" tomorrow should not need a stylesheet edit. Six muted hues from the area
 *  palette (tokens.css --area-1..6), picked by a stable hash so an area keeps its colour
 *  across reloads, machines and every view that shows it. */
export function areaHue(name: string): string {
  let hash = 0;
  for (let i = 0; i < name.length; i += 1) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0;
  }
  return `var(--area-${(Math.abs(hash) % 6) + 1})`;
}
