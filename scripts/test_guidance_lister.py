"""What guidance-check.sh prints per served document (TP-235, TP-192).

It reads `GET /api/guidance`, turns it into one `slug|commit|paths` line per document, and a
`while IFS='|' read` loop decides about each. Nothing below names the module that does the reading:
these are the script's own lines off its stdout, which is what lets the reader be replaced underneath
without this file being touched. The stub, the scratch repository and the runner come from
test_guidance_check.py rather than a second copy, which would be a second thing to keep true.
"""
import os
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
from test_guidance_check import (  # noqa: E402
    BANNER_LINE, BODY, Repo, guidance_file_text, run_check, served,
)


class EveryShapeOfServedDocument(unittest.TestCase):
    """One scratch repository, every shape at once, run twice — plainly and with --accept. The nine
    shapes and the line each produces are the assertion below; two pay for the line format. alpha
    (two source_paths, the second changed) needs them split into two pathspecs: as the single
    argument "api/service.go web/app.tsx" git finds no history and the script says MISSING instead.
    beta (no source_commit) is why the separator is a pipe and not a tab — bash collapses runs of IFS
    whitespace, so an empty commit field would swallow beta's path and it would read as unbound."""

    DRIFT_BEHIND = "DRIFT   behind: file lacks 2 served lines, has 0 the server lacks"
    DRIFT_AHEAD = "DRIFT   ahead: file lacks 0 served lines, has 1 the server lacks"

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        repo = Repo(cls.tmp.name)
        cls.first = repo.commit("api/service.go", "package service\n", "TP-0: the service")
        cls.second = repo.commit("web/app.tsx", "export {}\n", "TP-1: the web app")
        for slug, body in (("insync", BODY), ("behind", BODY),
                           ("ahead", BODY + "- A rule only the file has.\n"), ("delivered", BODY)):
            cls.head = repo.commit(f"docs/guidance/{slug}.md", guidance_file_text(slug, body), f"file {slug}")
        docs = [served("alpha", cls.first, "api/service.go", "web/app.tsx"),
                served("beta", None, "api/service.go"), served("gamma", None),
                served("delta", cls.second, "api/service.go")] + [
                    served(s, cls.head, "api/service.go")
                    for s in ("insync", "behind", "ahead", "delivered", "unfiled")]
        bodies = {"insync": BODY, "ahead": BODY, "unfiled": BODY,
                  "behind": BODY + "- A rule added through the API.\n- And another.\n",
                  "delivered": BANNER_LINE + BODY}
        cls.done, cls.stub = run_check(cls.tmp.name, docs, bodies)
        cls.accepted, cls.accept_stub = run_check(cls.tmp.name, docs, bodies, "--accept")

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    @staticmethod
    def lines_of(done):
        return [ln for ln in done.stdout.splitlines() if ln.strip()]

    def test_each_document_produces_exactly_its_line_in_order(self):
        self.assertEqual([
            f"STALE   alpha: api/service.go web/app.tsx changed since {self.first[:8]}"
            f" (now {self.second[:8]})",
            f"          {self.second[:8]} TP-1: the web app",
            f"unreviewed beta: no baseline commit — run 'just guidance-check --accept'"
            f" to set it to {self.head[:8]}",
            "unbound gamma (no source_paths)",
            "ok      delta",
            # The file comparison is added to the per-slug verdict, not swapped for it: a file that
            # matches says nothing, nor does the banner alone, nor a served document with no file.
            "ok      insync", self.DRIFT_BEHIND, "ok      behind",
            self.DRIFT_AHEAD, "ok      ahead", "ok      delivered", "ok      unfiled",
        ], self.lines_of(self.done))
        self.assertEqual(1, self.done.returncode, self.done.stderr)

    def test_only_the_documents_it_had_an_opinion_about_are_reviewed(self):
        # unreviewed is a gap in operations and unbound is coupled to nothing, so neither writes a
        # review. Nor does drift set a document stale: the served text is the truth (CLAUDE.md), so
        # a file that is behind says nothing about what an agent is about to read.
        reviewed = dict(self.stub.reviews)
        self.assertEqual({"alpha", "delta", "insync", "behind", "ahead", "delivered", "unfiled"},
                         set(reviewed))
        self.assertTrue(reviewed["alpha"]["stale"])
        self.assertIn(self.second[:8], reviewed["alpha"]["stale_reason"])
        self.assertEqual([{"stale": False}] * 6, [reviewed[s] for s in
                         ("delta", "insync", "behind", "ahead", "delivered", "unfiled")])

    def test_accept_pins_every_coupled_document_at_head_and_still_reports_the_drift(self):
        # --accept is the button for "I have just re-read these", which says nothing about whether
        # the file still matches. Letting it pass would leave `just guidance` reporting a clean run
        # over a file one flag away from deleting served rules.
        at = f"@ {self.head[:8]}"
        self.assertEqual([
            f"accept  alpha {at}", f"accept  beta {at}",
            "unbound gamma (no source_paths)",  # still all that is said about an uncoupled document
            f"accept  delta {at}", f"accept  insync {at}",
            self.DRIFT_BEHIND, f"accept  behind {at}",
            self.DRIFT_AHEAD, f"accept  ahead {at}",
            f"accept  delivered {at}", f"accept  unfiled {at}",
        ], self.lines_of(self.accepted))
        self.assertEqual(1, self.accepted.returncode, "drift is a complaint --accept cannot clear")
        self.assertEqual([(s, {"source_commit": self.head, "stale": False, "accepted": True})
                          for s in ("alpha", "beta", "delta", "insync", "behind", "ahead",
                                    "delivered", "unfiled")], self.accept_stub.reviews)


if __name__ == "__main__":
    unittest.main()
