"""Unit tests for scripts/lib/step_errors.py, the loop's reader of what went wrong in a step.

The fixture is built here rather than committed under scripts/testdata/, because a file there counts
as code in batch-size.sh and this one exists only for the test (TP-190). It is shaped like the
stream-json the CLI writes: a tool_use block names the tool, and the tool_result answering it by id
carries is_error — so the two have to be matched, and a result read on its own names nothing.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "lib", "step_errors.py")

sys.path.insert(0, os.path.join(ROOT, "scripts", "lib"))
import step_errors  # noqa: E402


def call(tool, uid):
    return {"type": "assistant", "message": {"content": [{"type": "tool_use", "id": uid, "name": tool}]}}


def result(uid, content, is_error=True):
    return {"type": "user", "message": {"content": [
        {"type": "tool_result", "tool_use_id": uid, "is_error": is_error, "content": content}]}}


def log(tmp, *events):
    path = os.path.join(tmp, "3-builder.jsonl")
    with open(path, "w") as f:
        f.write("".join(json.dumps(e) + "\n" for e in events))
    return path


class StepErrors(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()

    def test_a_repeated_tool_error_is_a_candidate_and_a_single_one_is_not(self):
        """Two runs of one mistake are one group with a count of 2; a mistake made once stays at 1,
        and a tool call that succeeded is not a mistake at all. The two 409s are written differently
        — one wrapped over two lines, one with the same text on one — because a lesson candidate the
        loop can act on has to survive the log's own formatting."""
        path = log(self.tmp,
                   call("mcp__ticketing__update_ticket", "t1"), result("t1", "409:\nthe ticket moved"),
                   call("Read", "t2"), result("t2", "no such file: api/nope.go"),
                   call("mcp__ticketing__update_ticket", "t3"), result("t3", "409: the ticket moved"),
                   call("Bash", "t4"), result("t4", "ok", is_error=False))
        counts = step_errors.failures([path])
        self.assertEqual({("mcp__ticketing__update_ticket", "409: the ticket moved"): 2,
                          ("Read", "no such file: api/nope.go"): 1}, dict(counts))

        # What loop.sh puts in the after-review prompt: the CLI's lines, recurring ones kept by awk.
        out = subprocess.run([sys.executable, SCRIPT, path], capture_output=True, text=True, check=True).stdout
        self.assertEqual("2  mcp__ticketing__update_ticket: 409: the ticket moved", out.splitlines()[0])
        self.assertEqual(2, len(out.splitlines()), out)
        recurring = [line for line in out.splitlines() if int(line.split()[0]) > 1]
        self.assertEqual(1, len(recurring), out)

    def test_it_counts_across_a_batch_of_logs_and_survives_a_log_it_cannot_read(self):
        """The loop asks about every step of the run at once, so an error made once in each of two
        steps is a repeat; and a step killed by a timeout leaves a half-written log, which must
        report as no data rather than crash the prompt that is asking (stream_usage's reader)."""
        first = log(self.tmp, call("Bash", "a"), result("a", "gofmt -l . returned api/x.go"))
        second = os.path.join(self.tmp, "5-builder.jsonl")
        with open(second, "w") as f:
            f.write(json.dumps(call("Bash", "b")) + "\n" + json.dumps(result("b", "gofmt -l . returned api/x.go")))
            f.write('\n{"type":"user","message":{"content":[{"type":"tool_re')  # killed mid-line
        counts = step_errors.failures([first, second, os.path.join(self.tmp, "no-such.jsonl")])
        self.assertEqual({("Bash", "gofmt -l . returned api/x.go"): 2}, dict(counts))

    def test_a_result_whose_tool_use_is_not_in_the_log_is_still_counted(self):
        """A log that starts mid-conversation carries results whose tool_use was never written. The
        error is the evidence; the tool name is how it reads, so the count is not lost with it."""
        path = log(self.tmp, result("gone", ["not a string", {"type": "text", "text": "a refusal"}]))
        self.assertEqual([("a tool", 1)], [(tool, n) for (tool, _), n in step_errors.failures([path]).items()])


    def test_loop_sh_hands_the_review_the_recurring_ones(self):
        """The wiring, run rather than read: loop.sh's own prompt_for is lifted out of the script and
        asked for a review prompt over a run directory holding one repeat and one single error.
        A candidate the review never sees is a candidate nobody files (TP-190)."""
        log(self.tmp,
            call("Bash", "a"), result("a", "git switch: already on that branch"),
            call("Bash", "b"), result("b", "git switch: already on that branch"),
            call("Read", "c"), result("c", "no such file"))
        function = subprocess.run(["sed", "-n", "/^prompt_for() {/,/^}$/p", os.path.join(ROOT, "scripts", "loop.sh")],
                                  capture_output=True, text=True, check=True).stdout
        script = os.path.join(self.tmp, "prompt_for.sh")
        with open(script, "w") as f:
            f.write(function + '\nprompt_for review batch-1 - "the batch is built"\n')
        prompt = subprocess.run(["bash", script], capture_output=True, text=True, check=True, cwd=ROOT,
                                env={**os.environ, "PROJECT": "TP", "REPO": ROOT, "RUN": self.tmp}).stdout
        self.assertIn("Lesson candidate, a tool call that failed this often while the batch was built: "
                      "2  Bash: git switch: already on that branch", prompt)
        self.assertNotIn("no such file", prompt)


if __name__ == "__main__":
    unittest.main()
