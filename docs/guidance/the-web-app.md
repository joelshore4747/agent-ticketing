---
title: How the web app is written
kind: convention
weight: 10
source_paths: web/src
---
Solid 2 with `@solidjs/web`, TanStack solid-router and solid-query, plain CSS with the tokens in
`src/styles/tokens.css`. No component library, no CSS framework, no icon pack. The two typefaces are
bundled from `@fontsource` packages, imported in `main.tsx`; nothing is loaded from a CDN, so the app
looks the same offline.

**`JSX` types come from `@solidjs/web`, not `solid-js`.** They moved in Solid 2 and the old import
still resolves to something, so the error you get points at the wrong file.

**Solid's `class` composition only applies to native elements.** `class={['card', { active }]}` on a
`<div>` works; on a component — including router `Link` — it is forwarded verbatim and renders
`class="card,[object Object]"`. Build the string yourself when the target is a component.

**Colour means something or it is not used.** The board's column hues, the priority ramp, the
resolved note, and the inbox purple for anything waiting on the human are the only places colour
carries information; everything else is the tonal greys, tags and goal chips included. "Waiting on you" has one
home, the Needs you page, and its count; anything new that needs the human belongs on it, not in a
new colour somewhere else. A new accent is a claim that something new matters, so be able to say what.

**The API decides which moves are legal; the web only labels them.** Every ticket carries
`nextStatuses`, and the board and the status buttons offer exactly those. Do not keep a copy of the
transition table here: two copies drift, and the drift shows up as a drag accepted and then refused.

**A new keyboard shortcut gets a line in `ShortcutsDialog`.** The handlers live in the components
that own them; the `?` list is written by hand, so it only stays true if each shortcut adds itself.

**Small components, one file each.** The test of this codebase is whether someone can read the whole
app in one sitting. An abstraction that saves ten lines and costs a file is not worth it yet.

**Keyboard and 360px are not optional.** Real buttons and links, visible focus rings, forms submit on
Enter. This is easier to keep than to retrofit.

**A test that renders the app answers every query the rail makes.** The rail is on every page, so a
mock that returns junk for one of its queries (roadmaps, tags, batches, guidance, needs-you) breaks every page
test, not just the one about the rail. Add a branch per query rather than a catch-all default.

**A mock that answers 401 hides every page behind the login field.** Since TP-320 the status matters
as much as the body: one 401 flips `needsLogin` and the app renders `LoginGate` alone, so a page test
whose mock refuses a request fails on a missing element with nothing on screen but "Token". Mock the
status deliberately — `src/test/login.test.tsx` is the one test that wants the 401, and it asserts
both directions by keying the fake API on the `Authorization` header. And because `token` and
`needsLogin` are module-level signals seeded from `localStorage` at import, they outlive a render:
reset them in `beforeEach` with `logout()`, or a token one test stored decides what the next one
sees. Since TP-331 that `logout()` raises `needsLogin` as well as clearing the token, so it resets a
test to the gate, not to the app: a test that wants the app on screen follows it with `login()`,
passing the token its mock accepts, because `login()` is the only thing that lowers the signal again.

**The page tests mock the API, so they cannot see the schema.** A query asking for a field the API
no longer has passes every web test and fails in the browser. `just test` runs a Go test that
validates every document in `src/api/queries.ts` against the served schema — keep new queries in that
file, as `/* GraphQL */` template constants, so it finds them.

**Rule 27's "What you do" section is read in two places and parsed in one.** `src/lib/whatYouDo.ts`
pulls that section out of a description and mirrors the heading match in
`api/internal/service/needsjoel.go`, so the two change together; `TicketDetail` shows it above the
title and `NeedsYou` in each needs-joel box of its to-do list, both through `renderMarkdown` in
`src/lib/markdown.ts`, which is the app's only markdown parser. Do not add a second one.

**Needs you opens with the to-do list (TP-369).** What only the human can do (each tripwire, each
needs-joel ticket in any status, each checkpoint) is one box apiece at the top, with its tick; the
findings to triage are folded below it. A new kind of thing the human must do gets a box there, not
a new group under the findings, which is where the tripwires used to get lost.

**There is no formatter.** No prettier config exists, so running one rewrites whole files to its
defaults. Match the lines around your change by hand.

**Solid 2 applies signal writes on the next microtask.** In a test, `await` a tick between typing
into a field and clicking the button that reads it; a person cannot click inside the same microtask.

**The contract is the GraphQL SDL in `docs/api.md` and the brief is `docs/design.md`.** Do not invent a
field: build against the SDL. Requests go over `fetch` to `/graphql`, which the Vite dev proxy forwards
to the API on port 8080, through one small `src/api/client.ts` with a `gql(query, variables)` helper and
typed documents. There is no codegen.

**`client.ts` also holds the credential, and it is the only place that does.** Since TP-320 that file
owns the whole of login: a `token` signal seeded from `localStorage['ticketing-token']`, a `needsLogin`
signal, and `login()` / `logout()`. `gql()` attaches `Authorization: Bearer <token>` when there is one
and, on a 401, sets `needsLogin` and throws — which is how one refused request replaces the whole app
rather than breaking one page: `RootLayout` renders `LoginGate` instead of the rail and `Outlet` while
`needsLogin()` is true. Two things raise that signal — a 401 in `gql()`, and `logout()` since TP-331,
so the gate appears on the click; dropping the token alone used to hide the rail's "Log out" control
and nothing else, leaving every page up until some later request 401'd. `login()` is the only thing
that lowers it. Read the token through the signal, never from `localStorage` directly, or the
rail's "Log out" control will not know it changed. In local mode (the API started with no
`TICKETING_TOKENS`) none of it is reached: no request is refused, and the rail offers no "Log out"
without a token, so neither writer fires and development looks exactly as it did.

**`marked` is the only extra runtime dependency allowed.** Anything else is a question for the human
before it is a commit.

**Every surface gets one smoke test** with Vitest and `@solidjs/testing-library`, and `npm run build`
and `npm test` stay green with zero warnings. The dead-code check this stack has is
`grep -rn "export " web/src` with each name checked for an import somewhere; there is no tool for it.
