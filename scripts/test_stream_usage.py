"""Unit tests for the one parser of a step log: what a step cost, and how much of the plan it has
used (TP-229).

The windows are the spending guard's only input, and they are read from a log the CLI writes without
being asked, so the field names are not ours to choose: `scripts/testdata/usage-windows.jsonl` is
shaped from a real `/tmp/ticketing-loop.*/N-builder.jsonl`, down to `rate_limit_info.unifiedWindows`
and the camel-case `resetsAt` inside it. A real log carries several of these events, rising as the
step runs, and ends half-written when the process is killed — so "the last one wins" and "a truncated
log reports as no data" are the two behaviours that matter, and both are pinned here. `usage()` is
pinned alongside them because TP-229 moved it onto the shared `events` scan; eval.sh and loop.sh
parse its output, so the two CLI forms are pinned as text, not just as Python.
"""
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "lib", "stream_usage.py")
FIXTURE = os.path.join(ROOT, "scripts", "testdata", "usage-windows.jsonl")
TRANSCRIPT = os.path.join(ROOT, "scripts", "testdata", "usage-transcript.jsonl")

sys.path.insert(0, os.path.join(ROOT, "scripts", "lib"))
import stream_usage  # noqa: E402

LATER = {"five_hour": {"utilization": 0.02, "resets_at": 1790672400},
         "seven_day": {"utilization": 0.38, "resets_at": 1791172800}}


def log(tmp, *lines):
    """A step log holding exactly these lines, verbatim so a half-written one stays half-written."""
    path = os.path.join(tmp, "step.jsonl")
    with open(path, "w") as f:
        f.write("".join(line + "\n" for line in lines))
    return path


def read(path):
    with open(path) as f:
        return json.load(f)


def event(five, seven):
    return json.dumps({"type": "rate_limit_event", "rate_limit_info": {
        "unifiedWindows": {"five_hour": {"utilization": five, "resetsAt": 1790672400},
                           "seven_day": {"utilization": seven, "resetsAt": 1791172800}}}})


class Windows(unittest.TestCase):
    def test_the_last_rate_limit_event_in_the_log_wins(self):
        # The fixture's two events differ (0.01/0.37 then 0.02/0.38); a real log's usage only rises,
        # so reading the earlier one would under-report and the guard would stop too late.
        self.assertEqual(stream_usage.windows(FIXTURE), LATER)

    def test_a_log_with_no_rate_limit_event_reads_as_usage_unknown(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = log(tmp, json.dumps({"type": "assistant"}), json.dumps({"type": "result", "total_cost_usd": 1}))
            self.assertEqual(stream_usage.windows(path), {})

    def test_a_truncated_or_empty_log_reports_no_data_rather_than_crashing(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(stream_usage.windows(log(tmp, '{"type":"rate_limit_ev')), {})
            self.assertEqual(stream_usage.windows(log(tmp, "", "  ")), {})
            self.assertIsNone(stream_usage.usage(log(tmp, '{"type":"resu'))["cost_usd"])

    def test_a_missing_file_reads_as_usage_unknown(self):
        missing = os.path.join(ROOT, "no", "such", "step.jsonl")
        self.assertEqual(stream_usage.windows(missing), {})
        self.assertIsNone(stream_usage.usage(missing)["cost_usd"])

    def test_an_event_carrying_no_window_is_passed_over_for_an_older_one_that_has_them(self):
        # The newest event is not always the most informative: an empty one must not be returned as
        # the answer, or one malformed event at the tail would blind the guard for the whole step.
        with tempfile.TemporaryDirectory() as tmp:
            path = log(tmp, event(0.05, 0.4), json.dumps({"type": "rate_limit_event", "rate_limit_info": {}}))
            self.assertEqual(stream_usage.windows(path)["seven_day"]["utilization"], 0.4)

    def test_a_partial_event_gives_the_window_it_has(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = log(tmp, json.dumps({"type": "rate_limit_event", "rate_limit_info": {
                "unifiedWindows": {"five_hour": {"utilization": 0.9}}}}))
            self.assertEqual(stream_usage.windows(path), {"five_hour": {"utilization": 0.9, "resets_at": 0}})


class WriteWindows(unittest.TestCase):
    def test_it_writes_both_windows_and_a_seen_at_creating_the_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "state", "ticketing", "usage-windows.json")
            before = int(time.time())
            written = stream_usage.write_windows(FIXTURE, out)
            got = read(out)
            self.assertEqual({k: v for k, v in got.items() if k != "seen_at"}, LATER)
            self.assertTrue(before <= got["seen_at"] <= int(time.time()))
            self.assertEqual(got, written)

    def test_empty_windows_leave_the_last_real_figures_in_place(self):
        # A step whose log carried no event says nothing about the plan, so it must not erase what the
        # step before it saw: stale figures are still usable, an emptied file is not.
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "usage-windows.json")
            stream_usage.write_windows(FIXTURE, out)
            kept = read(out)
            self.assertEqual(stream_usage.write_windows(log(tmp, '{"type":"assistant"}'), out), {})
            self.assertEqual(read(out), kept)
            self.assertEqual(sorted(os.listdir(tmp)), ["step.jsonl", "usage-windows.json"],
                             "no temp file is left behind")


class Cli(unittest.TestCase):
    def run_script(self, *args):
        p = subprocess.run([sys.executable, SCRIPT, *args], capture_output=True, text=True, check=True)
        return p.stdout.strip()

    def test_the_windows_form_prints_the_windows_as_json(self):
        self.assertEqual(json.loads(self.run_script("--windows", FIXTURE)), LATER)

    def test_the_windows_form_with_a_second_path_writes_it_and_prints_nothing(self):
        # This is the form loop.sh uses, so its silence is part of the contract: the step line is
        # loop.sh's to print, not the parser's.
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "state", "usage-windows.json")
            self.assertEqual(self.run_script("--windows", FIXTURE, out), "")
            self.assertEqual({k: v for k, v in read(out).items() if k != "seen_at"}, LATER)

    def test_the_cost_forms_are_unchanged_by_the_shared_scan(self):
        # eval.sh reads the bare form as five words and loop.sh posts the --json form to the API, so
        # both are pinned as text: TP-229 moved usage() onto events and neither may shift.
        self.assertEqual(self.run_script(FIXTURE), "0.4213 120 60 30 900")
        self.assertEqual(json.loads(self.run_script("--json", FIXTURE)), {
            "cost_usd": 0.4213, "input_tokens": 120, "output_tokens": 60,
            "cache_creation_input_tokens": 30, "cache_read_input_tokens": 900, "wall_time_seconds": 7})

    def test_the_json_form_drops_the_cost_when_the_log_has_none(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertNotIn("cost_usd", json.loads(self.run_script("--json", log(tmp, ""))))


class UsageReport(unittest.TestCase):
    """scripts/usage-report.sh, the other reader of a session's cost: it turns a hand-worked
    session's transcript into the post that lands on that ticket's claim (TP-272, from a demo script
    deleted in TP-269). The server side — which claim it lands on, and that a second report
    replaces rather than doubles — is TestUsageLandsOnTheClaimThatDidTheWork's; what is here is the
    script: what it reads out of the transcript, where it sends it, and its refusal to guess a key.
    """

    def report(self, *args, api="http://127.0.0.1:1"):
        p = subprocess.run([os.path.join(ROOT, "scripts", "usage-report.sh"), *args],
                           capture_output=True, text=True, stdin=subprocess.DEVNULL,
                           env={**os.environ, "API": api})
        return p.returncode, (p.stdout + p.stderr).strip()

    def test_it_sums_the_transcript_and_posts_it_to_the_named_ticket(self):
        posts = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                posts.append((self.path, self.headers.get("X-Actor"),
                              json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        code, out = self.report("--key", "TP-1", "--session", "sess-1", TRANSCRIPT,
                                api=f"http://127.0.0.1:{server.server_address[1]}")
        self.assertEqual(0, code, out)
        self.assertEqual(1, len(posts), posts)
        path, actor, body = posts[0]
        self.assertEqual(("/api/tickets/TP-1/usage", "usage-report"), (path, actor))
        # A streamed message is counted once (msg_b's 300, not 100 + 300), and the wall time is the
        # first timestamp to the last: 10:00:00 to 10:10:00.
        self.assertEqual({"input_tokens": 20, "output_tokens": 355, "cache_creation_input_tokens": 21000,
                          "cache_read_input_tokens": 40000, "wall_time_seconds": 600,
                          "session_id": "sess-1"}, body)

    def test_since_and_until_cut_one_step_out_of_a_shared_transcript(self):
        """TP-296 (lesson TP-282): when the loop's own account is spent, its steps run as subagents of
        a supervising session, and one agent's transcript is cumulative across the steps it ran. The
        bounds are what lets each of those steps post its own figures rather than none at all."""
        for label, args, want in (
            ("bounded to the last two messages",
             ("--since", "2026-09-24T10:04:10Z", "--until", "2026-09-24T10:10:00Z"),
             {"input_tokens": 10, "output_tokens": 350, "cache_creation_input_tokens": 1000,
              "cache_read_input_tokens": 40000, "wall_time_seconds": 330}),
            ("bounded to the first message alone", ("--until", "2026-09-24T10:00:05Z"),
             {"input_tokens": 10, "output_tokens": 5, "cache_creation_input_tokens": 20000,
              "cache_read_input_tokens": 0, "wall_time_seconds": 5}),
            ("not bounded at all", (),
             {"input_tokens": 20, "output_tokens": 355, "cache_creation_input_tokens": 21000,
              "cache_read_input_tokens": 40000, "wall_time_seconds": 600}),
        ):
            with self.subTest(label):
                code, out = self.report("--key", "TP-1", "--dry-run", *args, TRANSCRIPT)
                self.assertEqual(0, code, out)
                self.assertEqual(want, json.loads(out[out.index("{"):]))

    def test_an_offset_bound_and_a_z_bound_select_the_same_lines(self):
        """TP-303: the bounds were compared with the transcript's stamps as strings, so
        `--since 2026-09-24T11:04:10+01:00` — the same instant as 10:04:10Z, and what `date
        -Iseconds` prints on this machine — sorted above every line and the script posted zeros.
        A silent zero is worse than no report: the claim then counts as measured at nothing, which
        is what the digest's cost section and the loop's usage guard both read."""
        code, out = self.report("--key", "TP-1", "--dry-run", "--since", "2026-09-24T11:04:10+01:00",
                                "--until", "2026-09-24T11:10:00+01:00", TRANSCRIPT)
        self.assertEqual(0, code, out)
        self.assertEqual({"input_tokens": 10, "output_tokens": 350, "cache_creation_input_tokens": 1000,
                          "cache_read_input_tokens": 40000, "wall_time_seconds": 330},
                         json.loads(out[out.index("{"):]))
        for label, args, expected in (
            ("a bound that is not a time", ("--since", "yesterday"), "not a time"),
            ("a window holding no line", ("--since", "2026-09-25T00:00:00Z"), "no lines"),
        ):
            with self.subTest(label):
                code, out = self.report("--key", "TP-1", "--dry-run", *args, TRANSCRIPT)
                self.assertNotEqual(0, code, out)
                self.assertIn(expected, out)
                self.assertNotIn("would post", out)

    def test_it_refuses_rather_than_guessing(self):
        for label, args, expected in (
            ("a report that does not name its ticket", (TRANSCRIPT,), "--key KEY"),
            ("a transcript that is not there", ("--key", "TP-1", "no/such.jsonl"), "no transcript at"),
        ):
            with self.subTest(label):
                code, out = self.report(*args)
                self.assertEqual(2, code, out)
                self.assertIn(expected, out)


if __name__ == "__main__":
    unittest.main()


class Ledger(unittest.TestCase):
    """The per-step ledger (lean harness step 1): a claim keeps only the last step posted to its
    ticket and every other step posts nowhere, so this file is the one complete record of spend."""

    RESULT = json.dumps({"type": "result", "total_cost_usd": 0.5, "duration_ms": 3000,
                         "modelUsage": {"claude-sonnet-5-5": {}, "claude-haiku-5-5": {}},
                         "usage": {"input_tokens": 10, "output_tokens": 20,
                                   "cache_creation_input_tokens": 30, "cache_read_input_tokens": 40}})
    INIT = json.dumps({"type": "system", "subtype": "init", "model": "claude-opus-5-5"})

    def test_the_model_is_every_model_the_result_names_else_the_init_model_else_unknown(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual("claude-haiku-5-5,claude-sonnet-5-5", stream_usage.model(log(tmp, self.INIT, self.RESULT)))
            self.assertEqual("claude-opus-5-5", stream_usage.model(log(tmp, self.INIT, '{"type":"res')))
            self.assertEqual("unknown", stream_usage.model(log(tmp, "")))

    def test_each_step_appends_one_line_with_its_fields_model_and_usage(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "state", "ledger.jsonl")
            step = log(tmp, self.INIT, self.RESULT)
            subprocess.run([sys.executable, SCRIPT, "--ledger", step, out, "step=builder", "ticket=TP-1"], check=True)
            subprocess.run([sys.executable, SCRIPT, "--ledger", step, out, "step=evaluator", "ticket=TP-2"], check=True)
            with open(out) as f:
                rows = [json.loads(line) for line in f]
            self.assertEqual(["builder", "evaluator"], [r["step"] for r in rows])
            self.assertEqual({"ticket": "TP-1", "model": "claude-haiku-5-5,claude-sonnet-5-5", "cost_usd": 0.5,
                              "input_tokens": 10, "output_tokens": 20, "cache_creation_input_tokens": 30,
                              "cache_read_input_tokens": 40, "wall_time_seconds": 3},
                             {k: rows[0][k] for k in ("ticket", "model", "cost_usd", "input_tokens", "output_tokens",
                                                      "cache_creation_input_tokens", "cache_read_input_tokens",
                                                      "wall_time_seconds")})
            self.assertIsInstance(rows[0]["ts"], int)
