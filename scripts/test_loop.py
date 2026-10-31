"""Unit tests for the loop's brain: every step `decide` can name, from a snapshot (TP-125), the
prompt loop.sh hands the agent for one (TP-185), the project, roadmap and checkout a run settles on
before it starts (TP-146), what `loop.sh --dry-run` itself prints (TP-231), the `--shell` fields
loop.sh reads in place of parsing JSON (TP-228), and the spending guard that stops or waits on how
much of Joel's plan is left (TP-230)."""
import collections
import contextlib
import copy
import http.server
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))
import loop  # noqa: E402
import stream_usage  # noqa: E402
from loop import Problem, decide, shell_line, where  # noqa: E402


def ticket(key, typ="chore", status="open", area="api", kind=None, actor=None, guarded=None,
           title="", description="", awaits=None):
    t = {"key": key, "type": typ, "status": status, "area": area, "decision_kind": kind, "decision_actor": actor,
         "title": title, "description": description}
    if guarded is not None:
        t["guarded"] = guarded
    if awaits is not None:
        # (tripwire id, test_ref) per unconfirmed gate tripwire, as the snapshot carries them (TP-288).
        t["awaits_joel"] = awaits
    return t


def batch(seq=1, tickets=None, finished=False, checkpoint=None, reviews=("before",), nxt=None, in_progress=None,
          merged=None, required=True, added=0, deleted=0, test_added=None, test_deleted=None):
    """`added` and `deleted` are the batch's own last size measurement — the whole diff — and
    `test_added`/`test_deleted` how much of it was tests, which the cleanup check does not charge
    (TP-212). None for both is a batch measured before the split."""
    return {"id": f"b{seq}", "title": f"Batch {seq}", "sequence": seq, "finished": finished, "checkpoint": checkpoint,
            "reviews_required": required, "reviews": list(reviews), "tickets": tickets or [],
            "next": nxt, "in_progress": in_progress, "merged": merged,
            "lines_added": added, "lines_deleted": deleted,
            "test_lines_added": test_added, "test_lines_deleted": test_deleted}


def snap(*batches, waiting=(), usage=None, lessons=()):
    """`waiting` is the needs-joel tickets, each as (key, batch_id) or (key, batch_id, what it says); `lessons` the open lesson tickets,
    each as (key, seen_count); `usage` is what LOOP_USAGE_FILE held, or None for a loop that has never
    recorded a window."""
    return {"roadmap": list(batches),
            "waiting_on_joel": [{"key": k, "batch_id": b, "title": k, "description": text}
                                for k, b, *rest in waiting for text in [rest[0] if rest else ""]],
            "lessons": [{"key": k, "seen_count": n} for k, n in lessons], "usage": usage}


WORK = [ticket("TP-1"), ticket("TP-2", area="web"), ticket("TP-3", typ="eval")]


def done(ts):
    out = copy.deepcopy(ts)
    for t in out:
        t["status"] = "done"
    return out


class Decide(unittest.TestCase):
    def assertStep(self, s, name, **fields):
        got = decide(s)
        self.assertEqual(got["step"], name, got["reason"])
        for k, v in fields.items():
            self.assertEqual(got[k], v, got)
        self.assertTrue(got["reason"])
        return got

    def test_an_unplanned_batch_goes_to_the_strategist(self):
        self.assertStep(snap(batch(tickets=[ticket("TP-9", typ="eval", status="triage")])), "strategist", batch="b1")

    def test_a_plan_in_triage_goes_to_the_approver(self):
        ts = [ticket("TP-1", status="triage"), ticket("TP-3", typ="eval", status="triage")]
        self.assertStep(snap(batch(tickets=ts, reviews=())), "approver-accept", agent="approver")

    def test_a_plan_sent_back_once_goes_back_to_the_strategist(self):
        ts = [ticket("TP-1", status="triage"), ticket("TP-3", typ="eval", status="triage", kind="reframed", actor="approver")]
        self.assertIn("replan", self.assertStep(snap(batch(tickets=ts)), "strategist", batch="b1")["reason"])

    def test_a_replanned_plan_goes_back_to_the_approver(self):
        ts = [ticket("TP-1", status="triage"), ticket("TP-3", typ="eval", status="triage", kind="accepted", actor="strategist")]
        self.assertStep(snap(batch(tickets=ts)), "approver-accept")

    def test_a_plan_sent_back_twice_stops_for_joel(self):
        ev = ticket("TP-3", typ="eval", status="triage", kind="reframed", actor="approver")
        ev["send_backs"] = 2
        self.assertIn("twice", self.assertStep(snap(batch(tickets=[ticket("TP-1", status="triage"), ev])), "stop")["reason"])

    def test_an_accepted_batch_without_a_before_review_has_it_recorded_by_the_loop_not_an_agent(self):
        self.assertStep(snap(batch(tickets=WORK, reviews=(), nxt="TP-1")), "review-before", agent="loop", batch="b1")

    def test_a_built_batch_is_reviewed_and_trimmed_once_before_its_eval(self):
        # Lean harness: the rigour is paid once per batch, by the reviewer, before the evaluator judges it.
        ts = done(WORK[:2]) + [WORK[2]]
        self.assertStep(snap(batch(tickets=ts, nxt="TP-3")), "review", agent="reviewer", batch="b1")
        ts = done(WORK[:2]) + [ticket("TP-3", typ="eval")]
        self.assertStep(snap(batch(tickets=ts, nxt="TP-3", added=700, deleted=100)), "review", agent="reviewer")

    def test_a_batch_planned_before_reviews_skips_them(self):
        self.assertStep(snap(batch(tickets=WORK, reviews=(), nxt="TP-1", required=False)), "builder", ticket="TP-1")

    def test_work_goes_to_the_builder_for_its_area(self):
        self.assertStep(snap(batch(tickets=WORK, nxt="TP-1")), "builder", ticket="TP-1", agent="backend-builder")
        self.assertStep(snap(batch(tickets=WORK, nxt="TP-2")), "builder", ticket="TP-2", agent="frontend-builder")

    def test_an_unguarded_refactor_goes_to_the_approver_first(self):
        ts = [ticket("TP-1", typ="refactor", guarded=False), ticket("TP-3", typ="eval")]
        self.assertStep(snap(batch(tickets=ts, nxt="TP-1")), "approver-guard", ticket="TP-1", agent="approver")
        ts[0]["guarded"] = True
        self.assertStep(snap(batch(tickets=ts, nxt="TP-1")), "builder", ticket="TP-1", agent="backend-builder")

    def test_a_refactor_whose_guard_only_joel_can_confirm_stops_the_run_naming_it(self):
        ts = [ticket("TP-1", typ="refactor", guarded=False), ticket("TP-3", typ="eval")]
        ts[0]["awaits_joel"] = [["4d84daf1-a599-47ee-bb89-6fc5038d3fe2", "app_test.go:TestX"]]
        self.assertIn("waiting on Joel", self.assertStep(snap(batch(tickets=ts, nxt="TP-1")), "stop")["reason"])

    def test_a_ticket_already_in_progress_is_resumed_first(self):
        ts = copy.deepcopy(WORK)
        ts[1]["status"] = "in_progress"
        self.assertStep(snap(batch(tickets=ts, nxt=None, in_progress="TP-2")), "builder", ticket="TP-2")

    def test_the_eval_ticket_goes_to_the_evaluator(self):
        ts = done(WORK[:2]) + [WORK[2]]
        self.assertStep(snap(batch(tickets=ts, nxt="TP-3", reviews=("before", "after"))), "evaluator", ticket="TP-3", agent="evaluator")

    def test_a_grown_batch_at_its_eval_goes_straight_to_the_evaluator_which_judges_the_growth(self):
        # Joel, 2026-10-18: the Approver no longer has to accept growth. A separate approver-size step
        # stalled FS batches 2-4 at their evals; the evaluator now judges it with the measurement.
        ts = done(WORK[:2]) + [ticket("TP-3", typ="eval")]
        for added, deleted in ((700, 100), (0, 0), (40, 700)):  # grown, unmeasured, smaller
            self.assertStep(snap(batch(tickets=ts, nxt="TP-3", reviews=("before", "after"), added=added, deleted=deleted)),
                            "evaluator", ticket="TP-3", agent="evaluator")

    def test_done_work_without_an_after_review_goes_to_the_reviewer(self):
        self.assertStep(snap(batch(tickets=done(WORK))), "review", agent="reviewer")

    def test_done_and_reviewed_goes_to_the_narrator(self):
        self.assertStep(snap(batch(tickets=done(WORK), reviews=("before", "after"))), "narrator", batch="b1")

    def test_stuck_tickets_stop_the_run_rather_than_spin(self):
        ts = copy.deepcopy(WORK)
        ts[0]["status"] = "open"
        self.assertStep(snap(batch(tickets=ts, nxt=None)), "stop")

    def test_a_finished_batch_is_merged_then_checkpointed(self):
        b = batch(tickets=done(WORK), reviews=("before", "after"), finished=True, merged=False)
        self.assertStep(snap(b), "merge", batch="b1")
        b["merged"] = True
        self.assertStep(snap(b), "approver-checkpoint", agent="approver")

    def test_the_checkpoint_reason_names_the_claims_that_reported_no_usage(self):
        # A step run from a supervising session posts none (rule 23), and slot 13's digest measured 0
        # of 6 claims, so the Approver signed a cost figure without knowing what it left out (TP-296,
        # lesson TP-282). Named, never refused: the checkpoint is not withheld over it.
        b = batch(tickets=done(WORK), reviews=("before", "after"), finished=True, merged=True)
        b["digest"] = "## Cost\n\nTokens: 1 input, from 1 of 3 claims. No usage reported for TP-2, TP-3.\n"
        got = decide(snap(b))
        self.assertEqual(got["step"], "approver-checkpoint")
        self.assertIn("No usage reported for TP-2, TP-3.", got["reason"])
        self.assertNotIn("Tokens:", got["reason"])  # that one line, not the digest around it
        del b["digest"]
        self.assertNotIn("No usage", decide(snap(b))["reason"])

    def test_a_checkpointed_batch_hands_on_to_the_next(self):
        first = batch(1, tickets=done(WORK), reviews=("before", "after"), finished=True, checkpoint="continue", merged=True)
        self.assertStep(snap(first, batch(2, tickets=[ticket("TP-7", typ="eval", status="triage")])), "strategist", batch="b2")

    def test_a_checkpointed_batch_whose_branch_is_not_merged_is_merged_first(self):
        first = batch(1, tickets=done(WORK), finished=True, checkpoint="continue", merged=False)
        self.assertStep(snap(first, batch(2)), "merge", batch="b1")

    def test_a_stop_checkpoint_still_stops_the_run(self):
        # Only a roadmap Joel stopped ends the loop; a finished one does not (TP-183, below).
        self.assertIn("stop", self.assertStep(snap(batch(tickets=done(WORK), finished=True, checkpoint="stop")), "stop")["reason"])

    def test_a_complete_roadmap_asks_the_strategist_for_the_next_one(self):
        b = batch(tickets=done(WORK), finished=True, checkpoint="continue", merged=True)
        got = self.assertStep(snap(b), "strategist", batch=None, ticket=None, agent="strategist")
        self.assertIn("create_roadmap", got["reason"])

    def test_waiting_on_joel_inside_the_batch_being_worked_stops_the_run_first(self):
        s = snap(batch(tickets=WORK, nxt="TP-1"), waiting=[("TP-5", "b1")])
        self.assertIn("TP-5", self.assertStep(s, "stop")["reason"])

    def test_waiting_on_joel_later_in_the_batchs_order_is_not_in_the_way_until_the_work_reaches_it(self):
        # FS batch 4 (2026-10-16): Joel approves the copy only once the pages are built. Stopping on
        # that ticket from the start meant the pages could never be built.
        approve = ticket("TP-4")
        ts = [ticket("TP-1"), ticket("TP-2"), approve, ticket("TP-3", typ="eval")]
        got = self.assertStep(snap(batch(tickets=ts, nxt="TP-1"), waiting=[("TP-4", "b1")]), "builder", ticket="TP-1")
        self.assertIn("(waiting on Joel, not blocking: TP-4)", got["reason"])
        reached_it = done(ts[:2]) + ts[2:]
        self.assertIn("TP-4", self.assertStep(snap(batch(tickets=reached_it, nxt="TP-4"), waiting=[("TP-4", "b1")]), "stop")["reason"])

    def test_a_plan_in_triage_with_a_needs_joel_ticket_still_goes_to_the_approver(self):
        ts = [ticket("TP-1", status="triage"), ticket("TP-4", status="triage"), ticket("TP-3", typ="eval", status="triage")]
        self.assertStep(snap(batch(tickets=ts, reviews=()), waiting=[("TP-4", "b1")]), "approver-accept")

    def test_waiting_on_joel_outside_it_is_reported_and_the_run_carries_on(self):
        # Joel's standing instruction: ticket what needs him and evaluate something else. A blocker
        # in a later batch, or in none, is not in this step's way.
        done_one = batch(tickets=done(WORK), finished=True, checkpoint="continue", merged=True)
        s = snap(done_one, batch(seq=2, tickets=WORK, nxt="TP-1"), waiting=[("TP-5", "b1"), ("TP-6", None)])
        got = self.assertStep(s, "builder", ticket="TP-1")
        self.assertIn("(waiting on Joel, not blocking: TP-5, TP-6)", got["reason"])


NOW = 1790000000  # a fixed clock, so "seen 2m ago" and a reset time read the same whenever these run
FIXTURE_LOG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "testdata", "usage-windows.jsonl")


def windows(five=0.0, week=0.0, five_resets=NOW + 3600, week_resets=NOW + 86400, seen=NOW - 120):
    """What loop.sh records into LOOP_USAGE_FILE after a step: both windows, and when they were seen."""
    return {"five_hour": {"utilization": five, "resets_at": five_resets},
            "seven_day": {"utilization": week, "resets_at": week_resets}, "seen_at": seen}


def drop_env(test, *names):
    """Run a test as a fresh shell would: these variables unset for it, and back as they were after,
    so a run of these tests from inside a real loop still starts from nothing."""
    for name in names:
        old = os.environ.pop(name, None)
        if old is not None:
            test.addCleanup(os.environ.__setitem__, name, old)
        test.addCleanup(os.environ.pop, name, None)  # LIFO: this undoes what a test set, then the above restores


class Usage(unittest.TestCase):
    """The spending guard (TP-230): how much of Joel's plan is left, weighed before the roadmap gets a
    say. A batch cut off mid-ticket by the limit is the most expensive failure the loop has, so the
    week stops the run between steps and the five-hour window, which resets soon, only waits.
    """

    def setUp(self):
        drop_env(self, "LOOP_USAGE_LIMIT")

    def decide_with(self, usage, **kw):
        return decide(snap(batch(tickets=WORK, nxt="TP-1"), usage=usage, **kw), now=NOW)

    def test_a_week_below_the_limit_works_and_every_reason_shows_both_windows(self):
        got = self.decide_with(windows(five=0.1, week=0.59))
        self.assertEqual(got["step"], "builder")
        self.assertIn("(usage: 5h 10%, week 59%, seen 2m ago)", got["reason"])

    def test_a_week_at_the_limit_stops_the_run_naming_the_number_and_the_reset(self):
        got = self.decide_with(windows(five=0.1, week=0.6))
        self.assertEqual(got["step"], "stop")
        self.assertIn("seven-day usage 60% is at or above LOOP_USAGE_LIMIT 60%", got["reason"])
        self.assertIn(loop.clock(NOW + 86400), got["reason"])
        self.assertIsNone(got["until"])  # nothing to wait for: the week cannot be slept through

    def test_a_reset_time_is_named_in_words_a_human_reads(self):
        self.assertRegex(loop.clock(NOW + 86400), r"^[A-Z][a-z][a-z] \d\d? [A-Z][a-z][a-z] \d\d:\d\d$")

    def test_a_spent_five_hour_window_waits_until_it_resets(self):
        got = self.decide_with(windows(five=0.85, week=0.3))
        self.assertEqual((got["step"], got["until"]), ("wait", NOW + 3600))
        self.assertIn("five-hour usage 85% is at or above LOOP_USAGE_LIMIT 60%", got["reason"])
        self.assertIn(loop.clock(NOW + 3600), got["reason"])

    def test_a_spent_week_stops_even_when_the_five_hour_window_would_only_wait(self):
        self.assertEqual(self.decide_with(windows(five=0.99, week=0.9))["step"], "stop")

    def test_a_five_hour_window_whose_reset_has_passed_counts_as_empty(self):
        # The file holds what the last step saw, and that step may be days old: a window that has reset
        # since is empty however full it was then. Reading it as spent would sleep for nothing.
        got = self.decide_with(windows(five=0.9, five_resets=NOW - 60))
        self.assertEqual(got["step"], "builder")
        self.assertIn("5h 0%", got["reason"])

    def test_a_week_whose_reset_has_passed_does_not_stop_the_run_either(self):
        self.assertEqual(self.decide_with(windows(week=0.95, week_resets=NOW - 1))["step"], "builder")

    def test_no_usage_at_all_is_unknown_and_never_stops_a_run(self):
        got = self.decide_with(None)
        self.assertEqual(got["step"], "builder")
        self.assertIn("(usage: unknown)", got["reason"])

    def test_the_threshold_is_one_setting_joel_can_change(self):
        os.environ["LOOP_USAGE_LIMIT"] = "0.01"
        got = self.decide_with(windows(week=0.02))
        self.assertEqual(got["step"], "stop")
        self.assertIn("LOOP_USAGE_LIMIT 1%", got["reason"])

    def test_a_threshold_that_is_not_a_number_falls_back_rather_than_stopping_a_run(self):
        os.environ["LOOP_USAGE_LIMIT"] = "eighty percent"
        self.assertEqual(self.decide_with(windows(week=0.59))["step"], "builder")

    def test_a_needs_joel_stop_still_wins_in_its_own_batch(self):
        got = decide(snap(batch(tickets=WORK, nxt="TP-1"), waiting=[("TP-5", "b1")],
                          usage=windows(five=0.1, week=0.2)), now=NOW)
        self.assertEqual(got["step"], "stop")
        self.assertIn("TP-5", got["reason"])
        self.assertIn("week 20%", got["reason"])  # and it still says where the plan stands


class ReadUsage(unittest.TestCase):
    """LOOP_USAGE_FILE as loop.py reads it back. The file here is written by stream_usage itself
    (TP-229's writer), so the writer and the reader of the guard's only input cannot drift apart."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.path = os.path.join(self.dir, "usage-windows.json")
        drop_env(self, "LOOP_USAGE_FILE")

    def test_what_stream_usage_writes_is_what_the_guard_reads_and_says(self):
        written = stream_usage.write_windows(FIXTURE_LOG, self.path)
        got = loop.read_usage(self.path)
        self.assertEqual(got, written)
        before_either_reset = min(got["five_hour"]["resets_at"], got["seven_day"]["resets_at"]) - 60
        self.assertIsNone(loop.usage_guard(got, before_either_reset))  # the fixture is 2% and 38%
        self.assertIn("5h 2%, week 38%", loop.usage_note(got, before_either_reset))

    def test_a_missing_file_is_unknown(self):
        self.assertIsNone(loop.read_usage(self.path))

    def test_a_half_written_file_is_unknown_rather_than_a_crash(self):
        with open(self.path, "w") as f:
            f.write('{"five_hour": {"utiliz')
        self.assertIsNone(loop.read_usage(self.path))

    def test_no_file_named_at_all_is_unknown(self):
        self.assertIsNone(loop.read_usage())

    def test_the_path_comes_from_the_environment_so_loop_sh_and_loop_py_share_one_file(self):
        stream_usage.write_windows(FIXTURE_LOG, self.path)
        os.environ["LOOP_USAGE_FILE"] = self.path
        self.assertEqual(loop.read_usage()["seven_day"]["utilization"], 0.38)


# Real `guidance-check.sh` output, which is the only writer of this format. The indented lines under a
# STALE one are the commits it lists; the quiet kinds either side of it are the check's own bookkeeping.
GREEN_GUIDANCE = """ok      before-you-finish
unbound key-goals (no source_paths)
unversioned the-web-app: web/src are here but have no commits yet
ok      capturing-patterns
"""
STALE_GUIDANCE = """ok      before-you-finish
STALE   the-system: scripts/loop.py scripts/loop.sh .claude/agents changed since 24101fc9 (now 379722e9)
          379722e9 TP-257: the loop restarts the dev API when it is behind the checkout
unbound key-goals (no source_paths)
STALE   the-agents: .claude/agents api/internal/app/agents_test.go changed since 24101fc9 (now 379722e9)
          379722e9 TP-257: the loop restarts the dev API when it is behind the checkout
"""
DRIFTED_GUIDANCE = """ok      before-you-finish
STALE   the-system: scripts/loop.py changed since 24101fc9 (now 379722e9)
DRIFT   the-agents: file lacks 3 served lines, has 0 the server lacks
"""


class StaleGuidance(unittest.TestCase):
    """What a `guidance-check` report asks the loop to do before an eval (TP-258).

    Stale guidance stopped the loop in slots 5, 7 and 8, and a human ran the same routine every time.
    These fix the three answers the loop may give, because each one is a different kind of mistake:
    reviewing away a DRIFT line deletes served text, stopping on a STALE line is the halt this ticket
    exists to remove, and running the eval on either judges the batch against a document nobody has read.
    """

    def test_guidance_that_is_not_stale_asks_for_nothing_and_the_evaluator_runs(self):
        self.assertIsNone(loop.guidance_guard(0, GREEN_GUIDANCE))

    def test_a_stale_only_report_becomes_a_doc_review_rather_than_stopping_the_run(self):
        got = loop.guidance_guard(1, STALE_GUIDANCE)
        self.assertEqual(got["step"], "doc-review")
        # The builders' fallback list is the one carrying Edit and Write; a role with a `tools:` line
        # (its whole allow list) may have neither, and could not write the correction.
        self.assertEqual(got["agent"], "backend-builder")
        self.assertIn("the-system", got["reason"])
        self.assertIn("the-agents", got["reason"])

    def test_a_stale_line_hands_on_the_pin_and_the_paths_the_diff_needs(self):
        # Without both, the agent has no way to read what changed under the document and would be
        # left re-reading the whole corpus — the cost the memory exists to avoid.
        reason = loop.guidance_guard(1, STALE_GUIDANCE)["reason"]
        self.assertIn("the-system (scripts/loop.py scripts/loop.sh .claude/agents, pinned 24101fc9)", reason)
        self.assertIn("the-agents (.claude/agents api/internal/app/agents_test.go, pinned 24101fc9)", reason)

    def test_a_report_that_is_not_only_stale_stops_the_run_and_names_the_line(self):
        got = loop.guidance_guard(1, DRIFTED_GUIDANCE)
        self.assertEqual(got["step"], "stop")
        # Squeezed to single spaces, because the reason is one shell field that has to survive `read`.
        self.assertIn("DRIFT the-agents: file lacks 3 served lines", got["reason"])
        self.assertNotIn("the-system", got["reason"])  # the stale one is not the reason it stopped

    def test_an_unreviewed_document_stops_the_run_rather_than_being_re_pinned(self):
        # A baseline nobody set is an operator's job, and a doc review's re-pin would bury it.
        got = loop.guidance_guard(1, "unreviewed the-system: no baseline commit — run 'just guidance-check --accept'\n")
        self.assertEqual(got["step"], "stop")

    def test_a_kind_of_failure_this_was_not_written_for_stops_rather_than_being_reviewed_away(self):
        got = loop.guidance_guard(1, "WEDGED  the-system: something nobody has thought of yet\n")
        self.assertEqual(got["step"], "stop")
        self.assertIn("WEDGED", got["reason"])

    def test_a_check_that_failed_without_naming_a_stale_document_stops(self):
        # curl could not reach the API, the script died: exit 1 and nothing a doc review can act on.
        got = loop.guidance_guard(1, "")
        self.assertEqual(got["step"], "stop")
        self.assertIn("named nothing a doc review can fix", got["reason"])

    def test_the_commits_under_a_stale_line_are_not_mistaken_for_documents(self):
        got = loop.guidance_guard(1, STALE_GUIDANCE)
        self.assertEqual(got["step"], "doc-review")          # not stopped by "379722e9 TP-257: ..."
        self.assertNotIn("379722e9 TP-257", got["reason"])
        self.assertEqual(got["reason"].count("pinned"), 2)   # two documents, not four

    def test_a_stale_verdict_reaches_loop_sh_as_the_three_words_it_reads(self):
        line = shell_line("guidance", loop.guidance_guard(1, STALE_GUIDANCE))
        step, agent, reason = line.split(" ", 2)
        self.assertEqual((step, agent), ("doc-review", "backend-builder"))
        self.assertTrue(reason.startswith("guidance went stale"))  # the reason keeps its spaces, last


class Prompts(unittest.TestCase):
    """What loop.sh tells an agent to do, from its own prompt_for. Joel steers a slot by commenting
    on its eval ticket, so the Strategist is sent to read that before it plans (TP-185)."""

    def prompt(self, *args):
        return subprocess.run(["bash", "-c", shell_functions("prompt_for") + '\nprompt_for "$@"', "-", *args],
                              capture_output=True, text=True, check=True).stdout

    def test_the_strategist_plans_to_the_steering_on_the_eval_ticket(self):
        for batch, whose in (("b1", "its eval ticket"), ("-", "eval ticket of its last batch")):
            got = self.prompt("strategist", batch, "-", "why")
            self.assertIn("steering comment", got)
            self.assertIn(whose, got)

    def test_the_evaluator_is_given_the_size_and_told_to_judge_the_growth_itself(self):
        got = self.prompt("evaluator", "b1", "TP-3", "why")
        self.assertIn("scripts/batch-size.sh b1", got)     # the measurement it cites
        self.assertIn("record_decision(kind: accepted) on TP-3", got)
        self.assertIn("leave the eval open naming what should be cut", got)
        self.assertIn("Growth the brief did not ask for is a finding", got)

    def test_every_step_is_told_to_write_its_scratch_files_where_git_ignores_them(self):
        """The note has to name the directory, not just the trick: told only to pass a path, batch 16's
        steps wrote six commit messages into the repo root (lesson TP-334). Both halves are asserted
        together, because a note naming a path .gitignore misses is that same bug again."""
        for step in ("builder", "evaluator", "narrator", "doc-review"):
            got = self.prompt(step, "b1", "TP-3", "why")
            self.assertIn("under .scratch/", got, step)
            self.assertIn("git commit -F .scratch/<file>", got, step)
        ignored = subprocess.run(["git", "-C", os.path.dirname(SCRIPTS), "check-ignore", ".scratch/msg.txt"],
                                 capture_output=True, text=True)
        self.assertEqual(ignored.returncode, 0, ".scratch/ is named in every prompt but not in .gitignore")

    def test_a_stale_document_is_re_read_against_its_own_diff_and_only_it_is_re_pinned(self):
        reason = "guidance went stale before the eval, so it is re-read first: the-system (scripts/loop.py, pinned 24101fc9)"
        got = self.prompt("doc-review", "b1", "TP-9", reason)
        self.assertIn("the-system (scripts/loop.py, pinned 24101fc9)", got)  # the pin and paths travel through
        self.assertIn("git diff <its pin>..HEAD -- <its paths>", got)        # read the change, not the corpus
        self.assertIn("--accept-only", got)                                  # re-pin only the documents read
        self.assertIn("Do not run 'just guidance --replace'", got)           # which re-pins all ten (TP-283)
        self.assertIn("Do not touch code", got)


SCRIPTS = os.path.dirname(os.path.abspath(__file__))

# What a checkout's newest migration is called in these throwaway repositories; /api/version reports
# the real one by name, so the stub below answers in the same shape (api/migrations/migrations.go).
MIGRATION = "0001_first.sql"
# A commit no checkout here will ever be at, which is what a process left behind by a migration is
# serving as far as the loop can tell.
STALE_COMMIT = "0000000000000000000000000000000000000000"


def shell_functions(*names):
    """The named functions lifted out of loop.sh, to be run for real. Sourcing the whole script would
    start a run, so a case defines only the few variables the function it exercises reads — which is
    also what keeps these assertions on behaviour rather than on how loop.sh is laid out."""
    with open(os.path.join(SCRIPTS, "loop.sh")) as f:
        body = f.read()
    return "\n".join(re.search(r"^" + name + r"\(\).*?^\}$", body, re.M | re.S).group() for name in names)


def commit_all(root):
    """A throwaway checkout with one commit, so `git rev-parse HEAD` answers. Signing is off: Joel's
    global config signs through 1Password, which would wait here for an approval no test can give."""
    subprocess.run(["git", "-C", root, "init", "-q"], check=True, capture_output=True)
    subprocess.run(["git", "-C", root, "add", "-A"], check=True, capture_output=True)
    subprocess.run(["git", "-C", root, "-c", "user.email=t@example.com", "-c", "user.name=t",
                    "-c", "commit.gpgsign=false", "commit", "-qm", "first"], check=True, capture_output=True)
    return subprocess.run(["git", "-C", root, "rev-parse", "HEAD"], check=True,
                          capture_output=True, text=True).stdout.strip()


def write_served(root, commit, name="served.json"):
    """What the stub /api/version answers next: the two halves of service.CurrentVersion()."""
    with open(os.path.join(root, name), "w") as f:
        json.dump({"commit": commit, "migration": MIGRATION}, f)


def stub_version_api(test, root):
    """A stub /api/version answering whatever <root>/served.json holds, and 404 when it holds nothing
    — which is what a process older than that endpoint itself does. Returns its base URL."""

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            try:
                with open(os.path.join(root, "served.json"), "rb") as f:
                    body = f.read()
            except OSError:
                body = b""
            if self.path != "/api/version" or not body:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass  # what the loop printed is the assertion; an access log beside it is noise

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    test.addCleanup(server.server_close)
    test.addCleanup(server.shutdown)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{server.server_port}"

# Stands in for loop.py so a dry run needs no API and starts no agent. It answers both forms: the
# JSON loop.sh's two inline readers parse today, and — asked with --shell — the same fields
# space-separated with null as "-", which is where TP-228 moves those readers. Every assertion below
# is on loop.sh's own stdout, so this guard holds across that refactor without being edited.
STUB_LOOP_PY = '''#!/usr/bin/env python3
"""A fixed loop.py: prints the `where` and `next` answers in fixtures.json beside it."""
import json
import os
import sys

FIELDS = {"where": ("project", "roadmap", "repo"),
          "next": ("step", "batch", "ticket", "agent", "until", "reason")}
with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures.json")) as f:
    fixtures = json.load(f)
if sys.argv[1] == "guidance":
    # The guidance verb reads the check's report on stdin and prints a step line only when it asks
    # for one, so an empty fixture is the green case: nothing printed, nothing rewritten.
    sys.stdin.read()
    if fixtures.get("guidance"):
        print(fixtures["guidance"])
    sys.exit(0)
answer = fixtures[sys.argv[1]]
if "--shell" in sys.argv:
    print(" ".join("-" if answer[f] is None else str(answer[f]) for f in FIELDS[sys.argv[1]]))
else:
    print(json.dumps(answer))
'''

WHERE = {"project": "P", "roadmap": "R", "repo": None}  # repo is filled in with the temp checkout
NEXT = {"step": "builder", "batch": "b1", "ticket": "TP-231", "agent": "backend-builder",
        "until": None, "reason": "TP-231 is next in order"}
Dry = collections.namedtuple("Dry", "out where step prompt repo head")


class LoopShell(unittest.TestCase):
    """What `loop.sh --dry-run` prints, run for real against a stub loop.py (TP-231).

    Nothing in `just test` ran loop.sh at all: `Decide` and `Where` above test loop.py's JSON with a
    faked `get`, `Prompts` sources `prompt_for` alone, and the one end-to-end run (now
    scripts/test_loop_run.py) covers the loop, not what it prints. So the shell that turns that JSON into words — a reason with spaces, a null
    batch and ticket, the three where-fields — had nothing pinning it. These cases assert on stdout
    and never on how loop.sh gets there, which is what makes them TP-228's tripwire rather than a
    copy of it.
    """

    def dry_run(self, serving=None, guidance=("", 0), **fields):
        """`bash scripts/loop.sh --dry-run` in a throwaway checkout whose loop.py is the stub.

        `serving` is the commit a stub /api/version answers with ("HEAD" for this checkout's own),
        for the steps that ask what the API is serving before they write their prompt (TP-257).
        `guidance` is what a stub guidance-check.sh prints and exits with before an eval (TP-258);
        the default is the green one, which is the case every other step here wants.
        """
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root, True)
        os.makedirs(os.path.join(root, "scripts"))
        os.makedirs(os.path.join(root, "tmp"))
        os.makedirs(os.path.join(root, "api", "migrations"))
        shutil.copy(os.path.join(SCRIPTS, "loop.sh"), os.path.join(root, "scripts", "loop.sh"))
        with open(os.path.join(root, "scripts", "loop.py"), "w") as f:
            f.write(STUB_LOOP_PY)
        with open(os.path.join(root, "scripts", "fixtures.json"), "w") as f:
            json.dump({"where": dict(WHERE, repo=root), "next": dict(NEXT, **fields),
                       "guidance": guidance[0]}, f)
        # A stub guidance-check for the gate an evaluator step runs before it writes its prompt
        # (TP-258). What its report means is `StaleGuidance`'s to fix, and the gate that reads one for
        # real is `StaleGuidanceGate`'s; what these cases are for is the call site — whether loop.sh
        # asks at all, and whether the answer reaches the step it then takes.
        with open(os.path.join(root, "scripts", "guidance-check.sh"), "w") as f:
            f.write("#!/usr/bin/env bash\nexit " + str(guidance[1]) + "\n")
        os.chmod(os.path.join(root, "scripts", "guidance-check.sh"), 0o755)
        # A migration and a commit, because an evaluator step asks this checkout what an API built
        # from it would be serving, and compares that with what the running one says (TP-257).
        with open(os.path.join(root, "api", "migrations", MIGRATION), "w") as f:
            f.write("-- the newest migration this checkout holds\n")
        head = commit_all(root)
        # loop.sh cd's to its own parent, so the copy reads the stub beside it and this checkout is
        # never touched. TMPDIR keeps its mktemp run directory inside the temp tree; LOOP_*, REPO and
        # API are dropped so a run of these tests from inside a real loop still starts from nothing.
        env = {k: v for k, v in os.environ.items()
               if not k.startswith("LOOP_") and k not in ("REPO", "API", "CLAUDE_BIN")}
        env["TMPDIR"] = os.path.join(root, "tmp")
        if serving is not None:
            write_served(root, head if serving == "HEAD" else serving)
            env["API"] = stub_version_api(self, root)
        run = subprocess.run(["bash", "scripts/loop.sh", "--dry-run"], cwd=root, env=env,
                             capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)  # a dry run reports and exits clean
        lines = run.stdout.splitlines()
        return Dry(run.stdout, lines[0], lines[1], "\n".join(lines[2:]), root, head)

    def test_a_reason_with_spaces_arrives_whole_in_the_step_line_and_the_prompt(self):
        # `read` gives the last field the rest of the line, so the reason keeps its spaces; a reader
        # that split it would leave the step line ending at "plan".
        reason = "the Approver sent batch 6's plan back (see TP-194): replan it"
        d = self.dry_run(step="strategist", batch="b1", ticket=None, agent="strategist", reason=reason)
        self.assertRegex(d.step, r"^\[\d\d:\d\d:\d\d\] strategist +— " + re.escape(reason) + "$")
        self.assertIn(reason, d.prompt)
        self.assertIn("Plan batch b1 of the roadmap", d.prompt)

    def test_a_null_batch_and_ticket_leave_the_line_bare_and_take_the_dash_branch(self):
        d = self.dry_run(step="strategist", batch=None, ticket=None, agent="strategist",
                         reason="the roadmap is complete: propose the next roadmap (create_roadmap)")
        self.assertRegex(d.step, r"^\[\d\d:\d\d:\d\d\] strategist +— the roadmap is complete")
        self.assertNotIn("-", d.step.split("—")[0].split("]")[1])  # no "-" standing in for the ticket
        self.assertIn("The roadmap this run is working is complete", d.prompt)
        self.assertNotIn("Plan batch", d.prompt)

    def test_the_where_fields_name_the_project_the_roadmap_and_the_checkout(self):
        d = self.dry_run()
        self.assertEqual(d.where, f"loop: project P, roadmap R, in {d.repo}")
        self.assertIn('project "P"', d.prompt)
        self.assertIn(f"checked out at {d.repo}", d.prompt)

    def test_the_step_line_and_the_prompt_name_the_ticket_and_its_batch(self):
        # The fields in order: a reader that swapped batch and ticket would send the builder after
        # "ticket b1 in batch TP-231" and nothing else would notice.
        d = self.dry_run()
        self.assertEqual(re.sub(r"^\[\d\d:\d\d:\d\d\] ", "", d.step), "builder TP-231 — TP-231 is next in order")
        self.assertIn("Work ticket TP-231 in batch b1 to done", d.prompt)

    def test_a_wait_step_says_how_long_and_starts_no_agent(self):
        # A dry run must report the wait and exit rather than sleep through it: `--dry-run` is what
        # Joel types to see where the plan stands, and the five-hour window is an hour of nothing.
        reason = "five-hour usage 85% is at or above LOOP_USAGE_LIMIT 60%; it resets Fri 2 Oct 09:00, so this run waits for it"
        d = self.dry_run(step="wait", batch=None, ticket=None, agent="wait", until=str(NOW), reason=reason)
        self.assertRegex(d.step, r"^\[\d\d:\d\d:\d\d\] wait +— " + re.escape(reason) + "$")
        self.assertEqual(d.prompt, "")
        self.assertNotIn("You are working on project", d.out)

    def test_a_stop_step_says_why_and_sends_no_prompt(self):
        d = self.dry_run(step="stop", batch=None, ticket=None, agent="stop",
                         reason="waiting on Joel: TP-224 (tagged needs-joel)")
        self.assertEqual(d.prompt, "loop: stopped: waiting on Joel: TP-224 (tagged needs-joel)")
        self.assertNotIn("You are working on project", d.out)

    def test_an_eval_step_names_the_commit_the_api_is_serving_in_the_prompt(self):
        # The evaluator records its verdicts through the API, so the eval's resolution has to be able
        # to say which version it measured (TP-257, goal 9) — and `--dry-run` is where Joel sees that
        # the loop looked. A stale API is reported and restarted; this one is already current.
        d = self.dry_run(serving="HEAD", step="evaluator", batch="b1", ticket="TP-260",
                         agent="evaluator", reason="TP-260 is the eval ticket and next in order")
        self.assertIn(f"serving commit {d.head}", d.prompt)
        self.assertIn("name it in the eval's resolution", d.prompt)
        self.assertNotIn("restarting the API", d.out)  # nothing to restart, and a dry run restarts nothing

    def test_an_eval_step_whose_guidance_is_stale_shows_the_doc_review_it_would_take_first(self):
        # --dry-run is what Joel types to see where a run stands, so it has to name the step a real
        # run would take. Naming the evaluator here while the run would review documents first is the
        # one thing it must never do, which is why the gate is read on a dry run as well (TP-258).
        verdict = "doc-review backend-builder guidance went stale before the eval, so it is re-read first: the-system (scripts/loop.py, pinned 24101fc9)"
        d = self.dry_run(serving="HEAD", guidance=(verdict, 1), step="evaluator", batch="b1",
                         ticket="TP-260", agent="evaluator", reason="TP-260 is the eval ticket and next in order")
        self.assertRegex(d.out, r"\[\d\d:\d\d:\d\d\] doc-review TP-260 — guidance went stale")
        self.assertIn("Guidance has gone stale", d.out)      # the doc review's prompt, not the eval's
        self.assertIn("just guidance --replace", d.out)
        self.assertNotIn("Run eval ticket", d.out)           # and the eval itself waits its turn

class StaleApi(unittest.TestCase):
    """The API must be serving this checkout before an eval records anything through it (TP-257).

    A process left behind by a migration created orphan batch 2f37317c and left batch 3's
    gate_passed null for good (TP-175, TP-211), and restarting it by hand was the main thing
    stopping the loop (Joel on TP-197). These run loop.sh's own check for real against a stub
    /api/version and a stub dev.sh, so the curl, the JSON it parses, the comparison, the launch path
    and the bounded wait are all the shipped ones; only the API and the `go run` are stood in for.
    """

    def checkout(self, serving, restart_serves=None):
        """A throwaway checkout with one migration, a stub dev.sh and a stub API. `serving` is the
        commit the API reports ("HEAD" for this checkout's own, None for no /api/version at all);
        `restart_serves` is what the stub dev.sh makes it report, so a case chooses whether the
        restart works. Returns the checkout, its HEAD and the stub API's URL."""
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root, True)
        for part in (("api", "migrations"), ("scripts",), ("run",)):
            os.makedirs(os.path.join(root, *part))
        with open(os.path.join(root, "api", "migrations", MIGRATION), "w") as f:
            f.write("-- the newest migration this checkout holds\n")
        head = commit_all(root)
        resolve = lambda c: head if c == "HEAD" else c
        if serving is not None:
            write_served(root, resolve(serving))
        # The stub dev.sh records that it was asked, in the mode it was asked in, and — where a case
        # wants the restart to work — makes the API answer what a real relaunch would have made it
        # answer. The rename is so a poll landing mid-write reads the old file rather than half of one.
        dev = '#!/usr/bin/env bash\necho "dev.sh $*" >> restarts\n'
        if restart_serves is not None:
            write_served(root, resolve(restart_serves), name="restart.json")
            dev += 'cp restart.json served.json.new && mv served.json.new served.json\n'
        with open(os.path.join(root, "scripts", "dev.sh"), "w") as f:
            f.write(dev)
        os.chmod(os.path.join(root, "scripts", "dev.sh"), 0o755)
        return root, head, stub_version_api(self, root)

    def check(self, root, api, dry="", wait=3):
        """loop.sh's api_serves_head, run in that checkout with only the variables it reads set."""
        harness = (f"set -euo pipefail\nAPI={api}\nRUN={os.path.join(root, 'run')}\n"
                   f"dry={dry!r}\nLOOP_API_WAIT={wait}\n"
                   + shell_functions("api_serving", "api_serves_head")
                   + '\napi_serves_head "before TP-9\'s eval"\necho "SERVED=$SERVED"\n')
        return subprocess.run(["bash", "-c", harness], cwd=root, capture_output=True, text=True)

    def restarts(self, root):
        """How the stub dev.sh was called, which is the only way the API can have been relaunched."""
        try:
            with open(os.path.join(root, "restarts")) as f:
                return f.read().split()
        except OSError:
            return []

    def test_an_api_already_on_head_is_not_stale_and_nothing_is_restarted(self):
        root, head, api = self.checkout(serving="HEAD")
        got = self.check(root, api)
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertEqual(self.restarts(root), [])  # the common case costs one GET and no downtime
        self.assertIn(f"SERVED={head}", got.stdout)
        self.assertNotIn("restarting", got.stdout)

    def test_a_stale_api_is_restarted_and_the_step_then_proceeds(self):
        root, head, api = self.checkout(serving=STALE_COMMIT, restart_serves="HEAD")
        got = self.check(root, api)
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn(STALE_COMMIT, got.stdout)                    # what it found
        self.assertIn(f"the API now serves {head}", got.stdout)    # and what it waited for
        self.assertIn(f"SERVED={head}", got.stdout)                # so the prompt names the new one
        # Through dev.sh's api mode and nowhere else, so the env and the `go run` line stay in one file.
        self.assertEqual(self.restarts(root), ["dev.sh", "api"])

    def test_an_api_that_stays_stale_stops_the_run_naming_both_commits(self):
        root, head, api = self.checkout(serving=STALE_COMMIT)  # no restart_serves: nothing changes
        got = self.check(root, api)
        self.assertEqual(got.returncode, 1)
        self.assertIn("loop: stopped:", got.stderr)
        self.assertIn(STALE_COMMIT, got.stderr)  # what it is still serving
        self.assertIn(head, got.stderr)          # and what it should be, so Joel needs no second look
        self.assertEqual(self.restarts(root), ["dev.sh", "api"])  # it did try, once

    def test_an_api_too_old_to_say_what_it_serves_counts_as_stale(self):
        # The live API 404'd /api/version the day this was written, TP-256 having only just added it.
        # A process that cannot answer is not "unknown whether it is current", it is certainly not.
        root, head, api = self.checkout(serving=None, restart_serves="HEAD")
        got = self.check(root, api)
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn("the API serves unknown unknown", got.stdout)
        self.assertIn(f"SERVED={head}", got.stdout)

    def test_a_dry_run_reports_a_stale_api_and_restarts_nothing(self):
        # --dry-run is what Joel types to see where a run stands; it must not replace his dev API.
        root, head, api = self.checkout(serving=STALE_COMMIT, restart_serves="HEAD")
        got = self.check(root, api, dry="1")
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn(f"this checkout is at {head}", got.stdout)
        self.assertIn(f"SERVED={STALE_COMMIT}", got.stdout)  # the prompt names what is really served
        self.assertEqual(self.restarts(root), [])


class StaleGuidanceGate(unittest.TestCase):
    """loop.sh's own guidance_gate, run for real against a stub guidance-check (TP-258).

    `StaleGuidance` above fixes what a report means; this fixes the wiring that has to carry it — the
    capture of a check that exits non-zero without `set -e` killing the run, the pipe into loop.py, and
    the rewrite of the step variables the main loop goes on to use. A gate that read the report but
    left `step` saying `evaluator` would send the eval in against the very documents it just flagged.
    """

    def gate(self, report, code, ticket="TP-9"):
        """guidance_gate in a throwaway checkout whose guidance-check.sh says what a case wants, with
        the real loop.py beside it: only the check itself is stood in for."""
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root, True)
        os.makedirs(os.path.join(root, "scripts"))
        shutil.copy(os.path.join(SCRIPTS, "loop.py"), os.path.join(root, "scripts", "loop.py"))
        check = os.path.join(root, "scripts", "guidance-check.sh")
        with open(check, "w") as f:
            f.write("#!/usr/bin/env bash\necho ran >> asked\ncat report\nexit " + str(code) + "\n")
        os.chmod(check, 0o755)
        with open(os.path.join(root, "report"), "w") as f:
            f.write(report)
        harness = (f"set -euo pipefail\nstep=evaluator\nagent=evaluator\nticket={ticket}\n"
                   "reason='TP-9 is the eval ticket and next in order'\n"
                   + shell_functions("guidance_gate")
                   + '\nguidance_gate\necho "STEP=$step AGENT=$agent"\necho "REASON=$reason"\n')
        run = subprocess.run(["bash", "-c", harness], cwd=root, capture_output=True, text=True)
        return run, os.path.exists(os.path.join(root, "asked"))

    def test_guidance_that_is_not_stale_leaves_the_evaluator_step_as_it_was(self):
        run, asked = self.gate(GREEN_GUIDANCE, 0)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertTrue(asked)  # it did look; a gate that skipped the check would pass this way too
        self.assertIn("STEP=evaluator AGENT=evaluator", run.stdout)
        self.assertIn("REASON=TP-9 is the eval ticket and next in order", run.stdout)

    def test_a_stale_only_report_rewrites_the_step_into_a_doc_review_for_the_same_ticket(self):
        run, _ = self.gate(STALE_GUIDANCE, 1)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn("STEP=doc-review AGENT=backend-builder", run.stdout)
        self.assertIn("REASON=guidance went stale before the eval", run.stdout)
        # Named on stdout in the step line's own shape, so a run's log shows why the eval waited.
        self.assertRegex(run.stdout, r"\[\d\d:\d\d:\d\d\] doc-review TP-9 — guidance went stale")

    def test_a_report_that_is_not_only_stale_stops_the_run_and_prints_what_it_found(self):
        run, _ = self.gate(DRIFTED_GUIDANCE, 1)
        self.assertEqual(run.returncode, 1)
        self.assertIn("loop: stopped:", run.stderr)
        self.assertIn("DRIFT   the-agents", run.stderr)  # the whole report, so nobody re-runs it to look
        self.assertNotIn("STEP=", run.stdout)            # and the main loop never gets its step back


class WaitBlock(unittest.TestCase):
    """How long loop.sh waits, run for real with `sleep` and `date` stood in for (TP-230).

    The dry-run case above proves a `wait` step starts no agent; this proves the arithmetic behind it,
    which nothing else can: a real wait sleeps for the rest of a five-hour window. A sign error here
    would sleep for a negative number (bash: an error, so the run dies) or for the whole epoch.
    """

    def nap(self, until_at, now=1790000000):
        with open(os.path.join(SCRIPTS, "loop.sh")) as f:
            block = re.search(r'^  if \[ "\$step" = wait \]; then.*?^  fi$', f.read(), re.M | re.S).group()
        harness = ('sleep() { echo "slept $1"; }\n'
                   'date() { echo ' + str(now) + '; }\n'
                   'step=wait reason=why until_at=' + str(until_at) + '\n'
                   'for _ in 1; do\n' + block + '\ndone\n')
        got = subprocess.run(["bash", "-c", harness], capture_output=True, text=True, check=True)
        return got.stdout

    def test_it_sleeps_until_the_window_resets_plus_a_minute(self):
        # The minute is slack: at the reset moment itself the recorded figures are still the spent ones.
        self.assertIn("slept 660", self.nap(1790000000 + 600))

    def test_a_reset_already_past_or_unrecorded_naps_a_minute_rather_than_a_negative_number(self):
        # Neither should reach here — loop.py counts a passed window as empty — and if one does, the
        # guard on the same step coming up twice ends the run rather than spinning.
        self.assertIn("slept 60", self.nap(1790000000 - 3600))
        self.assertIn("slept 60", self.nap("-"))

    def test_it_says_how_long_it_is_waiting_and_why(self):
        self.assertIn("loop: waiting 660s: why", self.nap(1790000000 + 600))


# /api/projects comes back oldest first, which is the project the API resolves an unqualified query to.
PROJECTS = [{"key": "TP", "created_at": "2026-09-16T13:36:25Z", "repo_path": None},
            {"key": "DEMO", "created_at": "2026-09-25T09:00:00Z", "repo_path": "/tmp/demo"}]
ROADMAPS = {"TP": [{"id": "rm-tp-old", "created_at": "2026-09-24T16:45:18Z", "archived_at": None},
                   {"id": "rm-tp", "created_at": "2026-09-24T20:24:19Z", "archived_at": None},
                   {"id": "rm-tp-gone", "created_at": "2026-09-25T10:00:00Z", "archived_at": "2026-09-25T10:01:00Z"}],
            "DEMO": [{"id": "rm-demo", "created_at": "2026-09-25T09:05:00Z", "archived_at": None}]}


class Fetching(unittest.TestCase):
    """Stands in for the API and records what was asked of it."""

    def setUp(self):
        self.asked = []
        self.addCleanup(setattr, loop, "get", loop.get)
        loop.get = self.fake_get
        # The one input that is not the faked API: a real LOOP_USAGE_FILE would let the machine these
        # run on decide the step, and a spent window would stop every case below.
        drop_env(self, "LOOP_USAGE_FILE", "LOOP_USAGE_LIMIT")

    def fake_get(self, path, **params):
        self.asked.append((path, params))
        if path == "/api/projects":
            return PROJECTS
        if path == "/api/roadmaps":
            return ROADMAPS[params["project"]]
        raise AssertionError(f"unexpected GET {path}")

    def scoped(self, path):
        """The project named on the last query to that path."""
        return next((p.get("project") for asked, p in reversed(self.asked) if asked == path), "never asked")


class Where(Fetching):
    """Which project a run works, which roadmap, and the checkout its agents run in (TP-146)."""

    def test_naming_no_project_is_the_default_one_on_this_checkout(self):
        self.assertEqual(where(), {"project": "TP", "roadmap": "rm-tp", "repo": loop.CHECKOUT})
        self.assertEqual(self.scoped("/api/roadmaps"), "TP")

    def test_a_named_project_brings_its_own_roadmap_and_its_own_checkout(self):
        self.assertEqual(where("DEMO"), {"project": "DEMO", "roadmap": "rm-demo", "repo": "/tmp/demo"})
        self.assertEqual(self.scoped("/api/roadmaps"), "DEMO")

    def test_the_roadmap_is_the_newest_the_project_has_not_archived(self):
        self.assertEqual(where()["roadmap"], "rm-tp")  # not rm-tp-old, and not the archived rm-tp-gone

    def test_an_explicit_roadmap_still_wins(self):
        self.assertEqual(where("DEMO", roadmap_id="rm-other")["roadmap"], "rm-other")

    def test_REPO_wins_over_repo_path_so_a_demo_can_use_a_scratch_clone(self):
        self.assertEqual(where("DEMO", repo_env="/tmp/scratch")["repo"], "/tmp/scratch")

    def test_a_named_project_with_no_repo_path_refuses_to_start_and_says_so(self):
        with self.assertRaises(Problem) as refusal:
            where("TP")
        self.assertIn("repo_path", str(refusal.exception))

    def test_a_project_the_API_does_not_have_refuses_to_start_and_says_so(self):
        with self.assertRaises(Problem) as refusal:
            where("NOPE")
        self.assertIn("NOPE", str(refusal.exception))
        self.assertIn("DEMO", str(refusal.exception))

    def test_a_project_with_no_roadmap_refuses_to_start_and_says_so(self):
        ROADMAPS["EMPTY"] = []
        self.addCleanup(ROADMAPS.pop, "EMPTY")
        PROJECTS.append({"key": "EMPTY", "created_at": "2026-09-26T00:00:00Z", "repo_path": "/tmp/empty"})
        self.addCleanup(PROJECTS.pop)
        with self.assertRaises(Problem) as refusal:
            where("EMPTY")
        self.assertIn("no roadmap", str(refusal.exception))


class Shell(Fetching):
    """`--shell` prints the fields loop.sh `read`s, which is why that script parses no JSON (TP-228).

    `LoopShell` above pins the shell side against a stub; these pin the real loop.py's half of the
    same contract, so the two cannot drift apart without one of them going red.
    """

    def setUp(self):
        super().setUp()
        # main() falls back to REPO and LOOP_* from the environment, and these may run inside a loop.
        drop_env(self, "REPO", "LOOP_PROJECT", "LOOP_ROADMAP")

    def run_main(self, *argv, tracker=None):
        """loop.py's own main against the faked API, with `next`'s snapshot handed in rather than read."""
        if tracker is not None:
            self.addCleanup(setattr, loop, "snapshot", loop.snapshot)
            loop.snapshot = lambda w: tracker
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            loop.main(["loop.py", *argv, "--project", "DEMO"])
        return out.getvalue().rstrip("\n")

    def test_where_prints_the_project_the_roadmap_and_the_checkout(self):
        self.assertEqual(self.run_main("where", "--shell"), "DEMO rm-demo /tmp/demo")

    def test_next_puts_the_reason_last_so_it_keeps_its_spaces(self):
        line = self.run_main("next", "--shell", tracker=snap(batch(tickets=WORK, nxt="TP-1")))
        self.assertEqual(line, "builder b1 TP-1 backend-builder - TP-1 is next in order (usage: unknown)")

    def test_next_puts_a_waits_reset_time_in_the_field_loop_sh_sleeps_to(self):
        # The field is between the agent and the reason, so the reason keeps the rest of the line.
        # main() has no `now` to hand in, so the window resets relative to the real clock.
        resets = int(time.time()) + 600
        line = self.run_main("next", "--shell", tracker=snap(batch(tickets=WORK, nxt="TP-1"),
                                                             usage=windows(five=0.9, five_resets=resets)))
        self.assertEqual(line.split()[:5], ["wait", "-", "-", "wait", str(resets)])

    def test_next_writes_a_null_batch_and_ticket_as_a_dash(self):
        complete = batch(tickets=done(WORK), reviews=("before", "after"), finished=True, checkpoint="continue", merged=True)
        line = self.run_main("next", "--shell", tracker=snap(complete))
        self.assertEqual(line.split()[:4], ["strategist", "-", "-", "strategist"])

    def test_without_the_flag_both_forms_still_print_the_json_its_readers_parse(self):
        # fake_claude.py and loop.sh parse `loop.py next`, so the default output is not the refactor's to change.
        self.assertEqual(json.loads(self.run_main("where")), {"project": "DEMO", "roadmap": "rm-demo", "repo": "/tmp/demo"})
        self.assertEqual(json.loads(self.run_main("next", tracker=snap(batch(tickets=WORK, nxt="TP-1"))))["ticket"], "TP-1")

    def test_the_line_splits_under_bash_read_into_the_names_loop_sh_uses(self):
        reason = "the Approver sent batch 6's plan back (see TP-194): replan it"
        line = shell_line("next", {"step": "strategist", "batch": "b1", "ticket": None,
                                   "agent": "strategist", "until": None, "reason": reason})
        read = ('read -r step batch ticket agent until_at reason <<< "$1"; '
                'echo "$step|$batch|$ticket|$agent|$until_at|$reason"')
        got = subprocess.run(["bash", "-c", read, "-", line], capture_output=True, text=True, check=True)
        self.assertEqual(got.stdout.strip(), f"strategist|b1|-|strategist|-|{reason}")


class Snapshot(Fetching):
    """Reading the tracker for one project: every query that resolves one names it (TP-146)."""

    def setUp(self):
        super().setUp()
        self.needs_joel = []
        self.lessons = []

    def fake_get(self, path, **params):
        self.asked.append((path, params))
        if path == "/api/roadmaps/rm-demo":
            return {"batches": [{"batch": {"id": "b1", "title": "Connect DEMO", "sequence": 1,
                                           "finished_at": None, "reviews_required": True}}]}
        if path == "/api/tripwires":
            return []
        if path == "/api/batches/b1":
            return {"tickets": [], "progress": {}}
        if path == "/api/batches/b1/reviews":
            return []
        if path == "/api/tickets":
            return {"items": self.lessons if params.get("tag") == "lesson" else self.needs_joel}
        raise AssertionError(f"unexpected GET {path}")

    def read(self):
        return loop.snapshot({"project": "DEMO", "roadmap": "rm-demo", "repo": "/tmp/demo"})

    def test_a_named_projects_placeholder_batch_goes_to_the_strategist_naming_that_batch(self):
        step = decide(self.read())
        self.assertEqual((step["step"], step["batch"]), ("strategist", "b1"))
        self.assertIn("Connect DEMO", step["reason"])

    def test_every_query_names_this_project(self):
        # Even the ones addressed by id: the API reads a roadmap or a batch within a project, and
        # answers 404 for an id belonging to another one.
        self.read()
        for path in ("/api/roadmaps/rm-demo", "/api/tripwires", "/api/batches/b1", "/api/batches/b1/reviews",
                     "/api/tickets"):
            self.assertEqual(self.scoped(path), "DEMO", path)
        self.assertEqual({p["tag"] for asked, p in self.asked if asked == "/api/tickets"}, {"needs-joel", "lesson"})

    def test_the_snapshot_carries_the_plans_usage_beside_the_tracker(self):
        # The guard is only as good as this wiring: a snapshot without "usage" reads as unknown, which
        # never stops a run, so forgetting it would silently disarm the whole feature (TP-230).
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        os.environ["LOOP_USAGE_FILE"] = os.path.join(tmp, "usage-windows.json")
        written = stream_usage.write_windows(FIXTURE_LOG, os.environ["LOOP_USAGE_FILE"])
        self.assertEqual(self.read()["usage"], written)

    def test_which_lesson_statuses_are_carried_for_promotion(self):
        # A done or closed lesson has already been acted on; carrying it would name it forever. An
        # AI-filed lesson, though, sits in triage until a human accepts it, and a pattern seen three
        # times there is still three times (TP-285).
        for status, carried in (("open", True), ("triage", True), ("done", False), ("closed", False)):
            with self.subTest(status):
                self.lessons = [{"key": "DEMO-7", "status": status, "seen_count": 3}]
                self.assertEqual(self.read()["lessons"],
                                 [{"key": "DEMO-7", "seen_count": 3}] if carried else [])
                reason = decide(self.read())["reason"]
                self.assertEqual("DEMO-7" in reason, carried, reason)

    def test_only_this_projects_needs_joel_tickets_stop_the_run(self):
        self.needs_joel = [{"key": "DEMO-5", "status": "open", "batch_id": "b1", "title": "Confirm the guard"},
                           {"key": "DEMO-6", "status": "done"}]
        self.assertEqual(self.read()["waiting_on_joel"],
                         [{"key": "DEMO-5", "batch_id": "b1", "title": "Confirm the guard", "description": ""}])


class LessonPromotion(unittest.TestCase):
    """Promotion at 3 (TP-187): a counted lesson only changes the next batch if the step that plans it
    is told, so every strategist step's reason names the open lessons at loop.PROMOTE_AT or more."""

    def strategist_reason(self, lessons, roadmap=None):
        got = decide(snap(*(roadmap or [batch(tickets=[ticket("TP-9", typ="eval", status="triage")])]),
                          lessons=lessons))
        self.assertEqual(got["step"], "strategist", got["reason"])
        return got["reason"]

    def test_the_threshold_decides_which_keys_the_reason_names(self):
        for case, lessons, named, silent in (
            ("seen three times", [("TP-280", 3)], ["TP-280"], []),
            ("seen twice, a pattern that may not recur", [("TP-281", 2)], [], ["TP-281"]),
            ("seen more than three times", [("TP-282", 7)], ["TP-282"], []),
            ("one over and one under", [("TP-280", 3), ("TP-281", 2)], ["TP-280"], ["TP-281"]),
            ("no lessons at all", [], [], []),
        ):
            with self.subTest(case):
                reason = self.strategist_reason(lessons)
                self.assertEqual("Promote these lessons" in reason, bool(named), reason)
                for key in named:
                    self.assertIn(key, reason)
                for key in silent:
                    self.assertNotIn(key, reason)

    def test_the_other_strategist_step_names_them_too_and_no_other_step_does(self):
        # The roadmap-complete step plans the next roadmap, so it needs the same list.
        complete = [batch(tickets=done(WORK), reviews=("before", "after"), finished=True,
                          checkpoint="continue", merged=True)]
        self.assertIn("TP-280", self.strategist_reason([("TP-280", 3)], roadmap=complete))
        builder = decide(snap(batch(tickets=WORK, nxt="TP-1"), lessons=[("TP-280", 3)]))
        self.assertEqual(builder["step"], "builder")
        self.assertNotIn("TP-280", builder["reason"])


class PromotionIsPlanned(unittest.TestCase):
    """A lesson at PROMOTE_AT was only ever named to the Strategist (TP-286). The Approver looking at the
    new plan is told which due lessons that plan carries no ticket for, so it can send it back."""

    def approver_reason(self, lessons, plan):
        got = decide(snap(batch(tickets=plan + [ticket("TP-9", typ="eval", status="triage")]), lessons=lessons))
        self.assertEqual(got["step"], "approver-accept", got["reason"])
        return got["reason"]

    def test_the_plan_is_read_for_a_ticket_citing_each_due_lesson(self):
        cites_283 = ticket("TP-287", status="triage", title="Promote lesson TP-283")
        cites_281 = ticket("TP-288", status="triage", description="Promotes TP-281: tripwire waits on Joel.")
        for case, lessons, plan, missing in (
            ("this batch as planned: both due lessons cited", [("TP-283", 3), ("TP-281", 3)],
             [cites_283, cites_281], []),
            ("one of the two promoted", [("TP-283", 3), ("TP-281", 3)], [cites_283], ["TP-281"]),
            ("none promoted", [("TP-283", 3)], [ticket("TP-287", status="triage", title="Something else")],
             ["TP-283"]),
            ("nothing due: a lesson under the threshold is not asked for", [("TP-281", 2)],
             [ticket("TP-287", status="triage", title="Something else")], []),
            ("no lessons at all", [], [ticket("TP-287", status="triage")], []),
            ("cited only by the eval ticket, which names the batch and not the work", [("TP-283", 3)],
             [ticket("TP-287", status="triage"), ticket("TP-8", typ="eval", status="triage",
                                                        title="Eval: promote TP-283")], ["TP-283"]),
        ):
            with self.subTest(case):
                reason = self.approver_reason(lessons, plan)
                self.assertEqual("cites no ticket" in reason, bool(missing), reason)
                for key in missing:
                    self.assertIn(key, reason)

    def test_no_other_step_is_told(self):
        # Only the Approver can send a plan back; on any other step the note would be noise.
        for chosen in (decide(snap(batch(tickets=WORK, nxt="TP-1"), lessons=[("TP-283", 3)])),
                       decide(snap(batch(tickets=[], reviews=()), lessons=[("TP-283", 3)]))):
            self.assertNotIn("cites no ticket", chosen["reason"])


class UnguardedRefactorIsPlacedLast(unittest.TestCase):
    """Approver rule 7.4 wants a refactor whose gate tripwire only Joel can confirm placed last before
    the eval, with an open needs-joel ticket asking him. It was a rule and the lesson recurred three
    times (TP-281), so TP-288 reads the plan for it: the Approver is told what is out of place."""

    TRIPWIRE = "4d84daf1-a599-47ee-bb89-6fc5038d3fe2"
    REF = "api/internal/app/app_test.go:TestPostThenListThenPatch"

    def approver_reason(self, plan, waiting):
        got = decide(snap(batch(tickets=plan + [ticket("TP-9", typ="eval", status="triage")]), waiting=waiting))
        self.assertEqual(got["step"], "approver-accept", got["reason"])
        return got["reason"]

    def test_the_plan_is_read_for_where_the_refactor_sits_and_who_was_asked(self):
        other = ticket("TP-1", status="triage")
        unguarded = ticket("TP-275", typ="refactor", status="triage", guarded=False,
                           awaits=[[self.TRIPWIRE, self.REF]])
        asks_by_id = [("TP-277", None, f"Confirm the tripwire {self.TRIPWIRE[:8]} before TP-275 finishes")]
        asks_by_ref = [("TP-277", None, f"Confirm the guard test {self.REF}")]
        for case, plan, waiting, flagged in (
            ("this batch as planned: last before the eval, TP-277 naming 4d84daf1",
             [other, unguarded], asks_by_id, []),
            ("the needs-joel ticket names the test_ref instead of the id", [other, unguarded], asks_by_ref, []),
            ("a needs-joel ticket in triage still counts as asked: TP-277 has not been accepted",
             [other, unguarded], asks_by_id, []),
            ("not last before the eval, so the rest of the batch waits behind Joel",
             [unguarded, other], asks_by_id, ["is not last"]),
            ("nobody was asked", [other, unguarded], [], ["no open needs-joel"]),
            ("asked about some other tripwire", [other, unguarded],
             [("TP-242", None, "Confirm something else entirely")], ["no open needs-joel"]),
            ("both wrong at once", [unguarded, other], [], ["is not last", "no open needs-joel"]),
            ("a guarded refactor is nobody's problem, wherever it sits",
             [ticket("TP-275", typ="refactor", status="triage", guarded=True), other], [], []),
            ("an unguarded refactor with no tripwire at all goes to approver-guard, not here",
             [ticket("TP-275", typ="refactor", status="triage", guarded=False, awaits=[]), other], [], []),
        ):
            with self.subTest(case):
                reason = self.approver_reason(plan, waiting)
                self.assertEqual("not placed for it" in reason, bool(flagged), reason)
                for said in flagged:
                    self.assertIn(said, reason)
                if flagged:
                    self.assertIn("TP-275", reason)

    def test_no_other_step_is_told(self):
        plan = [ticket("TP-275", typ="refactor", guarded=False, awaits=[[self.TRIPWIRE, self.REF]]),
                ticket("TP-9", typ="eval")]
        # That step is the stop the loop already makes for this refactor; the note belongs to the plan.
        self.assertNotIn("not placed for it", decide(snap(batch(tickets=plan, nxt="TP-275")))["reason"])


class Credential(unittest.TestCase):
    """Every call loop.py makes carries the credential it has (TP-316).

    Once TICKETING_TOKENS is set the API refuses the lot — GETs included — so the whole loop is what
    breaks when the `loop` role has no token (TP-315 step 4). A run that read a project list it was
    never shown is not the failure to want; one that stops naming the 401 is.
    """

    TOKEN = "f00dcafe-the-loop-role"

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.seen = []
        test = self
        # Enough of the tracker for `next` to reach an answer: one project, one roadmap, no batches,
        # which decide() reads as a complete roadmap. What is under test is who may read it at all.
        answers = {"/api/projects": [{"key": "P", "created_at": "2026-09-01T00:00:00Z"}],
                   "/api/roadmaps": [{"id": "rm-1", "created_at": "2026-09-01T00:00:00Z"}],
                   "/api/roadmaps/rm-1": {"batches": []}, "/api/tripwires": [], "/api/tickets": {"items": []}}

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                path = self.path.split("?")[0]
                test.seen.append((path, self.headers.get("Authorization")))
                if self.headers.get("Authorization") != "Bearer " + test.TOKEN:
                    self.send_error(401)
                    return
                body = json.dumps(answers[path]).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.api = f"http://127.0.0.1:{server.server_port}"

    def next_step(self, **env):
        """`loop.py next` as loop.sh runs it: a subprocess, so the environment is the whole input.
        LOOP_* and TICKETING_* are dropped, because these tests may themselves run inside a loop."""
        clean = {k: v for k, v in os.environ.items()
                 if not k.startswith("LOOP_") and not k.startswith("TICKETING_") and k != "REPO"}
        return subprocess.run([sys.executable, os.path.join(SCRIPTS, "loop.py"), "next"], text=True,
                              capture_output=True, env={**clean, "API": self.api,
                                                        "TICKETING_TOKEN_DIR": self.dir, **env})

    def test_loop_py_next_sends_the_loop_token(self):
        refused = self.next_step()
        self.assertNotEqual(0, refused.returncode, refused.stdout)
        self.assertIn("401", refused.stderr)
        self.assertNotIn("Traceback", refused.stderr, "a 401 is said, not dumped")

        sent = self.next_step(TICKETING_TOKEN=self.TOKEN)
        self.assertEqual(0, sent.returncode, sent.stderr)
        self.assertEqual("strategist", json.loads(sent.stdout)["step"])

        # The other half of the contract: loop.sh exports TICKETING_TOKEN, but a loop.py run by hand
        # reads the `loop` role's own file, so both doors into the tracker carry the same credential.
        with open(os.path.join(self.dir, "loop"), "w") as f:
            f.write(self.TOKEN + "\n")
        from_file = self.next_step()
        self.assertEqual(0, from_file.returncode, from_file.stderr)
        self.assertEqual("strategist", json.loads(from_file.stdout)["step"])

        # Not the first call alone: every path the snapshot reads was asked for with the credential.
        asked = {path for path, auth in self.seen if auth == "Bearer " + self.TOKEN}
        self.assertEqual({"/api/projects", "/api/roadmaps", "/api/roadmaps/rm-1", "/api/tripwires",
                          "/api/tickets"}, asked)


class Merged(unittest.TestCase):
    """A batch branch is merged when main holds its changes, even under other hashes (TP-24's re-signing)."""

    def git(self, *args):
        subprocess.run(["git", "-C", self.repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", *args],
                       check=True, capture_output=True)

    def setUp(self):
        self.repo = tempfile.mkdtemp()
        self.git("init", "-q", "-b", "main")
        self.git("commit", "-q", "--allow-empty", "-m", "root")
        self.git("switch", "-q", "-c", "batch/1")
        open(os.path.join(self.repo, "a"), "w").write("a\n")
        self.git("add", "a")
        self.git("commit", "-q", "-m", "work")
        self.git("switch", "-q", "main")

    def test_an_unmerged_branch_is_not_merged(self):
        self.assertFalse(loop.merged("batch/1", self.repo))

    def test_a_branch_whose_changes_are_in_main_under_new_hashes_is_merged(self):
        self.git("cherry-pick", "batch/1")
        self.assertTrue(loop.merged("batch/1", self.repo))


if __name__ == "__main__":
    unittest.main()
