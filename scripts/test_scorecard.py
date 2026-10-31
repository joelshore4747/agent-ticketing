"""Unit tests for scripts/scorecard.py, the per-batch answer to "is the tracker working?" (Joel, 2026-10-18)."""
import datetime
import os
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
import scorecard  # noqa: E402


def at(day, clock):
    h, m, s = (int(p) for p in clock.split(":"))
    return datetime.datetime(2026, 10, day, h, m, s).timestamp()


def no_log(_reason):
    return ""


LOG = """\
[2026-10-16 23:50:00] restart: FS batch 2
[23:55:10] builder FS-20 — FS-20 is next in order
loop: stopped: the same step came up twice with nothing changed (builder FS-20); see /tmp/ticketing-loop.x
[2026-10-17 00:20:00] restart: FS batch 2 after the fix
[00:21:00] builder FS-21 — FS-21 is next in order
[00:30:00] stop  — waiting on Joel: FS-46 (tagged needs-joel)
loop: stopped: waiting on Joel: FS-46 (tagged needs-joel)
""".splitlines()


class Classify(unittest.TestCase):
    def test_who_each_stop_was_waiting_on(self):
        for reason, result, kind in (
                ("waiting on Joel: FS-46 (tagged needs-joel)", "", "you"),
                ("waiting on Joel: FS-23 is guarded by a tripwire only he can confirm", "", "you"),
                ("seven-day usage 60% is at or above LOOP_USAGE_LIMIT 60%", "", "usage"),
                ("the reviewer step failed; its log is /tmp/x/4-review.jsonl", "You've hit your session limit", "usage"),
                ("the same step came up twice with nothing changed (evaluator FS-12)", "", "harness"),
                ("the merge of batch 04ed5c91 was refused", "", "harness")):
            with self.subTest(reason):
                self.assertEqual(kind, scorecard.classify(reason, result))


class Stops(unittest.TestCase):
    def test_a_stop_takes_the_time_before_it_and_resumes_at_the_next_working_line(self):
        harness, joel = scorecard.stops(LOG, no_log)
        self.assertEqual(("harness", at(16, "23:55:10"), at(17, "00:20:00")),
                         (harness["kind"], harness["ts"], harness["resumed"]))
        self.assertEqual(("you", at(17, "00:30:00"), None), (joel["kind"], joel["ts"], joel["resumed"]))

    def test_an_undated_time_after_midnight_moves_to_the_next_day(self):
        lines = ["[2026-10-16 23:59:00] restart", "[00:01:00] builder FS-1 — next",
                 "loop: stopped: the same step came up twice"]
        self.assertEqual(at(17, "00:01:00"), scorecard.stops(lines, no_log)[0]["ts"])

    def test_the_step_log_a_stop_names_decides_a_failed_step(self):
        lines = ["[2026-10-16 15:18:41] review — batch 2", "loop: stopped: the reviewer step failed; its log is /tmp/a/4-review.jsonl"]
        self.assertEqual("usage", scorecard.stops(lines, lambda _r: "You've hit your session limit")[0]["kind"])


class Scorecard(unittest.TestCase):
    batch = {"id": "b2", "title": "Standards", "sequence": 2, "checkpoint_at": None,
             "lines_added": 700, "lines_deleted": 100, "test_lines_added": 400, "test_lines_deleted": 0}

    def test_a_batch_runs_from_its_first_step_and_is_charged_the_stops_inside_it(self):
        ledger = [{"batch": "b2", "ts": at(16, "23:50:00"), "wall_time_seconds": 600, "cost_usd": 1.5, "step": "builder"},
                  {"batch": "b2", "ts": at(17, "00:25:00"), "wall_time_seconds": 300, "cost_usd": 0.5, "step": "evaluator"},
                  {"batch": "other", "ts": at(16, "23:51:00"), "wall_time_seconds": 999, "cost_usd": 9, "step": "builder"}]
        now = at(17, "01:30:00")
        row, = scorecard.scorecard([self.batch], ledger, scorecard.stops(LOG, no_log), now)
        self.assertEqual((now - at(16, "23:50:00"), 900, 1, 1, 2.0), (row["elapsed"], row["building"],
                         row["harness_stops"], row["evals"], row["cost"]))
        self.assertEqual(at(17, "00:20:00") - at(16, "23:55:10"), row["lost"]["harness"])
        self.assertEqual(now - at(17, "00:30:00"), row["lost"]["you"])  # still waiting, so it runs to now
        self.assertEqual((200, 400), (row["code_net"], row["tests_added"]))  # code only: +300 code, -100

    def test_a_batch_no_agent_worked_is_left_out_and_an_unmeasured_one_reads_unknown(self):
        unmeasured = {**self.batch, "id": "b3", "lines_added": None}
        rows = scorecard.scorecard([self.batch, unmeasured], [{"batch": "b3", "ts": 1, "step": "builder"}], [], 10)
        self.assertEqual(1, len(rows))  # b2 has no steps in the ledger, so only b3 is scored
        self.assertIsNone(rows[0]["code_net"])
        self.assertIn("?", scorecard.render(rows))

    def test_the_verdict_names_the_harness_stops_and_the_share_spent_building(self):
        rows = [{"harness_stops": 3, "building": 3600, "elapsed": 36000, "lost": {"harness": 1800}, "evals": 1},
                {"harness_stops": 0, "building": 3600, "elapsed": 4000, "lost": {"harness": 0}, "evals": 2}]
        v = scorecard.verdict(rows)
        self.assertIn("Harness stops: 3 in 2 batches (1.5 a batch, 0.5h lost); aim for 0.", v)
        self.assertIn("Building was 18% of the time", v)
        self.assertIn("evals that passed first time: 1 of 2", v)


if __name__ == "__main__":
    unittest.main()
