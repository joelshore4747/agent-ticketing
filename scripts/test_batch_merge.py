"""scripts/batch-merge.sh held to its outward behaviour, by running it for real against a scratch git
repository and a stub API (TP-272).

These four behaviours were pinned only by a demo script (deleted in TP-269), which needed Docker, a
database and a real API to say anything; here the batch is whatever JSON the stub is told to serve,
so the refusals can be provoked in a second each. What the tracker does around the merge — the branch a plan
batch names, the commands finish_batch hands back — stays the API's own tests' to pin; this is the
script: it refuses an unfinished batch, refuses while a ticket branch is still outside the batch
branch, changes nothing under --dry-run, and otherwise puts the whole batch into main as one merge.
"""
import json
import os
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "batch-merge.sh")
BATCH_ID = "e51554f6-cd76-43d1-b362-54786931e718"
REF = "batch/1-order-the-work"
TITLE = "Order the work"


class StubBatchAPI:
    """GET /api/batches/:id, answering with the one batch the test handed it."""

    def __init__(self, payload):
        paths = self.paths = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                paths.append(self.path)
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_address[1]}"

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class BatchMerge(unittest.TestCase):
    """A batch of two done tickets, each on its own branch off the batch branch."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.repo = tmp.name
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Test")
        self.git("config", "commit.gpgsign", "false")
        self.commit("README", "start")
        self.git("switch", "-q", "-c", REF)
        self.branches = [self.work("TP-1", "position.txt"), self.work("TP-2", "refuse.txt")]
        self.git("switch", "-q", "main")

    def git(self, *args):
        return subprocess.run(["git", "-C", self.repo, *args],
                              check=True, capture_output=True, text=True).stdout.strip()

    def commit(self, path, text):
        with open(os.path.join(self.repo, path), "w") as f:
            f.write(text + "\n")
        self.git("add", path)
        self.git("commit", "-qm", f"{path}: {text}")

    def work(self, key, path):
        """What a claim's next_steps say: branch from the batch branch, commit, and leave it there."""
        branch = f"ticket/{key.lower()}-demo"
        self.git("switch", "-q", REF)
        self.git("switch", "-q", "-c", branch)
        self.commit(path, key)
        self.git("switch", "-q", REF)
        return branch

    def merge_back(self, *branches):
        self.git("switch", "-q", REF)
        for b in branches:
            self.git("merge", "-q", "--no-ff", b, "-m", f"Merge {b}")
        self.git("switch", "-q", "main")

    def run_merge(self, *args, finished=True, ref=REF, tickets=("TP-1", "TP-2"), env=None):
        stub = StubBatchAPI({
            "batch": {"id": BATCH_ID, "title": TITLE, "git_ref": ref,
                      "finished_at": "2026-09-29T12:00:00Z" if finished else None},
            "tickets": [{"key": k, "status": "done"} for k in tickets],
        })
        self.addCleanup(stub.close)
        self.stub = stub
        clean = {k: v for k, v in os.environ.items() if k != "LOOP_PROJECT"}
        p = subprocess.run([SCRIPT, BATCH_ID, *args], capture_output=True, text=True,
                           env={**clean, "API": stub.url, "REPO": self.repo, **(env or {})})
        return p.returncode, (p.stdout + p.stderr).strip()

    def test_it_refuses_a_batch_that_is_not_ready(self):
        for label, kwargs, expected in (
            ("a batch that has not finished", {"finished": False}, "has not finished"),
            ("a finished batch with no branch", {"ref": None}, "has no branch"),
            ("a finished batch whose ticket branch is still outside it", {}, "ticket/tp-2-demo"),
        ):
            with self.subTest(label):
                before = self.git("rev-parse", "main")
                code, out = self.run_merge(**kwargs)
                self.assertNotEqual(0, code, out)
                self.assertIn("refused", out)
                self.assertIn(expected, out)
                self.assertEqual(before, self.git("rev-parse", "main"), "main must not move on a refusal")

    def test_a_connected_projects_batch_is_asked_for_under_that_project(self):
        # The loop exports LOOP_PROJECT; without it the API looks the batch up in the default project
        # and answers 404, which stopped the first FS batch at its merge (lean harness, 2026-10-15).
        self.run_merge("--dry-run", env={"LOOP_PROJECT": "FS"})
        self.assertEqual(f"/api/batches/{BATCH_ID}?project=FS", self.stub.paths[0])
        self.run_merge("--dry-run")
        self.assertEqual(f"/api/batches/{BATCH_ID}", self.stub.paths[0])

    def test_dry_run_shows_the_merge_and_changes_nothing(self):
        self.merge_back(*self.branches)
        before = self.git("rev-parse", "main")
        code, out = self.run_merge("--dry-run")
        self.assertEqual(0, code, out)
        self.assertIn(f"would run: git switch main && git merge --no-ff {REF}", out)
        self.assertEqual(before, self.git("rev-parse", "main"))

    def test_it_merges_the_batch_into_main_as_one_commit_carrying_every_ticket(self):
        self.merge_back(*self.branches)
        code, out = self.run_merge()
        self.assertEqual(0, code, out)
        self.assertEqual(f"Merge {REF}: {TITLE} (batch {BATCH_ID})",
                         self.git("log", "-1", "--format=%s", "main"))
        self.assertEqual("1", self.git("rev-list", "--count", "--merges", "--first-parent", "main"),
                         "main takes the batch as one merge, not one per ticket")
        self.assertEqual({"README", "position.txt", "refuse.txt"},
                         set(self.git("ls-tree", "--name-only", "main").split()))

    def test_a_done_ticket_with_no_branch_is_noted_rather_than_refused(self):
        self.merge_back(*self.branches)
        code, out = self.run_merge(tickets=("TP-1", "TP-2", "TP-3"))
        self.assertEqual(0, code, out)
        self.assertIn("note: TP-3 is done but has no ticket/tp-3-* branch here", out)


if __name__ == "__main__":
    unittest.main()
