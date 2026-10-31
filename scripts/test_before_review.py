"""Unit tests for scripts/lib/before_review.py: the before-review the loop records without an agent."""
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "scripts", "lib"))
import before_review  # noqa: E402

HEAD = "a" * 40


class Body(unittest.TestCase):
    def test_it_names_the_memory_it_starts_from_and_how_far_the_code_moved(self):
        memory = {"review": {"commit": "b" * 40}, "batch_title": "Login on the API"}
        got = before_review.body(memory, HEAD, " 3 files changed, 10 insertions(+)")
        self.assertIn('"Login on the API" at bbbbbbbbbbbb', got)
        self.assertIn("git diff --shortstat bbbbbbbbbbbb..aaaaaaaaaaaa", got)
        self.assertIn("3 files changed", got)

    def test_an_empty_diff_says_nothing_has_changed(self):
        self.assertIn("nothing has changed", before_review.body({"review": {"commit": "b" * 40}}, HEAD, ""))

    def test_a_project_with_no_memory_yet_says_so(self):
        got = before_review.body({"review": None}, HEAD, None)
        self.assertIn("no project memory yet", got)
        self.assertIn("aaaaaaaaaaaa", got)

    def test_an_empty_repository_is_described_by_the_empty_tree_sha(self):
        self.assertRegex(before_review.EMPTY_TREE, r"^[0-9a-f]{40}$")


if __name__ == "__main__":
    unittest.main()
