"""Unit tests for scripts/cost.py, the report over the per-step ledger (lean harness step 1)."""
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
import cost  # noqa: E402


def row(**kw):
    base = {"ts": 1_800_000_000, "project": "TP", "step": "builder", "model": "m", "batch": "b1", "ticket": "TP-1",
            "cost_usd": 1.0, "input_tokens": 10, "output_tokens": 5, "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 0}
    return {**base, **kw}


class Cost(unittest.TestCase):
    def ledger(self, tmp, *rows):
        path = os.path.join(tmp, "ledger.jsonl")
        with open(path, "w") as f:
            f.write("".join(json.dumps(r) + "\n" for r in rows) + "not json\n")
        return path

    def test_groups_sum_steps_tokens_and_cost_most_expensive_first(self):
        g = cost.group([row(step="builder", cost_usd=1.0), row(step="evaluator", cost_usd=4.0),
                        row(step="builder", cost_usd=2.5)], "step")
        self.assertEqual(["evaluator", "builder"], list(g))
        self.assertEqual((2, 3.5, 20), (g["builder"]["steps"], g["builder"]["cost_usd"], g["builder"]["input_tokens"]))

    def test_cache_hit_is_reads_over_all_input_and_zero_with_no_input(self):
        g = cost.group([row(input_tokens=10, cache_creation_input_tokens=30, cache_read_input_tokens=60)], "step")
        self.assertAlmostEqual(0.6, g["builder"]["cache_hit"])
        self.assertEqual(0.0, cost.group([row(input_tokens=0)], "step")["builder"]["cache_hit"])

    def test_filters_by_project_batch_and_since_and_skips_bad_lines(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self.ledger(tmp, row(), row(project="FS"), row(batch="b2"), row(ts=1))
            self.assertEqual(4, len(cost.rows(path)))
            self.assertEqual(1, len(cost.rows(path, project="FS")))
            self.assertEqual(1, len(cost.rows(path, batch="b2")))
            self.assertEqual(3, len(cost.rows(path, since=1_000)))

    def test_a_missing_ledger_reads_as_nothing_rather_than_crashing(self):
        self.assertEqual([], cost.rows("/nonexistent/ledger.jsonl"))
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(0, cost.main(["--ledger", "/nonexistent/ledger.jsonl"]))
        self.assertIn("no steps recorded", out.getvalue())


if __name__ == "__main__":
    unittest.main()
