const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 60 * 60 * 24 * 365],
  ['month', 60 * 60 * 24 * 30],
  ['week', 60 * 60 * 24 * 7],
  ['day', 60 * 60 * 24],
  ['hour', 60 * 60],
  ['minute', 60],
];

const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

/** Formats an RFC 3339 timestamp as "3 hours ago" / "in 2 days". */
export function relativeTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';

  const diffSeconds = Math.round((date.getTime() - Date.now()) / 1000);
  for (const [unit, secondsInUnit] of UNITS) {
    if (Math.abs(diffSeconds) >= secondsInUnit) {
      return rtf.format(Math.round(diffSeconds / secondsInUnit), unit);
    }
  }
  return rtf.format(diffSeconds, 'second');
}

const SHORT: [string, number][] = [
  ['y', 60 * 60 * 24 * 365],
  ['w', 60 * 60 * 24 * 7],
  ['d', 60 * 60 * 24],
  ['h', 60 * 60],
  ['m', 60],
];

/** Age as a code — "47m", "3d" — for a board card, where "47 minutes ago" is three words too many
 *  and wraps the row its priority and its button share. The ticket page still spells it out. */
export function shortAge(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';

  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  for (const [suffix, secondsInUnit] of SHORT) {
    if (seconds >= secondsInUnit) return `${Math.floor(seconds / secondsInUnit)}${suffix}`;
  }
  return 'now';
}
