// Line icons for the rail: 24-box, 1.6 stroke, round caps, one glyph per destination.
const PATHS: Record<string, string[]> = {
  ai: ['M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3', 'M7 7h10v10H7z', 'M10 10h4v4h-4z'],
  human: ['M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M4 21a8 8 0 0 1 16 0'],
  area: ['M3 7h6l2 2h10v10H3z', 'M3 7V5h6'],
  board: ['M4 4h4v16H4zM10 4h4v11h-4zM16 4h4v7h-4z'],
  batch: ['m12 3 9 4.5-9 4.5-9-4.5L12 3Z', 'm3 12 9 4.5 9-4.5', 'm3 16.5 9 4.5 9-4.5'],
  roadmap: ['M5 5h4v4H5zM15 15h4v4h-4z', 'M9 7h4a2 2 0 0 1 2 2v2a2 2 0 0 0 2 2', 'M17 13v2'],
  guidance: ['M4 5a2 2 0 0 1 2-2h13v16H6a2 2 0 0 0-2 2z', 'M9 7h7M9 11h7'],
  search: ['M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14Z', 'm20 20-4-4'],
  check: ['m5 12 4 4L19 7'],
  inbox: ['M3 13h5l2 3h4l2-3h5', 'M5 5h14l2 8v6H3v-6z'],
  progress: ['M12 3a9 9 0 1 0 9 9', 'M12 7v5l3 2'],
  plus: ['M12 5v14M5 12h14'],
  next: ['M5 12h13', 'm13 6 6 6-6 6'],
  back: ['M19 12H6', 'm11 18-6-6 6-6'],
  branch: ['M7 4v9M7 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4ZM7 8a2 2 0 1 0 0-4 2 2 0 0 0 0 4ZM17 10a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z', 'M17 10v1a4 4 0 0 1-4 4H9'],
};

export function Icon(props: { name: keyof typeof PATHS; size?: number }) {
  return (
    <svg
      width={props.size ?? 24}
      height={props.size ?? 24}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      {PATHS[props.name].map((d) => (
        <path d={d} />
      ))}
    </svg>
  );
}
