# Design (web/)

The governing idea: one warm stone neutral makes every light surface (paper, surface, sheet) as
three steps of a single tone rather than three unrelated greys, and the rail is the one dark thing on
screen. With the chrome that quiet, the only colour left is colour that means something. Nothing
here is decorative. Green means done.

## Tokens (`src/styles/tokens.css`, all prefixed `--ui-` except the type scale and hues)
- Ink `#1c1a17` down to muted `#6b665d`; paper `#f3f1ec`, surface `#f9f8f5`, sheet `#ffffff`,
  surface-2 `#ebe8e1`; lines `#e0dcd3` / `#c7c1b6`.
- Rail: deep slate-blue `#172033` with ink `#b9c2d3`; the row you are on is a light lozenge
  `#e8ecf4` with dark ink.
- Accent `#23324a` — a second ink, not a colour. It is what the primary button wears, because an app
  whose loudest thing is its Save button has nothing left to say something urgent with.
- **The four columns**, each coloured outright because the colour is the meaning: inbox violet
  `#6c4fa3`, to-do blue `#2a5ea8`, in-progress burnt orange `#a8430e`, done green `#276b43`, each
  with a tint. A column wears its hue on its head band, its dot and its count.
- **Priority** is a heat ramp, all four levels tagged: urgent `#b23a2a`, high `#875408`, medium
  `#3a6688`, low `#6b665d`, each on its own tint. Low is information too — it is what tells you a
  card can wait.
- **Areas** take a muted hue from a six-colour palette, picked by hashing the area name
  (`src/lib/areaHue.ts`) so a user-managed list needs no stylesheet edit.
- Signals: attention `#b23a2a` (urgent), live `#276b43` (done).
- Every hue holds at least 4.5:1 against its own tint, so a tag stays readable as text.

**Where the colour goes.** Cards stay white and titles stay black. What carries a hue is the handful
of words that classify a ticket — its priority, its area, who filed it — plus the column heads.
Enough that a column reads as a picture before it reads as a list, little enough that nothing shouts.
- Fonts, bundled through Fontsource so the app works offline: **Instrument Sans** for everything,
  and **JetBrains Mono** for every numeral, ticket key, file path, glob and timestamp.
- Type scale is the `--t-*` set in `tokens.css`. Radii 12px for cards and panels, 10px for icon
  tiles, 8px for buttons, inputs and the rail's selected row, and pill.

## Layout
- **Shell**: dark rail (252px) + one scrolling main. The rail opens with the **project switcher** —
  in an app with more than one project, the wordmark was saying the least useful thing on screen —
  then groups its destinations: Work (Board, Batches), Queues (AI findings, Human tickets), Areas (one row
  per area, each a board of its own). Every row carries a count. The inbox count is the one number
  that gets a colour. Each destination wears its hue on its icon, or on a dot where it has none.
- **Topbar**: view title, one line of copy, search on the right, and whatever acts on the view. The
  ticket page is the exception: its topbar holds one accent **Back** button and nothing else, because
  the breadcrumb under it already says which ticket you are on and the title does not need saying
  twice.
- **Board** (`/`, `/ai`, `/human`, `/a/$areaId`): the columns are the statuses, so where a ticket is
  in its life is a position on screen rather than a word you have to read. Four columns for AI work,
  three for your own (human tickets are created open and never have an inbox). An **area board** is
  the same board with one area's work in it, and hides the area filter and the cards' area chips —
  on that board they would say the same thing on every row. **The In progress column is tinted its
  own hue**, the only one that is, so the eye lands on the work in flight first. Filter pills for
  when, type, area and priority sit above; they live in the URL, so a filtered board can be linked.
- **Card**: key, area and source on top; title; then priority and type as tags. Urgency also rides
  the 3px left edge, because a red edge is findable in a scan down a column in a way a word is not.
  Done cards show a green check, "Fixed" and three lines of the resolution.
  It carries **no clock**: a card's age answered a question nobody was asking, and the ticket page
  has every timestamp for when the date matters.
- **Ticket page**: breadcrumb, area eyebrow in the area's own hue, editable title, then two tags —
  priority and type — because the key is already in the breadcrumb and the area is already the
  eyebrow.
  The main column is white panels: Description; the resolution, when there is one; Where in the
  code; Comments; History.
  - **The resolution is an entry, not a field.** It reads as a green note in the main column with a
    check and "Fixed", because it is a sentence somebody wrote about the work — the same kind of
    thing as a comment. It used to sit in the sidebar among the metadata, where it looked like a
    value that had overflowed its row.
  - **The branch lives in the head of "Where in the code"**, which is where in the code it is. That
    panel always renders, so there is one place to add a branch even when no file locations were
    recorded.
  - **Comments show no empty state.** With no comments there is no list and no "No comments yet"
    box, just the one text box you came for; the post button appears once you type.
  - **The sidebar is three blocks**, each with one job: what you can do to the ticket (full-width
    actions), what it is (priority, type and area,
    labelled above the control rather than beside it — a two-column grid left every row a different
    width), and when it happened (timestamps, quietest, at the bottom). There is **no status
    badge**: the actions say it — "Start work" means open, "Mark done" means in progress, "Accept"
    means the inbox, and a finished ticket has its resolution note in the main column. A box that
    only ever repeated one of those was a row of chrome. The pull request appears
    only once there is one. **Priority carries its tag colour into the select**, so the chip on the
    board and the control on the page agree.
- **Areas**: sheet boxes edged in the area's own hue, with globs, open count, a "Skip the inbox"
  toggle and Archive.
- **Batches** (`/batches`, `/b/$batchId`): the provenance side of the tracker — which tickets were
  filed together, why, and what guidance went out with them. The list is one box per batch: brief,
  kind, whether it is still in flight, and how many findings it reported. The batch page is the
  brief, the guidance and the tickets. It is **read-only**: a batch is opened and closed by whatever
  is doing the work (`start_batch`, `finish_batch`), so a New batch button here could only ever make
  an empty one.
  - **Each guidance document is collapsed**, with its slug, revision and kind on the summary line.
    The first question is which documents were served, not what they say, and five bodies open at
    once is a wall of markdown. This is the only place a human can read the exact bodies an agent
    was handed, which is what makes a claim checkable rather than something to take on trust.
  - **A stale document is marked, never withheld**: a banner above the body saying what moved, with
    the body still readable underneath, because no guidance is worse than old guidance.
  - **Its tickets are read-only cards.** There is nowhere to drop one, and no Accept or Dismiss,
    because triaging a finding belongs on the queue that owns it. `BoardCard` renders both: given no
    handlers it drops its buttons and its `draggable` attribute.
  - **Findings reported and tickets filed are different numbers**, and the page says so when they
    disagree — a repeat is deduped onto the ticket that already exists and still bumps
    `findings_count`.

## Moving a ticket
**The board does not start work.** Work is started in the project, by whoever is doing it — usually
Claude Code over MCP or REST — and the board finds out. So cards carry no Start and no Mark done.
The one exception is the inbox, where Accept and Dismiss stay, because triaging a finding is a
decision nobody else can make for you.

Everything else moves by dragging a card to a column. `src/lib/board.ts` mirrors the API's
transition table (docs/api.md rule 4) so **only legal targets light up during a drag** — an illegal
drop is never offered rather than offered and then refused with a 409. Entering done needs a
resolution, so that move opens a dialog first. Closing is not a gesture: it is done from the ticket
page, where a reason gets typed. `src/test/board-moves.test.ts` is where the board finds out if the
service's table moves.

## Projects
Everything in the database is scoped by project (`docs/schema.sql`), and the API takes a project
**key** on every scoped operation, defaulting to the oldest project. The web app carries that key in
the URL (`?project=OPS`) rather than in a store, so a board stays linkable and a shared link opens
the project it was taken from. `src/api/hooks.ts` reads it once, from the URL, so no component has
to pass it down; `keepProject` carries it across a link that changes nothing else.

A project's key names every ticket in it, so it is chosen when the project is created and never
edited. Which also means a key identifies its project: `TP-42` is TP's, and the by-key REST and MCP
endpoints take no project parameter at all.

## Behaviour
- `/` focuses search from any page, jumping to the board first if the page has no search.
- The date filter is a window (Today, Last 7 days, Last 30 days), resolved to an instant when the
  query runs — a board left open overnight means the new today, not yesterday's. It bounds
  `updatedAfter`, so it answers "what has been touched", which is what a board is for.
- Enter saves inline edits and the resolution, Escape cancels. Cmd/Ctrl+Enter submits comments and
  description edits. Focus ring is 2px accent.
- Version conflicts say the board reloaded and to try again; other errors show their message inline.
- Empty states are dashed boxes naming what would fill them. Loading uses skeleton lines. The card
  lift on hover is disabled under prefers-reduced-motion.
- Under 1100px the rail becomes a strip of icons; under 760px it lies down as a top bar and the board
  scrolls sideways, which is what a board does on a phone.

## Solid 2
The app runs on Solid 2 (`solid-js` 2.0.0-rc.8), which is still a release candidate — `latest` on npm
is 1.9.15. The whole front end moves together: `@solidjs/web` (the DOM layer, split out of `solid-js`,
and where `render` and the `JSX` types now live), `@solidjs/vite-plugin` 3 (`vite-plugin-solid`
renamed), `@tanstack/solid-router` 2 and `@tanstack/solid-query` 6, all of which peer on Solid 2.
`tsconfig.json` points `jsxImportSource` at `@solidjs/web`.

What the migration touched:
- `onMount` is gone; `onSettled` replaces it and takes the cleanup as its **return value** rather
  than through `onCleanup` (see `src/router.tsx`).
- `classList` is gone; `class` takes a string, an object or an array — **but only on native
  elements**. A component that forwards `class` to the DOM, like TanStack's `Link`, receives it
  verbatim, so an object arrives as `"[object Object]"`. `src/components/BoardCard.tsx` builds its
  class as a string for exactly that reason.
- Pseudo-boolean attributes (`draggable`, `aria-expanded`) take `"true"`/`"false"`, not booleans.

Some `@solid-primitives/*` packages under the router still declare `^1.x` peers, so `npm install`
prints one ERESOLVE warning; the tree dedupes to a single Solid 2 and the production bundle carries
no devtools.

## Getting the data back
The tracker's history is recovered from a dump, not replayed: `just backup` writes a timestamped
`pg_dump -Fc` of the whole database into `BACKUP_DIR` beside a counts sidecar, and the scheduled
`backup` service in `compose.yml` does the same on its own every `BACKUP_INTERVAL`. `just
backup-verify` proves the newest dump by restoring it into a scratch database and comparing its
ticket, decision and batch counts with that sidecar.
