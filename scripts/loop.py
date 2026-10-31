#!/usr/bin/env python3
"""The loop's brain (goal 8, TP-125): read the tracker, say which agent acts next.

    python3 scripts/loop.py next  [--project KEY] [--roadmap ID] [--shell]  # one JSON step, or its fields
    python3 scripts/loop.py where [--project KEY] [--roadmap ID] [--shell]  # {project, roadmap, repo}, ditto
    python3 scripts/loop.py guidance <exit-code> [--shell]  # what a guidance-check report, on stdin, asks for

It holds no state of its own. The tracker is the state, so a loop that crashed or was stopped with
Ctrl-C picks up exactly where the record says it is. `decide` is a pure function of a snapshot, so
every branch is tested without an API (scripts/test_loop.py); `snapshot` is the only part that reads.

`where` is what a run settles before it starts (goal 7, TP-146): which project it works, which of
that project's roadmaps, and which checkout its agents run in. Naming no project means the API's
default — the oldest, worked on this checkout, which is what the loop always did. Naming one means
that project everywhere: its roadmap, its tripwires, its needs-joel stop, and its `repo_path` as the
working directory, because the loop has nowhere else to learn where that project's code is.

How much of Joel's plan is left is weighed before any of that (goal 8, TP-230). loop.sh records the
five-hour and seven-day windows after every step; `LOOP_USAGE_FILE` holds them and `LOOP_USAGE_LIMIT`
(default 0.6, a fraction) says how full a window may be. A spent week stops the run between steps,
because a batch cut off mid-ticket is the most expensive failure the loop has; a spent five-hour
window only slows it. No file reads as "usage unknown", which never stops a run.

Steps, in the order a batch needs them:
  strategist            the roadmap's next batch has no tickets yet, the Approver sent its plan back, or the
                        roadmap is complete and the next one has to be proposed; its reason names the
                        open lessons at PROMOTE_AT occurrences, which that plan must promote
  approver-accept       a plan has tickets waiting in triage
  scout-before          the batch requires reviews and has no before-review
  approver-guard        the next ticket is a refactor with no confirmed tripwire guarding it (rule 21)
  builder               the next ticket in order is work (backend-builder, or frontend-builder for web)
  doc-review            guidance went stale under the code before an eval: re-read it against the diff,
                        correct it, sync it and re-pin it (a builder, for the editing tools)
  evaluator             the next ticket in order is the eval
  scout-after           the work and the eval are done, the after-review is missing
  narrator              everything is done: write the report and finish the batch
  merge                 a finished batch's branch is not in main yet
  approver-checkpoint   a finished batch has no checkpoint
  wait                  the five-hour window is spent: sleep until `until`, then decide again
  stop                  the roadmap was checkpointed stop, a plan was sent back twice, the batch being worked
                        waits on Joel, or the seven-day window is spent
"""
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = os.environ.get("API", "http://localhost:8080")
CHECKOUT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))  # the loop's own repository
USAGE_LIMIT = 0.6  # the share of a rate-limit window a run may spend before it stops or waits
PROMOTE_AT = 3  # occurrences that make a lesson a ticket in the next plan (TP-187)
SHELL_FIELDS = {"where": ("project", "roadmap", "repo"),
                "next": ("step", "batch", "ticket", "agent", "until", "reason"),
                # A guidance verdict is about the step loop.sh already has in hand, so it carries no
                # batch or ticket of its own: those stay the evaluator step's.
                "guidance": ("step", "agent", "reason")}
# What a `guidance-check` line means to the loop. These are its own bookkeeping and fail nothing.
GUIDANCE_QUIET = ("ok", "unbound", "unversioned", "accept")


class Problem(Exception):
    """Something a run cannot start without; main prints it and exits non-zero."""


def shell_line(verb, answer):
    """One answer as the words loop.sh `read`s (TP-228): "-" for null, the reason last so it keeps its spaces."""
    return " ".join("-" if answer[f] is None else str(answer[f]) for f in SHELL_FIELDS[verb])


def step(name, reason, batch=None, ticket=None, agent=None, until=None):
    return {"step": name, "reason": reason, "batch": batch, "ticket": ticket,
            "agent": agent or name.split("-")[0], "until": until}


def usage_limit():
    """How full a rate-limit window may be, from LOOP_USAGE_LIMIT as a fraction. Read per call rather
    than at import so one setting is all Joel changes (`LOOP_USAGE_LIMIT=0.01 just loop --dry-run`);
    a value that is not a number falls back to the default, because a typo must not stop a run."""
    try:
        return float(os.environ.get("LOOP_USAGE_LIMIT") or USAGE_LIMIT)
    except ValueError:
        return USAGE_LIMIT


def spent(window, now):
    """How much of one window the file says is gone, as a fraction. A window whose `resets_at` has
    passed counts as 0: the figures were true when the last step saw them, and a window that has reset
    since is empty however full it was then."""
    window = window or {}
    resets_at = window.get("resets_at") or 0
    if resets_at and resets_at <= now:
        return 0.0
    return float(window.get("utilization") or 0)


def pct(fraction):
    return f"{round(fraction * 100)}%"


def clock(epoch):
    """A reset time as a human reads it: "Fri 2 Oct 09:00". Assembled rather than asked of strftime,
    whose day-without-padding format is platform-specific."""
    t = time.localtime(epoch)
    return f"{time.strftime('%a', t)} {t.tm_mday} {time.strftime('%b %H:%M', t)}"


def usage_guard(usage, now):
    """The step the plan's own usage asks for, or None when there is room (goal 8, TP-230).

    The seven-day window cannot be waited out inside a run, so a spent one stops the loop between
    steps rather than letting the limit cut a ticket in half. The five-hour window resets soon enough
    to sleep through, so a spent one becomes a `wait` step carrying the moment it frees, and the run
    slows down instead of ending. Nothing recorded means unknown, and unknown never stops a run.
    """
    if not usage:
        return None
    limit = usage_limit()
    week, five = spent(usage.get("seven_day"), now), spent(usage.get("five_hour"), now)
    if week >= limit:
        return step("stop", f"seven-day usage {pct(week)} is at or above LOOP_USAGE_LIMIT {pct(limit)}; "
                            + resets_in_words(usage.get("seven_day")))
    if five >= limit:
        # `until` is what loop.sh sleeps to. A window at the limit with no recorded reset time cannot
        # be waited out, and loop.sh's "same step twice" guard is what ends that run, loudly.
        return step("wait", f"five-hour usage {pct(five)} is at or above LOOP_USAGE_LIMIT {pct(limit)}; "
                            + resets_in_words(usage.get("five_hour")) + ", so this run waits for it",
                    until=(usage.get("five_hour") or {}).get("resets_at") or 0)
    return None


def resets_in_words(window):
    at = (window or {}).get("resets_at") or 0
    return f"it resets {clock(at)}" if at else "when it resets is not recorded"


def usage_note(usage, now):
    """What every step's reason ends with, so `just loop --dry-run` shows both windows beside the next
    step without asking anything of the API."""
    if not usage:
        return " (usage: unknown)"
    seen_at = usage.get("seen_at") or 0
    seen = f", seen {max(0, now - seen_at) // 60}m ago" if seen_at else ""
    return (f" (usage: 5h {pct(spent(usage.get('five_hour'), now))}, "
            f"week {pct(spent(usage.get('seven_day'), now))}{seen})")


def stale_doc(line):
    """The (slug, paths, pin) one STALE line names, or None when it is not shaped like one.

    guidance-check.sh writes `STALE   <slug>: <paths> changed since <pin> (now <newest>)`. The pin it
    prints is abbreviated, which git reads as happily as a full hash, so it can be handed to an agent
    as `git diff <pin>..HEAD -- <paths>` unchanged.
    """
    _, _, body = line.partition(" ")
    slug, named, rest = body.strip().partition(": ")
    paths, changed, since = rest.partition(" changed since ")
    pin = since.split(" ")[0] if changed else ""
    return (slug, paths, pin) if slug and named and paths and pin else None


def guidance_guard(code, report):
    """The step a `guidance-check` run asks for before an eval, or None when it is green (TP-258).

    Stale guidance is stale state exactly as a stale API is: a document whose code has moved under it
    is read and believed by every agent the claim hands it to. It stopped the loop in slots 5, 7 and 8,
    and each time a human ran the same routine — re-read the document against the diff, correct it,
    sync it, re-pin it. That is agent housekeeping rather than Joel's (`the-agents`), so a report whose
    every failing line is STALE becomes a `doc-review` step and the loop decides again afterwards.

    Any other failure stops the run. A served body that differs from its file is how served lines get
    deleted, and a baseline nobody ever set is an operator's job; a re-pin would bury both, and burying
    them is the one thing a doc review must not do. An unrecognised line is counted with those on
    purpose: a kind of failure this was not written for must be loud rather than quietly reviewed away.
    """
    if code == 0:
        return None
    stale, other = [], []
    for line in (report or "").splitlines():
        if not line.strip() or line[:1].isspace():
            continue  # the commits a STALE line lists, indented beneath it
        if line.split()[0] in GUIDANCE_QUIET:
            continue
        doc = stale_doc(line) if line.split()[0] == "STALE" else None
        if doc:
            stale.append(doc)
        else:
            other.append(" ".join(line.split()))
    if other or not stale:
        return step("stop", "the guidance check found what a doc review must not paper over: "
                            + ("; ".join(other) if other else
                               f"it exited {code} but named nothing a doc review can fix"))
    # A builder rather than the architecture scout, which owns the memory and would otherwise be the
    # fit: a role file's `tools:` line is its whole allow list (`the-agents`), and the scout's carries
    # no Edit or Write, so a scout sent to correct a document could not write the correction. The
    # builders' fallback list is the one that already holds the editing tools, git and `just`.
    return step("doc-review", "guidance went stale before the eval, so it is re-read first: "
                              + "; ".join(f"{slug} ({paths}, pinned {pin})" for slug, paths, pin in stale),
                agent="backend-builder")


def decide(s, now=None):
    """The next step for a snapshot: {"roadmap": [batches in sequence], "waiting_on_joel": [{key, batch_id,
    title, description}],
    "lessons": [{key, seen_count}], "usage": {five_hour, seven_day, seen_at} or None}.

    Each batch is {id, title, sequence, finished, checkpoint, reviews_required, reviews: [phases],
    tickets: [{key, type, status, title, description, area, decision_kind, decision_actor, guarded,
    awaits_joel}], next,
    in_progress, merged, lines_added, lines_deleted, test_lines_added, test_lines_deleted}.
    `guarded` is whether a refactor has a gate criterion linked to a confirmed tripwire; the line counts are the
    batch's own last measurement — the whole diff and the test half of it — and the cleanup check is
    failed on the difference, because the ceiling counts code only (TP-212).

    Usage is weighed first, because stopping or waiting is cheaper than any step it would allow. Then
    a needs-joel ticket stops the run, but only when it sits in the batch the loop would work next (the
    first not finished and checkpointed) and that batch's work has reached it (`reached`); any other is
    named in the step's reason and the run carries on, because Joel's instruction for a blocker not in
    the way is to ticket it and work on. Whatever is chosen, its reason ends with where the plan's
    usage stands.
    """
    now = int(time.time()) if now is None else now
    guard = usage_guard(s.get("usage"), now)
    if guard:
        return guard
    waiting = s.get("waiting_on_joel") or []
    nxt = next((b for b in s["roadmap"] if not (b["finished"] and b["checkpoint"])), None)
    blocking = [w["key"] for w in waiting if nxt and w.get("batch_id") == nxt["id"] and reached(w["key"], nxt)]
    if blocking:
        chosen = step("stop", "waiting on Joel: " + ", ".join(blocking) + " (tagged needs-joel)")
    else:
        chosen = next_step(s)
        if waiting:
            chosen["reason"] += " (waiting on Joel, not blocking: " + ", ".join(w["key"] for w in waiting) + ")"
    chosen["reason"] += usage_note(s.get("usage"), now)
    return chosen


def lesson_note(lessons):
    """The open lessons a plan must promote, by key (TP-187), or "" when none is due. A count only
    changes the next batch if the step that plans it is told: at PROMOTE_AT occurrences the pattern
    becomes a ticket for a mechanical check, or a guidance rule when no check is possible, and the
    lesson is closed when that ticket is done. Below the threshold nothing is named."""
    due = sorted(l["key"] for l in (lessons or []) if (l.get("seen_count") or 0) >= PROMOTE_AT)
    return f". Promote these lessons, seen {PROMOTE_AT}+ times: " + ", ".join(due) if due else ""


def unpromoted(b, lessons):
    """The due lesson keys a batch's plan does not cite (TP-286). A promotion is a ticket for the
    lesson, so its key belongs in a non-eval ticket's title or description; the eval names the batch,
    not the work it promotes, so citing it there would not be a promotion."""
    plan = " ".join(f"{t.get('title') or ''} {t.get('description') or ''}"
                    for t in (b or {}).get("tickets", []) if t["type"] != "eval")
    return sorted(l["key"] for l in (lessons or [])
                  if (l.get("seen_count") or 0) >= PROMOTE_AT and l["key"] not in plan)


def promotion_note(b, lessons):
    """What the Approver is told when the plan in front of it promotes none of the lessons due, or "" when
    there is nothing to say. `lesson_note` only asks the Strategist; nothing confirmed the plan carried it."""
    missing = unpromoted(b, lessons)
    return (f". This plan cites no ticket for lessons seen {PROMOTE_AT}+ times: " + ", ".join(missing)
            + "; send it back unless it promotes them") if missing else ""


def guard_note(b, waiting):
    """What the Approver is told about a refactor whose gate tripwire only Joel can confirm (TP-288,
    lesson TP-281). Two things must hold: it sits last before the eval, so the rest of the batch is
    worked while he is asked rather than behind him, and an open needs-joel ticket names that tripwire
    by id or test_ref, so the asking has actually been filed. Approver rule 7.4 says both and the
    lesson recurred three times anyway, so the plan is read for it instead of remembered."""
    work = [t for t in (b or {}).get("tickets", []) if t["type"] != "eval"]
    asked = " ".join(f"{w.get('title') or ''} {w.get('description') or ''}" for w in (waiting or []))
    missing = []
    for i, t in enumerate(work):
        if t["type"] != "refactor" or t.get("guarded", True) or not t.get("awaits_joel"):
            continue
        if i != len(work) - 1:
            missing.append(f"{t['key']} is not last before the eval")
        if not any(tid[:8] in asked or (ref and ref in asked) for tid, ref in t["awaits_joel"]):
            missing.append(f"{t['key']} has no open needs-joel ticket naming its tripwire")
    return (". This plan holds a refactor waiting on Joel that is not placed for it: " + "; ".join(missing)
            + "; send it back") if missing else ""


def approver_note(chosen, s):
    """What a plan up for acceptance is read for, and "" on any other step: the Approver is the one step
    that can send a plan back, so a gap between what was asked for and what was planned is named there."""
    if chosen["step"] != "approver-accept":
        return ""
    b = next((b for b in s["roadmap"] if b["id"] == chosen.get("batch")), None)
    return promotion_note(b, s.get("lessons")) + guard_note(b, s.get("waiting_on_joel"))


def reached(key, b):
    """Whether batch `b`'s work has got as far as ticket `key`: every ticket before it in the batch's
    order is finished. A batch is worked in order, so a needs-joel ticket placed after the work it
    judges (Joel approving the built pages, FS-48) is not in the way until that work is done; were it,
    the work it waits for could never start. A ticket the batch does not list is treated as reached."""
    keys = [t["key"] for t in b["tickets"]]
    if key not in keys:
        return True
    return all(t["status"] in ("done", "closed") for t in b["tickets"][:keys.index(key)])


def next_step(s):
    """The step the roadmap asks for, with the lessons due named on the two steps that can act on them:
    every strategist step (`lesson_note`) and the Approver's look at a new plan (`approver_note`)."""
    chosen = roadmap_step(s)
    chosen["reason"] += lesson_note(s.get("lessons")) if chosen["step"] == "strategist" else approver_note(chosen, s)
    return chosen


def roadmap_step(s):
    """The step the roadmap itself asks for, before needs-joel is weighed. See `decide`."""
    for b in s["roadmap"]:
        if b["finished"] and b["checkpoint"] in ("continue", "adjust"):
            if b.get("merged") is False:
                return step("merge", f"batch {b['sequence']} ({b['title']}) is finished but its branch is not in main", batch=b["id"])
            continue
        if b["finished"] and b["checkpoint"] == "stop":
            return step("stop", f"batch {b['sequence']} ({b['title']}) was checkpointed stop")
        work = [t for t in b["tickets"] if t["type"] != "eval"]
        evals = [t for t in b["tickets"] if t["type"] == "eval"]
        if not work:
            return step("strategist", f"batch {b['sequence']} ({b['title']}) has no tickets yet", batch=b["id"])
        sent_back = [t for t in evals if t.get("decision_kind") == "reframed" and (t.get("decision_actor") or "").startswith("approver")]
        if sent_back and any(t["status"] == "triage" for t in b["tickets"]):
            # One send-back is the Strategist's to fix; a second means the two disagree, and Joel decides.
            if sent_back[0].get("send_backs", 1) >= 2:
                return step("stop", f"the Approver sent batch {b['sequence']}'s plan back twice (see {sent_back[0]['key']}); Joel decides")
            return step("strategist", f"the Approver sent batch {b['sequence']}'s plan back (see {sent_back[0]['key']}): replan it", batch=b["id"])
        if any(t["status"] == "triage" for t in b["tickets"]):
            return step("approver-accept", f"batch {b['sequence']} has tickets waiting in triage", batch=b["id"], agent="approver")
        if b["reviews_required"] and "before" not in b["reviews"]:
            # No agent: the before-review is the stored memory plus the size of the diff since it, which
            # loop.sh records itself. Reading the code is the review step's, once the batch is built.
            return step("review-before", f"batch {b['sequence']} has no before-review", batch=b["id"], agent="loop")
        if not b["finished"]:
            current = b.get("in_progress") or b.get("next")
            if current:
                t = next(t for t in b["tickets"] if t["key"] == current)
                if t["type"] == "eval":
                    # One deep review per batch, before its eval: review the whole diff, fix and trim it,
                    # correct the guidance it moved, write the memory. Rigour paid once, not per ticket.
                    if b["reviews_required"] and "after" not in b["reviews"]:
                        return step("review", f"batch {b['sequence']}'s tickets are built: review and trim the whole diff before its eval",
                                    batch=b["id"], agent="reviewer")
                    # The eval judges the batch's growth itself (Joel, 2026-10-18): it accepts it with a
                    # cited reason or leaves the eval open naming the cut. A separate Approver step for
                    # it only ever stalled the batch at its eval (FS batches 2-4; slots 3, 4 and 7).
                    return step("evaluator", f"{current} is the eval ticket and next in order", batch=b["id"], ticket=current)
                if t["type"] == "refactor" and not t.get("guarded", True) and t.get("awaits_joel"):
                    return step("stop", f"waiting on Joel: {current} is guarded by a tripwire only he can confirm (see its needs-joel ticket)")
                if t["type"] == "refactor" and not t.get("guarded", True):
                    return step("approver-guard", f"{current} is a refactor with no confirmed tripwire guarding it", batch=b["id"], ticket=current, agent="approver")
                builder = "frontend-builder" if t.get("area") == "web" else "backend-builder"
                return step("builder", f"{current} is next in order", batch=b["id"], ticket=current, agent=builder)
            if any(t["status"] not in ("done", "closed") for t in b["tickets"]):
                return step("stop", f"batch {b['sequence']} has unfinished tickets that nothing can claim; get_batch says why")
            if b["reviews_required"] and "after" not in b["reviews"]:
                return step("review", f"batch {b['sequence']} is done but has no after-review", batch=b["id"], agent="reviewer")
            return step("narrator", f"batch {b['sequence']} is done and reviewed: write its report and finish it", batch=b["id"])
        if b.get("merged") is False:
            return step("merge", f"batch {b['sequence']} is finished but its branch is not in main", batch=b["id"])
        # The claims the digest found reported no usage, so the checkpoint is signed knowing what the cost figure leaves out (TP-296). Named, never refused.
        short = next((l[l.index("No usage"):] for l in (b.get("digest") or "").splitlines() if "No usage reported for" in l), "")
        return step("approver-checkpoint", f"batch {b['sequence']} is finished and waits for its checkpoint"
                    + (". " + short if short else ""), batch=b["id"], agent="approver")
    # A finished roadmap is not a reason to want the human (goal 8): proposing the next one is the
    # Strategist's job. Joel still steers, through the batch budget, the checkpoints and needs-joel.
    return step("strategist", "the roadmap is complete: propose the next roadmap (create_roadmap)")


def bearer():
    """The credential header this process sends (TP-316): TICKETING_TOKEN, which loop.sh exports as the
    `loop` role's, else that role's own file. Neither is local mode, where the API reads X-Actor."""
    file = pathlib.Path(os.environ.get("TICKETING_TOKEN_DIR") or "~/.config/ticketing/agent-tokens").expanduser() / "loop"
    found = os.environ.get("TICKETING_TOKEN") or (file.read_text(encoding="utf-8").strip() if file.exists() else "")
    return {"Authorization": "Bearer " + found} if found else {}


def get(path, **params):
    """GET one API path. Anything the API scopes by project takes project=KEY: without it every
    query answers for the default project, which is how the loop used to see only the oldest one."""
    params = {k: v for k, v in params.items() if v is not None}
    if params:
        path += ("&" if "?" in path else "?") + urllib.parse.urlencode(params)
    with urllib.request.urlopen(urllib.request.Request(API + path, headers=bearer())) as r:
        return json.load(r)


def pick_project(projects, key):
    """The project a run works: the one it named, or the API's own default. /api/projects comes back
    ordered by created_at, and ResolveProject falls back to the oldest, so the first row is it."""
    if not projects:
        raise Problem("the API has no projects")
    if not key:
        return projects[0]
    for p in projects:
        if p["key"].upper() == key.upper():
            return p
    raise Problem(f"no project {key}: the API has " + ", ".join(p["key"] for p in projects))


def pick_roadmap(roadmaps, key):
    """A project's live roadmap: the newest it has not archived. A project works one at a time and
    the next succeeds the last, so the newest is the one a run without --roadmap means."""
    live = [r for r in roadmaps if not r.get("archived_at")]
    if not live:
        raise Problem(f"project {key} has no roadmap to work: create one (create_roadmap) first")
    return max(live, key=lambda r: r["created_at"])["id"]


def pick_repo(project, repo_env, named):
    """The checkout the agents run in. REPO wins, so a demo can point the loop at a scratch clone;
    then the project's repo_path. A run that named no project is working here, on this checkout; a
    run that named one has nowhere but repo_path to learn where that project's code is."""
    if repo_env:
        return repo_env
    if project.get("repo_path"):
        return project["repo_path"]
    if named:
        raise Problem(f"project {project['key']} has no repo_path, so there is no checkout to run its "
                      "agents in: set one when creating the project (docs/api.md) or pass REPO=<path>")
    return CHECKOUT


def where(project_key=None, roadmap_id=None, repo_env=None):
    """What a run settles before it starts: its project, that project's roadmap, and its checkout."""
    project = pick_project(get("/api/projects"), project_key)
    repo = pick_repo(project, repo_env, named=bool(project_key))  # before the roadmap: connecting a
    return {                                                      # project is where that goes wrong
        "project": project["key"],
        "roadmap": roadmap_id or pick_roadmap(get("/api/roadmaps", project=project["key"]), project["key"]),
        "repo": repo,
    }


def merged(ref, repo):
    """Whether a batch branch is in main. A branch that no longer exists counts as merged, and so does
    one whose every change is in main under another hash (history re-signed or rebased): `git cherry`
    marks those with '-' by patch id."""
    if not ref:
        return True
    exists = subprocess.run(["git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/" + ref], capture_output=True)
    if exists.returncode != 0:
        return True
    if subprocess.run(["git", "-C", repo, "merge-base", "--is-ancestor", ref, "main"], capture_output=True).returncode == 0:
        return True
    cherry = subprocess.run(["git", "-C", repo, "cherry", "main", ref], capture_output=True, text=True)
    return cherry.returncode == 0 and not any(line.startswith("+") for line in cherry.stdout.splitlines())


def read_usage(path=None):
    """The plan's windows as loop.sh last recorded them (LOOP_USAGE_FILE, TP-229), or None.

    The file lives outside any one run's directory, so a fresh run and a dry run — which start no agent
    and so learn nothing themselves — still read the last figures. Missing, unreadable or half-written
    all come back None, which reads as "usage unknown": not knowing must never stop a run.
    """
    path = path or os.environ.get("LOOP_USAGE_FILE")
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as f:
            found = json.load(f)
    except (OSError, ValueError):
        return None
    return found if isinstance(found, dict) and found else None


def snapshot(w):
    """Read the tracker for one run's project, roadmap and checkout (what `where` returned)."""
    project, repo = w["project"], w["repo"]
    # Every query names the project. A roadmap or a batch is fetched by id, but the API still reads
    # it within a project and answers 404 for another project's id, so the name is not decoration.
    roadmap = get(f"/api/roadmaps/{w['roadmap']}", project=project)
    tripwires = {tw["id"]: tw for tw in get("/api/tripwires", project=project)}
    confirmed = {tid for tid, tw in tripwires.items() if tw.get("confirmed_at")}
    batches = []
    for entry in roadmap["batches"]:
        b = entry.get("batch", entry)
        detail = get(f"/api/batches/{b['id']}", project=project)
        reviews = [r["phase"] for r in get(f"/api/batches/{b['id']}/reviews", project=project)]
        tickets = []
        for t in detail["tickets"]:
            full = get(f"/api/tickets/{t['key']}", project=project)
            current = next((d for d in full.get("decisions", []) if d["id"] == full.get("decision_id")), None)
            tickets.append({"key": t["key"], "type": t["type"], "status": t["status"],
                            # What the plan says, so a promotion can be looked for in it (TP-286).
                            "title": t.get("title") or "", "description": t.get("description") or "",
                            "area": (t.get("area") or {}).get("name"),
                            "decision_kind": current and current["kind"], "decision_actor": current and current["actor"],
                            "send_backs": sum(1 for d in full.get("decisions", []) if d["kind"] == "reframed" and d["actor"].startswith("approver")),
                            "guarded": any(c["tier"] == "gate" and c.get("tripwire_id") in confirmed for c in full.get("criteria", [])),
                            # The gates linked to a tripwire only Joel can confirm: nothing an agent can do.
                            # Each as (id, test_ref), because the needs-joel ticket that asks him may
                            # name either (TP-288); empty is the ticket nothing waits on.
                            "awaits_joel": [[c["tripwire_id"], tripwires.get(c["tripwire_id"], {}).get("test_ref") or ""]
                                            for c in full.get("criteria", [])
                                            if c["tier"] == "gate" and c.get("tripwire_id") and c["tripwire_id"] not in confirmed]})
        progress = detail.get("progress") or {}
        batches.append({
            "id": b["id"], "title": b["title"], "sequence": b["sequence"], "finished": b["finished_at"] is not None,
            "checkpoint": b.get("checkpoint_decision"), "reviews_required": b.get("reviews_required", False),
            "reviews": reviews, "tickets": tickets, "next": progress.get("next"),
            "lines_added": b.get("lines_added"), "lines_deleted": b.get("lines_deleted"), "digest": b.get("digest") or "",
            "test_lines_added": b.get("test_lines_added"), "test_lines_deleted": b.get("test_lines_deleted"),
            "in_progress": next((t["key"] for t in tickets if t["status"] == "in_progress"), None),
            "merged": merged(b.get("git_ref"), repo) if b["finished_at"] else None,
        })
    # Only this project's tickets: another project's needs-joel is not this run's business to wait on.
    # Each carries its batch, because only one in the batch being worked next stops the run.
    # What each says, so the tripwire it asks about can be looked for in it (TP-288).
    waiting = [{"key": t["key"], "batch_id": t.get("batch_id"),
                "title": t.get("title") or "", "description": t.get("description") or ""}
               for t in get("/api/tickets", tag="needs-joel", limit=100, project=project)["items"]
               if t["status"] not in ("done", "closed")]
    # Lessons are counted records (TP-186), and a count acts only if the planning step is told (TP-187).
    lessons = [{"key": t["key"], "seen_count": t.get("seen_count") or 0}
               for t in get("/api/tickets", tag="lesson", limit=100, project=project)["items"]
               # An AI-filed lesson waits in triage for a human; its count is real there too (TP-285).
               if t["status"] in ("open", "triage")]
    # The one input that is not the tracker's: how much of Joel's plan is left, which only the CLI knows
    # and only the last step's log carried. Read from a file rather than asked for, so it costs nothing.
    return {"roadmap": sorted(batches, key=lambda b: b["sequence"]), "waiting_on_joel": waiting,
            "lessons": lessons, "usage": read_usage()}


def main(argv):
    if len(argv) < 2 or argv[1] not in ("next", "where", "guidance"):
        sys.exit(__doc__)
    if argv[1] == "guidance":
        # The check is a command with side effects, so loop.sh runs it and this only reads what it
        # said: the report on stdin, its exit status as the argument. An argument that is not a number
        # reads as a failure, which is the safe direction — and nothing is printed when it is green,
        # so loop.sh's `read` leaves the evaluator step it already has in hand alone.
        answer = guidance_guard(int(argv[2]) if len(argv) > 2 and argv[2].isdigit() else 1, sys.stdin.read())
        if answer:
            print(shell_line("guidance", answer) if "--shell" in argv else json.dumps(answer))
        return
    flag = lambda name, fallback: argv[argv.index(name) + 1] if name in argv else fallback
    try:
        w = where(flag("--project", os.environ.get("LOOP_PROJECT")),
                  flag("--roadmap", os.environ.get("LOOP_ROADMAP")),
                  os.environ.get("REPO"))
        answer = w if argv[1] == "where" else decide(snapshot(w))
        print(shell_line(argv[1], answer) if "--shell" in argv else json.dumps(answer))
    # An HTTPError is the same kind of thing as a Problem: a run cannot start, and a 401 — the loop with
    # no credential of its own once login is on (TP-316) — deserves saying rather than a traceback.
    except (Problem, urllib.error.HTTPError) as problem:
        sys.exit(f"loop: {problem}")


if __name__ == "__main__":
    main(sys.argv)
