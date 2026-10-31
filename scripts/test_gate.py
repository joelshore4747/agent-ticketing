"""The render guard's place in the standing gate, pinned (TP-141).

`scripts/render-check.sh` is the only thing in this repo that catches a CSS regression: the web tests
run in jsdom and never load global.css, so a stylesheet refactor like TP-120's split had no guard at
all. It used to be its own `just render-check` target, chained into nothing, which meant a CSS change
could pass every gate that actually ran and still regress rendering.

So these tests are about wiring, not rendering. They fail if `just gate` stops running the render
check, if the gate stops being lint-then-test-then-render, if this file stops being run by `just
test`, or if the check starts passing quietly on a machine with no browser — the last being the
failure that would hurt most, because a green gate over an unchecked stylesheet is worse than no
gate. Rendering itself is checked by the baselines under scripts/render-baselines/.
"""
import os
import re
import shlex
import shutil
import subprocess
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def read(path):
    with open(os.path.join(ROOT, path), encoding="utf-8") as f:
        return f.read()


def recipe_body(name):
    """The indented lines of one Justfile recipe, comments and blanks dropped."""
    lines = read("Justfile").splitlines()
    header = re.compile(rf"^{re.escape(name)}(\s+[^:]*)?:")
    for i, line in enumerate(lines):
        if not header.match(line):
            continue
        body = []
        for following in lines[i + 1:]:
            if following.strip() == "":
                continue
            if not following.startswith((" ", "\t")):
                break
            stripped = following.strip()
            if not stripped.startswith("#"):
                body.append(stripped)
        return body
    return None


class TheGateRunsTheRenderCheck(unittest.TestCase):
    """`just gate` is where a change is stopped, so the render check has to be inside it."""

    def test_there_is_a_gate_recipe(self):
        self.assertIsNotNone(recipe_body("gate"), "no `gate` recipe in the Justfile")

    def test_it_runs_lint_then_test_then_the_render_check(self):
        # The order matters: the render check is the slowest by far, so the cheap failures come out
        # first. just stops the recipe at the first line that exits non-zero.
        self.assertEqual(recipe_body("gate"), ["just lint", "just test", "just render-check"])

    def test_the_render_check_is_not_quietly_dropped_from_it(self):
        self.assertIn("just render-check", recipe_body("gate"),
                      "the gate no longer runs the CSS guard; a stylesheet change can pass it unchecked")

    def test_just_test_runs_this_file(self):
        # Without this the guard above is unenforced: nothing would run it.
        self.assertIn("scripts/test_gate.py", " ".join(recipe_body("test")))

    def test_the_standing_guard_for_css_is_named_as_such(self):
        # The ticket asked for the intended guard to be documented, not just wired.
        self.assertIn("standing guard", read("Justfile"))
        self.assertIn("standing guard", read("scripts/render-check.sh"))
        self.assertIn("just gate", read("scripts/render-check.sh"))


class MissingToolingFailsLoudly(unittest.TestCase):
    """No browser means an unchecked stylesheet, which must never read as a pass.

    Playwright and Chromium are deliberately not project dependencies (the web app's budget is one
    markdown renderer, and the npm package would still leave the ~170MB browser to a separate
    install), so the check has to survive being run where they are absent. Exiting non-zero with the
    install command is that behaviour; returning 0 would hand `just gate` a false green.
    """

    def run_render_check(self, **env):
        return subprocess.run(
            [ "node", "scripts/render-check.mjs", "--api", "http://127.0.0.1:1",
              "--ticket", "TP-1", "--batch", "00000000-0000-0000-0000-000000000000" ],
            cwd=os.path.join(ROOT, "web"), capture_output=True, text=True,
            env={**os.environ, **env},
        )

    def test_no_playwright_exits_non_zero_and_names_the_install_command(self):
        got = self.run_render_check(RENDER_CHECK_PLAYWRIGHT="/nonexistent/playwright/index.mjs")
        self.assertNotEqual(got.returncode, 0, "a missing Playwright passed as if the CSS were checked")
        self.assertEqual(got.returncode, 2, got.stderr)
        self.assertIn("npx playwright install chromium", got.stderr)

    def test_an_override_pointing_at_nothing_is_not_silently_ignored(self):
        # Falling back to a cached copy here would hide the typo and check nothing anyone asked for.
        self.assertIn("no Playwright found",
                      self.run_render_check(RENDER_CHECK_PLAYWRIGHT="/nonexistent/typo.mjs").stderr)

    def test_the_missing_browser_message_also_names_the_install_command(self):
        # Reached only where Playwright is present but its browser is not, which is not reproducible
        # on every machine; the message is pinned here instead.
        source = read("web/scripts/render-check.mjs")
        self.assertIn("no Chromium found", source)
        self.assertIn("npx playwright install chromium", source)


def shell_function(name, script="scripts/run-checks.sh"):
    """One function lifted out of a script, to be run for real. Sourcing the whole file would run it
    against the live tracker, and a case that defines only the variables its function reads keeps the
    assertions on behaviour rather than on how the script around it is laid out."""
    return re.search(r"^" + name + r"\(\).*?^\}$", read(script), re.M | re.S).group()


class StaleGuidanceFailsTheGateOfTheTicketThatMovedIt(unittest.TestCase):
    """A guidance document is the gate's business when the branch under it is what moved the code
    (TP-295, lesson TP-280).

    Stale guidance used to surface one step later, at the eval, where loop.sh turns it into a
    doc-review step for an agent who no longer has the change in front of them: slots 5, 7, 8 and 11
    to 13, and three doc-review commits after slot 13's merge. `guidance_moved` is the half that
    decides, so these run it for real over a scratch repository. What must not happen is either
    direction of wrongness: passing a document this branch moved, or failing one it did not, which
    would make every ticket wait on another's drift.
    """

    @classmethod
    def setUpClass(cls):
        """A repository whose branch commit touches scripts/loop.py and nothing else."""
        cls.repo = tempfile.mkdtemp()
        git = lambda *a: subprocess.run(["git", "-C", cls.repo, *a], check=True, capture_output=True, text=True)
        os.makedirs(os.path.join(cls.repo, "scripts"))
        os.makedirs(os.path.join(cls.repo, "web"))
        for path in ("scripts/loop.py", "web/app.tsx"):
            with open(os.path.join(cls.repo, path), "w") as f:
                f.write("first\n")
        git("init", "-q")
        git("add", "-A")
        # Signing is off: Joel's global config signs through 1Password, which would wait here for an
        # approval no test can give.
        git("-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false",
            "commit", "-qm", "base")
        cls.base = git("rev-parse", "HEAD").stdout.strip()
        with open(os.path.join(cls.repo, "scripts", "loop.py"), "w") as f:
            f.write("moved by this ticket\n")
        git("add", "-A")
        git("-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false",
            "commit", "-qm", "the ticket")

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo, True)

    def moved(self, report, base=None):
        """run-checks.sh's own guidance_moved, run over that repository against one report."""
        harness = (f"set -euo pipefail\nROOT={shlex.quote(self.repo)}\n" + shell_function("guidance_moved")
                   + f"\nguidance_moved {shlex.quote(base or self.base)} {shlex.quote(report)}\n")
        run = subprocess.run(["bash", "-c", harness], cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        return run.stdout

    def test_only_the_documents_this_branch_moved_are_the_tickets_to_fix(self):
        stale = "STALE   {}: {} changed since 24101fc9 (now 379722e9)"
        for name, report, expected in [
            ("a document whose paths this branch changed", stale.format("the-system", "scripts/loop.py docs/api.md"), ["the-system"]),
            ("a glob of theirs", stale.format("infra", "Justfile scripts/**"), ["infra"]),
            ("a document this branch left alone", stale.format("the-web-app", "web/src"), []),
            ("a green report", "ok      the-system\nunbound key-goals (no source_paths)", []),
            ("nothing at all, as an unreachable API leaves behind", "", []),
            # The commits a STALE line lists are printed indented beneath it; read as documents they
            # would each be looked up as a slug and the paths of the real one lost among them.
            ("the commits listed under a STALE line",
             stale.format("the-system", "scripts/loop.py") + "\n          379722e9 TP-257: ask the API\n"
             + stale.format("the-web-app", "web/src"), ["the-system"]),
        ]:
            with self.subTest(name):
                out = self.moved(report)
                self.assertEqual([line.split(" ")[0] for line in out.splitlines()], expected)

    def test_the_failure_hands_over_the_diff_to_read_and_the_re_pin_to_run(self):
        # Without both the builder is left re-reading the whole document against the whole history,
        # and a correction that is never re-pinned leaves the next ticket failing on the same line.
        out = self.moved("STALE   the-system: scripts/loop.py changed since 24101fc9 (now 379722e9)")
        self.assertIn("git diff 24101fc9..HEAD -- scripts/loop.py", out)
        self.assertIn("./scripts/guidance-check.sh --accept-only the-system", out)
        self.assertNotIn("(now", out)  # the pin, not the rest of the line it was printed on

    def test_a_base_of_head_scans_nothing_so_a_caller_can_opt_out(self):
        # The seal TP-339 put under the Go suite: with the base at HEAD the range is no commits, so
        # every document is somebody else's business and the scan is empty whatever the report says.
        # A test that runs the real script wants the gate's two checks and not the checkout's
        # guidance state, and this is what lets it ask for that without stubbing the script out.
        head = subprocess.run(["git", "-C", self.repo, "rev-parse", "HEAD"],
                              check=True, capture_output=True, text=True).stdout.strip()
        # The same report either way, so only the base can account for the difference.
        report = "STALE   the-system: scripts/loop.py changed since 24101fc9 (now 379722e9)"
        self.assertIn("the-system", self.moved(report), "the branch's own drift stopped being its business")
        self.assertEqual(self.moved(report, base=head), "", "GATE_BASE=HEAD still scanned something")

    def test_the_hermetic_opt_out_the_go_suite_relies_on_stays_wired(self):
        # Two halves, each useless alone. run-checks.sh must keep taking an overridable base (a
        # hardcoded `main` silently un-seals the Go test), and evidence_test.go must keep passing one
        # (dropping it puts the suite back to going red for another ticket's drift, TP-326).
        # assertTrue, not assertIn: the haystacks are whole source files, and assertIn prints them.
        self.assertTrue("${GATE_BASE:-main}" in read("scripts/run-checks.sh"),
                        "run-checks.sh no longer lets a caller name the base to scan from")
        self.assertTrue("GATE_BASE=HEAD" in read("api/internal/service/evidence_test.go"),
                        "the gate test reads the checkout's guidance again; one ticket's drift fails the rest")

    def test_run_checks_fails_the_ticket_on_what_it_finds(self):
        # The wiring, as with the render check above: deciding well and then not counting it would
        # leave gate_passed true over a document the ticket left behind.
        body = read("scripts/run-checks.sh")
        self.assertIn("guidance_moved \"$(git -C \"$ROOT\" merge-base", body)
        self.assertIn('failed=$((failed + 1)); echo "FAIL  guidance', body)
        self.assertIn("./scripts/guidance-check.sh", body)


PGURL = os.environ.get("PGURL", "postgres://ticketing:ticketing@127.0.0.1:5434")
# What stands in for a connected project's own gate below: it writes down the directory it ran in,
# which is the whole question, and then exits with the code the case asked for.
FIXTURE_JUSTFILE = """# A connected project's gate, for scripts/test_gate.py (TP-346).
gate:
    pwd > gate-ran-here
    exit {code}
"""


def psql(db, *args):
    """One psql call, the way run-checks.sh makes them: same URL, same strict flags."""
    run = subprocess.run(["psql", f"{PGURL}/{db}", "-qtAX", "-v", "ON_ERROR_STOP=1", *args],
                         capture_output=True, text=True)
    assert run.returncode == 0, f"psql {args}: {run.stderr}"
    return run.stdout.strip()


@unittest.skipUnless(shutil.which("psql") and shutil.which("just"),
                     "the gate records through psql and this fixture runs the project's own `just`")
class ChecksRunInTheCheckoutOfTheTicketsOwnProject(unittest.TestCase):
    """A connected project's gate is its own code's gate, never this checkout's (TP-346).

    run-checks.sh lives in the ticketing checkout and used to `cd` here for every check it ran. Every
    planned ticket carries `just gate` (service/ownership.go's DefaultChecks), so for a ticket of a
    project connected to this tracker the gate that ran was *ticketing's* lint, tests and render
    check: a green run of this repository would have recorded that project's work as passed, and a
    red one would have failed it for a change in a repository it has never seen.

    So the fixture is a project whose `repo_path` is a scratch checkout with a Justfile of its own,
    whose `gate` writes down the directory it ran in. That marker is the assertion, and the timeout
    is the second half of it: a regression shows up as the marker missing rather than as minutes of
    this repository's real gate. The other direction — a project with no `repo_path`, whose checks do
    run here — is pinned by the Go suite, where this script runs for a TP ticket whose check is
    `test -f Justfile` (service/evidence_test.go), and is not paid for twice here.
    """

    @classmethod
    def setUpClass(cls):
        cls.db = f"ticketing_gate_{os.getpid()}"
        psql("postgres", "-c", f"CREATE DATABASE {cls.db}")
        cls.addClassCleanup(psql, "postgres", "-c", f"DROP DATABASE IF EXISTS {cls.db} WITH (FORCE)")
        # The real migrations rather than a hand-written fixture schema: the columns this script reads
        # are the ones the tracker has, and a second copy of them here would be the thing that rots.
        migrations = os.path.join(ROOT, "api", "migrations")
        for name in sorted(f for f in os.listdir(migrations) if f.endswith(".sql")):
            psql(cls.db, "-f", os.path.join(migrations, name))
        cls.checkout = cls.fixture_checkout(1)
        psql(cls.db, "-c", f"""
            INSERT INTO projects (key, name, repo_path) VALUES ('CONN', 'Connected', '{cls.checkout}');
            INSERT INTO tickets (project_id, number, key, title, type, status, source)
              SELECT id, 1, 'CONN-1', 'the first ticket worked on a connected project',
                     'bug', 'in_progress', 'ai' FROM projects WHERE key = 'CONN';
            INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command, expect_exit, visibility)
              SELECT id, 1, 'the standing gate', 'command', 'just gate', 0, 'public'
              FROM tickets WHERE key = 'CONN-1';""")

    @classmethod
    def fixture_checkout(cls, code):
        """A scratch checkout whose own `just gate` exits with `code`, as a connected project's does."""
        path = tempfile.mkdtemp()
        cls.addClassCleanup(shutil.rmtree, path, True)
        with open(os.path.join(path, "Justfile"), "w") as f:
            f.write(FIXTURE_JUSTFILE.format(code=code))
        return path

    def run_checks(self, **env):
        """The real script against the fixture database. REPO is dropped from the inherited
        environment unless a case asks for it: loop.sh exports it into every agent step, so a `just
        test` run from inside one would otherwise point these cases at that run's checkout."""
        return subprocess.run(
            [os.path.join(ROOT, "scripts", "run-checks.sh"), "CONN-1"], cwd=ROOT, timeout=180,
            capture_output=True, text=True,
            env={**{k: v for k, v in os.environ.items() if k != "REPO"},
                 "PGURL": PGURL, "DB": self.db, **env})

    def ran_in(self, checkout):
        """Where that checkout's own gate says it ran, or a failure naming what is missing."""
        marker = os.path.join(checkout, "gate-ran-here")
        self.assertTrue(os.path.exists(marker), f"nothing ran {checkout}'s own gate")
        with open(marker) as f:
            return os.path.realpath(f.read().strip())

    def gate_recorded(self):
        return psql(self.db, "-c", "SELECT gate_passed FROM tickets WHERE key = 'CONN-1'")

    def test_a_connected_projects_checks_run_there_and_its_red_gate_is_recorded(self):
        run = self.run_checks()
        out = run.stdout + run.stderr
        self.assertEqual(1, run.returncode, out)
        self.assertIn("FAIL  1. the standing gate — just gate exited 1, expected 0", out)
        self.assertIn(self.checkout, out, "the output never says which checkout the gate ran in")
        self.assertEqual(os.path.realpath(self.checkout), self.ran_in(self.checkout),
                         "the ticket's checks did not run in its own project's checkout")
        self.assertEqual("f", self.gate_recorded(), "a failing gate was not recorded as failed")

    def test_this_checkouts_guidance_is_not_another_projects_gate(self):
        # guidance-check.sh reads this corpus against this checkout's git history (run-checks.sh:90),
        # so run for CONN it would fail CONN's gate over a document describing code CONN has never
        # had — and the correction it asks for is in a repository CONN's builder is not working in.
        self.assertIn("SKIP  guidance", self.run_checks().stdout)

    def test_a_green_gate_in_the_checkout_the_caller_names_is_what_passes_the_ticket(self):
        # Both the other direction — a script that failed everything would pass the case above — and
        # the precedence loop.sh depends on: it exports REPO, so the gate it records as a step ends is
        # run in the checkout it gave that step's agent.
        passing = self.fixture_checkout(0)
        run = self.run_checks(REPO=passing)
        out = run.stdout + run.stderr
        self.assertEqual(0, run.returncode, out)
        self.assertIn("PASS  1. the standing gate", out)
        self.assertEqual("t", self.gate_recorded())
        self.assertEqual(os.path.realpath(passing), self.ran_in(passing),
                         "REPO lost to repo_path, so the loop's own call would run a gate somewhere "
                         "other than the checkout it just gave the agents")


if __name__ == "__main__":
    unittest.main()
