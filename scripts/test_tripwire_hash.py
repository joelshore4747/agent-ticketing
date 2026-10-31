"""The definition of a tripwire's body_sha256, pinned (TP-134).

The golden hashes below are the whole point of this file. `RegisterTripwire` throws away a human's
confirmation whenever it is handed a hash that differs from the row's, and the API cannot check the
hash it is given, so a quiet change to what gets sliced would quietly drop confirmations across the
whole project. Here that change is a failing test naming the constant it broke.

The fixtures under testdata/ are the frozen input. Editing one fails these tests on purpose.
"""
import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
import tripwire_hash  # noqa: E402
from tripwire_hash import Problem, body, body_sha256, parse_ref  # noqa: E402

GO = "scripts/testdata/tripwire_sample_test.go"
TSX = "scripts/testdata/tripwire_sample.test.tsx"

# Change one of these only with a reason. Every tripwire registered before it was hashed the old way.
GO_FUNCTION = "80df5226830b47cdf1f715f97e66dd682d5e1a3b17762ab79847a2077d8cc387"
GO_WHOLE_FILE = "b79112654f4ed27eb4acbcf28a37729fc3093b690d152e5de1acb66c6c95cfe3"
TSX_SAMPLE_CASE = "2f8e692b800f92d90ab810ff0a8010dd9bed4eb354f6b0e4c90d6cbd82793c0a"
TSX_WRONG_BLOCK_CASE = "6e673668894d4b9ab96610345d8384b65c168b818d6a27a2b66f8a515de92075"


class TheDefinition(unittest.TestCase):
    """What the hash is taken over, byte for byte."""

    def setUp(self):
        os.chdir(ROOT)  # a test_ref's path is always relative to the repository root

    def test_a_go_test_is_func_through_the_brace_in_column_one(self):
        slice_ = body(f"{GO}:TestTheSliceStartsAtFunc").decode()
        self.assertTrue(slice_.startswith("func TestTheSliceStartsAtFunc(t *testing.T) {\n"), slice_)
        self.assertTrue(slice_.endswith("\t}\n}\n"), repr(slice_[-20:]))
        self.assertEqual(body_sha256(f"{GO}:TestTheSliceStartsAtFunc"), GO_FUNCTION)

    def test_the_comment_above_the_function_is_not_in_the_slice(self):
        self.assertNotIn("This comment is above the function", body(f"{GO}:TestTheSliceStartsAtFunc").decode())

    def test_the_next_function_is_not_in_the_slice(self):
        self.assertNotIn("TestTheNextFunctionIsNotInTheSlice", body(f"{GO}:TestTheSliceStartsAtFunc").decode())

    def test_an_indented_closing_brace_does_not_end_the_slice(self):
        # The `for` loop's `}` comes before the function's; only column 1 ends it.
        self.assertIn("t.Log(i)", body(f"{GO}:TestTheSliceStartsAtFunc").decode())

    def test_the_slice_always_ends_in_exactly_one_newline(self):
        for ref in (f"{GO}:TestTheSliceStartsAtFunc", f"{TSX} > the sample block > holds a case of the same name"):
            with self.subTest(ref=ref):
                self.assertTrue(body(ref).endswith(b"\n"), ref)
                self.assertFalse(body(ref).endswith(b"\n\n"), ref)

    def test_a_bare_path_is_the_whole_file(self):
        with open(os.path.join(ROOT, GO), "rb") as f:
            self.assertEqual(body(GO), f.read())
        self.assertEqual(body_sha256(GO), GO_WHOLE_FILE)

    def test_naming_the_script_itself_is_the_whole_file_too(self):
        # web/scripts/render-check.mjs:render-check — a whole script that is one check.
        self.assertEqual(body_sha256(f"{GO}:tripwire_sample_test"), GO_WHOLE_FILE)
        self.assertEqual(parse_ref(f"{GO}:tripwire_sample_test")[2], "file")

    def test_a_vitest_case_is_the_it_call_through_the_line_that_closes_it(self):
        slice_ = body(f"{TSX} > the sample block > holds a case of the same name").decode()
        self.assertTrue(slice_.startswith("  it('holds a case of the same name'"), slice_)
        self.assertTrue(slice_.endswith("  });\n"), repr(slice_[-20:]))
        self.assertIn("expect(2).toBe(2);", slice_)
        self.assertNotIn("sibling", slice_)  # the next `it` in the same describe stays out
        self.assertEqual(body_sha256(f"{TSX} > the sample block > holds a case of the same name"), TSX_SAMPLE_CASE)

    def test_a_brace_nested_deeper_does_not_close_the_vitest_case(self):
        self.assertIn("toEqual([2, 3]);", body(f"{TSX} > the sample block > holds a case of the same name").decode())

    def test_the_describe_picks_between_two_cases_of_the_same_name(self):
        wrong = body_sha256(f"{TSX} > the wrong block > holds a case of the same name")
        self.assertEqual(wrong, TSX_WRONG_BLOCK_CASE)
        self.assertNotEqual(wrong, TSX_SAMPLE_CASE)
        self.assertIn("expect(1).toBe(1);", body(f"{TSX} > the wrong block > holds a case of the same name").decode())


class Refusals(unittest.TestCase):
    """A ref that names nothing is an error, never a hash of something else.

    The fallback worth refusing is "hash the whole file when the function is not found": a misspelt
    test name would then register a hash that looks fine and pins the confirmation to the file.
    """

    def setUp(self):
        os.chdir(ROOT)

    def assertRefused(self, ref, *expected):
        with self.assertRaises(Problem) as caught:
            body_sha256(ref)
        for want in expected:
            self.assertIn(want, str(caught.exception))

    def test_an_unknown_go_test_is_refused_not_silently_the_whole_file(self):
        self.assertRefused(f"{GO}:TestNoSuchThing", "func TestNoSuchThing")

    def test_an_unknown_vitest_case_is_refused(self):
        self.assertRefused(f"{TSX} > the sample block > no such case", "no such case")

    def test_an_unknown_describe_is_refused(self):
        self.assertRefused(f"{TSX} > no such block > holds a case of the same name", "no such block")

    def test_a_missing_file_is_refused(self):
        self.assertRefused("scripts/testdata/nope_test.go:TestX", "nope_test.go")

    def test_a_half_written_vitest_ref_is_refused(self):
        self.assertRefused("a > b", "three parts")

    def test_an_empty_half_is_refused(self):
        self.assertRefused(":TestX", "non-empty")


class TheCommandLine(unittest.TestCase):
    """What the Approver actually runs (approver.md rule 7)."""

    def run_it(self, *args):
        return subprocess.run([sys.executable, "scripts/tripwire_hash.py", *args],
                              cwd=ROOT, capture_output=True, text=True)

    def test_it_prints_the_hash_and_the_ref_like_sha256sum(self):
        got = self.run_it(f"{GO}:TestTheSliceStartsAtFunc")
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertEqual(got.stdout, f"{GO_FUNCTION}  {GO}:TestTheSliceStartsAtFunc\n")

    def test_json_is_the_post_body_for_api_tripwires(self):
        got = self.run_it("--json", f"{GO}:TestTheSliceStartsAtFunc")
        self.assertEqual(got.returncode, 0, got.stderr)
        import json
        self.assertEqual(json.loads(got.stdout), {"test_ref": f"{GO}:TestTheSliceStartsAtFunc",
                                                  "body_sha256": GO_FUNCTION})

    def test_a_bad_ref_exits_non_zero_and_says_so_on_stderr(self):
        got = self.run_it(f"{GO}:TestNoSuchThing")
        self.assertEqual(got.returncode, 1)
        self.assertIn("func TestNoSuchThing", got.stderr)
        self.assertEqual(got.stdout, "")

    def test_no_arguments_is_the_usage_message(self):
        got = self.run_it()
        self.assertEqual(got.returncode, 2)
        self.assertIn("usage:", got.stderr)

    def test_the_shell_wrapper_works_from_any_directory(self):
        # Paths are relative to the repository root, so the wrapper cds there first.
        got = subprocess.run(["bash", os.path.join(ROOT, "scripts/tripwire-hash.sh"),
                              f"{GO}:TestTheSliceStartsAtFunc"],
                             cwd=os.path.dirname(ROOT), capture_output=True, text=True)
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn(GO_FUNCTION, got.stdout)


class TheDocumentedContract(unittest.TestCase):
    """The contract has to name the script, or the next caller invents a hash of their own again.

    That is the whole failure this ticket came from: `POST /api/tripwires` documented the field and
    not the bytes, so the Approver hashed something nobody could reproduce.
    """

    def read(self, path):
        with open(os.path.join(ROOT, path), encoding="utf-8") as f:
            return f.read()

    def test_docs_api_md_sends_the_caller_to_the_script(self):
        api = self.read("docs/api.md")
        self.assertIn("scripts/tripwire-hash.sh", api)
        self.assertIn("body_sha256", api)

    def test_docs_api_md_spells_out_each_shape_of_test_ref(self):
        api = self.read("docs/api.md")
        for shape in ("path:TestName", "path > describe > it", "basename without extension"):
            with self.subTest(shape=shape):
                self.assertIn(shape, api)

    def test_the_schema_comment_does_not_define_it_a_second_time(self):
        # Two places defining the same bytes is how they come to disagree.
        self.assertIn("scripts/tripwire-hash.sh", self.read("docs/schema.sql"))

    def test_the_module_docstring_still_carries_the_definition(self):
        self.assertIn("body_sha256", tripwire_hash.__doc__)


if __name__ == "__main__":
    unittest.main()
