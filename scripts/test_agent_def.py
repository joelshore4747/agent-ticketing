"""Unit tests for scripts/lib/agent_def.py: a role file as the JSON `claude --agents` takes."""
import glob
import os
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts", "lib"))
import agent_def  # noqa: E402


class Definition(unittest.TestCase):
    def test_frontmatter_becomes_the_definition_and_the_body_its_prompt(self):
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as f:
            f.write("---\nname: scout\ndescription: Looks: closely\nmodel: sonnet\ntools: Read, mcp__x__y\n---\nYou look.\n")
        self.addCleanup(os.unlink, f.name)
        self.assertEqual({"scout": {"description": "Looks: closely", "prompt": "You look.", "model": "sonnet",
                                    "tools": ["Read", "mcp__x__y"]}}, agent_def.definition(f.name))

    def test_a_role_with_no_tools_line_gets_no_tools_key_so_the_loop_list_applies(self):
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as f:
            f.write("---\nname: builder\ndescription: Builds\nmodel: sonnet\n---\nBuild.\n")
        self.addCleanup(os.unlink, f.name)
        self.assertNotIn("tools", agent_def.definition(f.name)["builder"])

    def test_every_real_role_file_converts(self):
        for path in glob.glob(os.path.join(ROOT, ".claude", "agents", "*.md")):
            name = os.path.splitext(os.path.basename(path))[0]
            got = agent_def.definition(path)[name]
            self.assertTrue(got["description"] and got["prompt"] and got["model"], name)


if __name__ == "__main__":
    unittest.main()
