"""A batch's measured size is its own tickets, not whatever else rode the branch in (TP-180).

Batch 20c65c62 was recorded at +278/-283 (net -5) when its six planned tickets came to +223/-263
(net -40). The difference was TP-176 and TP-178: real, evidenced, merged onto `batch/2-deletion-pass`
and in nobody's plan. That -5 is what the eval read as passing the batch's own cleanup check and what
goal 9's size-over-time series remembers, and the Narrator — which explains a batch from its ticket
list — had no way to account for a third of it.

So the fixture below is that history, rebuilt: the same six merge subjects in the same three shapes,
an earlier batch merged into the branch, and older ticket merges both before the branch was cut and
inside that earlier batch. The scan has to come back with exactly TP-176 and TP-178. The three
negatives are what make it a measurement rather than a guess — name a ticket from before the cut and
every long-lived branch reads as off-plan; miss one and the figure is mixed again.

The other half of a figure that tells the truth is what it counts (TP-212). The rule counted a test line
the same as a code line, so the cheapest way under the ceiling was to thin the test that proved the
change: 63% of batch 3's growth was tests. So the script splits the diff by path, and SPLIT_FILES below
is one file per pattern it recognises.
"""
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "batch-size.sh")

BATCH_ID = "20c65c62-cfe1-4f7a-a32a-7af4650e886b"
REF = "batch/2-deletion-pass"
# Verbatim from `git log --merges --first-parent` on the real branch. The shapes matter: a subject
# written by hand, the git default for a branch merged into a non-main branch, and the one
# batch-merge.sh writes, whose trailing uuid must never be read as a ticket key.
BY_HAND = "Merge TP-169: the dead-code check is one command, just deadcode"
GIT_DEFAULT = "Merge branch 'ticket/tp-170-plans-go-duplicates-git-ref-resolution' into batch/2-deletion-pass"
EARLIER_BATCH = "Merge batch/1-shrinking-you-can-check: Shrinking you can check (batch 840686a8-2d68-488d-8792-05d8f37c2571)"
PLAN = ["TP-161", "TP-169", "TP-170", "TP-171", "TP-177", "TP-166"]
OFF_PLAN = ["TP-176", "TP-178"]

# A branch whose diff is a known mix, for the code/test split (TP-212). One file per pattern the script
# recognises, each a different number of lines, so a pattern that silently stopped matching changes the
# figure rather than leaving it plausible: 10 lines of code, and 7 + 5 + 3 + 2 = 17 of tests.
SPLIT_REF = "batch/9-split"
SPLIT_FILES = [
    ("api/internal/service/measures.go", 10, False),
    ("api/internal/service/measures_test.go", 7, True),
    ("web/src/Batches.test.tsx", 5, True),
    ("scripts/test_thing.py", 3, True),
    ("web/tests/render.spec.ts", 2, True),  # a test by its directory, not its name
    ("web/package-lock.json", 11, None),    # None: a lockfile, counted as neither (lean harness)
]
SPLIT_CODE = sum(n for _, n, is_test in SPLIT_FILES if is_test is False)
SPLIT_TESTS = sum(n for _, n, is_test in SPLIT_FILES if is_test)
# A rename with an edit, which is the one path shape numstat does not print plainly.
RENAME_REF = "batch/9-rename"


class StubAPI:
    """Stands in for GET /api/batches/:id and POST /api/batches/:id/size."""

    def __init__(self):
        self.queries = []
        self.batch = {}
        self.tickets = []
        self.posts = []
        stub = self

        class Handler(BaseHTTPRequestHandler):
            def _send(self, code, payload=b""):
                self.send_response(code)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_GET(self):
                path, _, query = self.path.partition("?")
                stub.queries.append(query)
                if path != f"/api/batches/{BATCH_ID}":
                    return self._send(404)
                body = {"batch": stub.batch, "tickets": stub.tickets}
                self._send(200, json.dumps(body).encode())

            def do_POST(self):
                if self.path != f"/api/batches/{BATCH_ID}/size":
                    return self._send(404)
                length = int(self.headers.get("Content-Length") or 0)
                stub.posts.append(json.loads(self.rfile.read(length) or b"{}"))
                self._send(200, b"{}")

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_address[1]}"


class OffPlanTickets(unittest.TestCase):
    """Drives the real script against a rebuilt batch/2-deletion-pass, and against two plain branches
    for the code/test split the ceiling is read on (TP-212)."""

    @classmethod
    def setUpClass(cls):
        cls.repo = tempfile.mkdtemp(prefix="batch-size-")
        cls.git("init", "-q", "-b", "main")
        cls.git("config", "user.email", "test@example.com")
        cls.git("config", "user.name", "Test")
        cls.git("config", "commit.gpgsign", "false")

        cls.commit("root")
        root = cls.rev("main")

        # The batch before this one: a ticket merged inside it, then it lands on main.
        cls.git("switch", "-q", "-c", "batch/1-shrinking-you-can-check")
        cls.merge_ticket("batch/1-shrinking-you-can-check", "ticket/tp-163-inside",
                         "Merge TP-163: a ticket inside the earlier batch")
        cls.git("switch", "-q", "main")
        cls.git("merge", "-q", "--no-ff", "batch/1-shrinking-you-can-check", "-m", EARLIER_BATCH)
        # What the API recorded as this batch's base: main's merge of the previous batch, which is
        # not an ancestor of the branch itself. That is the real shape, and 41ecb535 is that commit.
        cls.recorded_base = cls.rev("main")

        # This branch was cut before that landed, and carries an older ticket merge of its own from
        # before the previous batch caught up with it — as batch/2-deletion-pass carried TP-143.
        cls.git("switch", "-q", "-c", REF, root)
        cls.merge_ticket(REF, "ticket/tp-77-older", "Merge TP-77: an older ticket, before the cut")
        cls.git("merge", "-q", "--no-ff", "batch/1-shrinking-you-can-check", "-m", EARLIER_BATCH)
        cls.merge_ticket(REF, "ticket/tp-169-deadcode", BY_HAND)
        cls.merge_ticket(REF, "ticket/tp-170-plans-go-duplicates-git-ref-resolution", GIT_DEFAULT)
        cls.merge_ticket(REF, "ticket/tp-171-one-door", "Merge TP-171: one door for plan batches")
        cls.merge_ticket(REF, "ticket/tp-177-scaffolding", "Merge TP-177: delete the scaffolding")
        cls.merge_ticket(REF, "ticket/tp-176-demo", "Merge TP-176: the demo pins the loop inputs")
        cls.merge_ticket(REF, "ticket/tp-178-needs-joel", "Merge TP-178: a needs-joel ticket stops the loop")

        # Two more branches off the same root, for the split. They carry no merges, so the off-plan
        # scan has nothing to say about them and the figures are the only thing under test.
        cls.split_base = root
        cls.git("switch", "-q", "-c", SPLIT_REF, root)
        for name, lines, _ in SPLIT_FILES:
            cls.commit(name, name=name, lines=lines)
        cls.git("switch", "-q", "-c", RENAME_REF, SPLIT_REF)
        cls.git("mv", "api/internal/service/measures_test.go", "api/internal/service/size_test.go")
        # One line added, not a rewrite: git prints the "{old => new}" form only when it still sees the
        # two as the same file.
        with open(os.path.join(cls.repo, "api/internal/service/size_test.go"), "a", encoding="utf-8") as f:
            f.write("one more line\n")
        cls.git("add", "-A")
        cls.git("commit", "-q", "-m", "move the test and edit a line")

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo, ignore_errors=True)

    @classmethod
    def git(cls, *args):
        return subprocess.run(["git", "-C", cls.repo, *args], check=True,
                              capture_output=True, text=True).stdout.strip()

    @classmethod
    def rev(cls, ref):
        return cls.git("rev-parse", ref)

    @classmethod
    def commit(cls, slug, name=None, lines=2):
        """One file per commit, so branches that ran in parallel merge without conflicting."""
        name = name or slug.replace("/", "-") + ".txt"
        path = os.path.join(cls.repo, name)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write("".join(f"a line of {slug}\n" for _ in range(lines)))
        cls.git("add", name)
        cls.git("commit", "-q", "-m", f"work on {slug}")

    @classmethod
    def merge_ticket(cls, onto, branch, subject):
        cls.git("switch", "-q", "-c", branch, onto)
        cls.commit(branch)
        cls.git("switch", "-q", onto)
        cls.git("merge", "-q", "--no-ff", branch, "-m", subject)

    def setUp(self):
        self.api = StubAPI()
        self.api.batch = {"git_ref": REF, "title": "Deletion pass", "size_base_commit": None}
        self.api.tickets = [{"key": k, "status": "done"} for k in PLAN]
        self.addCleanup(self.api.server.shutdown)

    def run_script(self, *args, env=None):
        clean = {k: v for k, v in os.environ.items() if k != "LOOP_PROJECT"}
        return subprocess.run(
            ["bash", SCRIPT, BATCH_ID, *args], capture_output=True, text=True,
            env={**clean, "API": self.api.url, "REPO": self.repo, **(env or {})},
        )

    def test_it_refuses_and_names_the_tickets_the_batch_never_planned(self):
        got = self.run_script("--dry-run")
        self.assertEqual(got.returncode, 1, got.stdout + got.stderr)
        for key in OFF_PLAN:
            self.assertIn(key, got.stderr, f"{key} rode the branch in and was not named")

    def test_it_says_what_to_do_about_them(self):
        # A refusal that does not name the way out just gets --off-plan-ok pasted in forever.
        self.assertIn("add_to_batch", self.run_script("--dry-run").stderr)

    def named(self, *args):
        """The keys the refusal lists, one per indented line, in the order they landed."""
        stderr = self.run_script("--dry-run", *args).stderr
        return [line.split()[0] for line in stderr.splitlines() if line.startswith("  TP-")]

    def test_the_batchs_own_tickets_are_not_named(self):
        self.assertEqual(self.named(), OFF_PLAN)

    def test_a_ticket_merged_before_the_branch_was_cut_is_not_named(self):
        # The scan stops at the previous batch landing. Without that bound every ticket in the
        # history before this branch reads as off-plan and the refusal is unusable.
        self.assertNotIn("TP-77", self.named("--base", self.recorded_base))

    def test_a_ticket_inside_the_earlier_batch_is_not_named(self):
        # Reachable through that batch merge, but not a merge this branch made: --first-parent.
        self.assertNotIn("TP-163", self.named("--base", self.recorded_base))

    def test_the_uuid_in_a_batch_merge_subject_is_not_read_as_a_ticket_key(self):
        # "(batch 840686a8-2d68-...)" is hex and dashes; a loose key pattern reads one out of it.
        self.assertEqual(self.named("--base", self.recorded_base), OFF_PLAN)

    def test_it_refuses_before_reporting_anything(self):
        self.run_script()
        self.assertEqual(self.api.posts, [], "a mixed figure reached the digest anyway")

    def test_off_plan_ok_records_the_mixed_figure_and_still_names_them(self):
        got = self.run_script("--off-plan-ok")
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        for key in OFF_PLAN:
            self.assertIn(key, got.stderr)
        self.assertEqual(len(self.api.posts), 1, got.stdout + got.stderr)
        self.assertGreater(self.api.posts[0]["lines_added"], 0)

    def test_a_branch_carrying_only_its_own_plan_reports_without_the_flag(self):
        self.api.tickets = [{"key": k, "status": "done"} for k in PLAN + OFF_PLAN]
        got = self.run_script()
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertEqual(len(self.api.posts), 1)

    def test_a_landed_batch_is_measured_against_the_base_it_was_first_measured_from(self):
        # merge-base(main, ref) is the branch tip once it has landed, so the diff would be a silent
        # zero (TP-160). The recorded base is where it started, so re-measuring needs no argument.
        self.git("switch", "-q", "main")
        self.git("merge", "-q", "--no-ff", REF, "-m", f"Merge {REF}: Deletion pass (batch {BATCH_ID})")
        self.addCleanup(self.git, "reset", "-q", "--hard", "HEAD~1")
        self.api.batch["size_base_commit"] = self.recorded_base
        got = self.run_script("--dry-run")
        self.assertIn("already in main", got.stdout)
        self.assertEqual(got.returncode, 1, got.stdout + got.stderr)
        self.assertIn("TP-176", got.stderr)

    def measure(self, ref, base):
        """Run the script over a branch that carries no merges, and hand back what it said and posted."""
        self.api.batch = {"git_ref": ref, "title": "Split", "size_base_commit": None}
        self.api.tickets = []
        got = self.run_script("--base", base)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertEqual(len(self.api.posts), 1, got.stdout + got.stderr)
        return got.stdout, self.api.posts[0]

    def test_a_connected_projects_batch_is_read_and_reported_under_that_project(self):
        # The loop measures a connected project's batch itself (lean harness); without the project the
        # API looks the batch up in the default one and answers 404.
        self.api.batch = {"git_ref": SPLIT_REF, "title": "Split", "size_base_commit": None}
        self.api.tickets = []
        got = self.run_script("--base", self.split_base, env={"LOOP_PROJECT": "FS"})
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertEqual("project=FS", self.api.queries[-1])
        self.assertEqual("FS", self.api.posts[-1]["project"])

    def test_code_and_test_lines_are_counted_apart(self):
        # The ceiling counts code only (TP-212), so the figure it reads has to exclude the test that
        # proves the change. Charging both alike is what made thinning the test the cheap way through.
        stdout, posted = self.measure(SPLIT_REF, self.split_base)
        self.assertEqual(posted["lines_added"], SPLIT_CODE + SPLIT_TESTS, "lines_* is the whole diff")
        self.assertEqual(posted["test_lines_added"], SPLIT_TESTS, stdout)
        self.assertEqual(posted["test_lines_deleted"], 0, stdout)
        self.assertIn(f"code +{SPLIT_CODE} / -0", stdout)
        self.assertIn(f"tests +{SPLIT_TESTS} / -0", stdout)

    def test_a_renamed_test_file_is_still_a_test(self):
        # numstat prints a rename as "dir/{old => new}.go", which ends in "}" and so matches none of
        # the suffix patterns. Unnormalised, moving a test file charges its edits to code.
        stdout, posted = self.measure(RENAME_REF, self.rev(SPLIT_REF))
        self.assertEqual(posted["test_lines_added"], posted["lines_added"], stdout)
        self.assertIn("code +0 / -0", stdout)

    def test_a_landed_batch_nobody_ever_measured_still_refuses_rather_than_report_zero(self):
        self.git("switch", "-q", "main")
        self.git("merge", "-q", "--no-ff", REF, "-m", f"Merge {REF}: Deletion pass (batch {BATCH_ID})")
        self.addCleanup(self.git, "reset", "-q", "--hard", "HEAD~1")
        got = self.run_script("--dry-run")
        self.assertEqual(got.returncode, 1, got.stdout)
        self.assertIn("--base", got.stderr)
        self.assertEqual(self.api.posts, [])


if __name__ == "__main__":
    unittest.main()
