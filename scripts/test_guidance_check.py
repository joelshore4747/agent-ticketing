"""The two guidance scripts held to their outward behaviour, by running them for real: --changed-since
ancestry (TP-162), the flags the recipes advertise surviving the trip through just (TP-225), the
payload guidance-seed.sh PUTs for every docs/guidance file (TP-234), and drift() with --replace's
refusal to delete served lines (TP-192). What the check prints per slug is in test_guidance_lister.py
(TP-235), which imports the stub and the runners from here. The stub binds a loopback socket on a
free port: no database, nothing that leaves the machine.
"""
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "guidance-check.sh")
SEED = os.path.join(ROOT, "scripts", "guidance-seed.sh")
GUIDANCE_DIR = os.path.join(ROOT, "docs", "guidance")

sys.path.insert(0, os.path.join(ROOT, "scripts"))
from lib.guidance_file import drift  # noqa: E402


class Repo:
    """A scratch git repository, committed into with no signing and no user config of the host's."""

    def __init__(self, path):
        self.path = path
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Test")
        self.git("config", "commit.gpgsign", "false")

    def git(self, *args):
        return subprocess.run(
            ["git", "-C", self.path, *args],
            check=True, capture_output=True, text=True,
        ).stdout.strip()

    def commit(self, path, text, message):
        full = os.path.join(self.path, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(text)
        self.git("add", path)
        self.git("commit", "-q", "-m", message)
        return self.git("rev-parse", "HEAD")

    def merge(self, branch, message):
        self.git("merge", "--no-ff", "-q", "-m", message, branch)
        return self.git("rev-parse", "HEAD")

    def changed_since(self, commit, *paths):
        """The script's comparison on its own: (exit code, its lines)."""
        done = subprocess.run(
            [SCRIPT, "--changed-since", commit, *paths],
            capture_output=True, text=True,
            env={**os.environ, "GUIDANCE_CHECK_ROOT": self.path},
        )
        return done.returncode, [ln for ln in done.stdout.splitlines() if ln.strip()]

    def newest_touching(self, *paths):
        """What the old comparison used, kept so the tests can show it giving the wrong answer."""
        return self.git("log", "-1", "--format=%H", "--", *paths)


class ScratchRepoTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Repo(self.tmp.name)


class APinOnAMergeCommit(ScratchRepoTest):
    """Reviewed at a merge, then an unrelated commit: the shape that cost two batches an eval."""

    def setUp(self):
        super().setUp()
        self.repo.commit("Justfile", "gate:\n    just lint\n", "initial")
        # The paths change on a ticket branch, merged with --no-ff; the reviewer reads the document
        # at the merge and pins it there; then life goes on where the document does not describe.
        self.touched = self.repo.commit("Justfile", "gate:\n    just lint\n    just test\n", "TP-1: gate runs tests")
        self.repo.git("checkout", "-q", "-b", "ticket/tp-2", "HEAD~1")
        self.repo.commit("api/service.go", "package service\n", "TP-2: unrelated")
        self.repo.git("checkout", "-q", "main")
        self.pin = self.repo.merge("ticket/tp-2", "Merge TP-2: unrelated")
        self.repo.commit("api/other.go", "package api\n", "TP-3: elsewhere")

    def test_the_old_comparison_would_have_called_it_stale(self):
        # git log skips the merge, so the "newest" commit here predates the pin.
        newest = self.repo.newest_touching("Justfile")
        self.assertEqual(newest, self.touched)
        self.assertNotEqual(newest, self.pin, "the pin would have been compared against an older commit")
        self.assertEqual(
            "0", self.repo.git("rev-list", "--count", f"{self.pin}..{newest}"),
            "and that older commit is an ancestor of the pin, so nothing has actually changed",
        )

    def test_a_document_pinned_at_a_merge_is_not_stale(self):
        code, lines = self.repo.changed_since(self.pin, "Justfile")
        self.assertEqual((0, []), (code, lines),
                         "a pin on a merge commit must not report its own history as newer than itself")


class RealChangeAfterThePin(ScratchRepoTest):
    """The check still has to fire, or the fix is just a way of never failing."""

    def test_a_commit_after_the_pin_is_reported(self):
        self.repo.commit("Justfile", "gate:\n", "initial")
        pin = self.repo.git("rev-parse", "HEAD")
        self.repo.commit("api/other.go", "package api\n", "unrelated")
        self.repo.commit("Justfile", "gate:\n    just test\n", "TP-9: the gate grew a step")
        code, lines = self.repo.changed_since(pin, "Justfile")
        self.assertEqual(1, code, "a real change to the paths must exit non-zero")
        self.assertEqual(1, len(lines), f"only the commit touching the paths should be listed: {lines}")
        self.assertIn("TP-9", lines[0])

    def test_a_change_on_a_merged_branch_is_reported_through_the_merge(self):
        # The change is not on main's first-parent line at all; ancestry still finds it.
        self.repo.commit("Justfile", "gate:\n", "initial")
        pin = self.repo.git("rev-parse", "HEAD")
        self.repo.git("checkout", "-q", "-b", "ticket/tp-10")
        self.repo.commit("Justfile", "gate:\n    just render-check\n", "TP-10: render guard in the gate")
        self.repo.git("checkout", "-q", "main")
        self.repo.merge("ticket/tp-10", "Merge TP-10: render guard in the gate")
        code, lines = self.repo.changed_since(pin, "Justfile")
        self.assertEqual(1, code)
        self.assertTrue(any("TP-10" in ln for ln in lines), lines)


class MultiplePaths(ScratchRepoTest):
    """source_paths is a list, and a directory in it means everything under it."""

    def test_only_the_named_paths_count(self):
        self.repo.commit("api/internal/service/plans.go", "package service\n", "initial")
        self.repo.commit("web/src/app.tsx", "export {}\n", "web")
        pin = self.repo.git("rev-parse", "HEAD")
        self.repo.commit("web/src/other.tsx", "export {}\n", "more web")
        self.assertEqual((0, []), self.repo.changed_since(pin, "api/internal/service"))
        code, lines = self.repo.changed_since(pin, "api/internal/service", "web/src")
        self.assertEqual(1, code)
        self.assertEqual(1, len(lines), lines)


@unittest.skipUnless(shutil.which("just"), "just is not installed")
class TheRecipesForwardTheirFlags(unittest.TestCase):
    """A flag a script advertises has to survive the trip through just (TP-225): neither recipe
    declared parameters, so just read the flag as a recipe name and refused it."""

    def dry_run(self, *args):
        """`just --dry-run <args>`: (exit code, both streams, so this is not a test of which one)."""
        done = subprocess.run(["just", "--dry-run", *args], cwd=ROOT, capture_output=True, text=True)
        return done.returncode, (done.stdout + done.stderr)

    def test_each_recipe_forwards_its_flag_and_still_runs_bare(self):
        # *ARGS is optional, so an empty {{ARGS}} still has to leave a runnable line behind.
        for recipe, args, expected in (
            ("guidance-check", ("--accept",), "guidance-check.sh --accept"),
            ("guidance", ("--replace",), "guidance-seed.sh --replace"),
            ("guidance-check", (), "guidance-check.sh"),
            ("guidance", (), "guidance-seed.sh"),
        ):
            with self.subTest(recipe=recipe, args=args):
                code, out = self.dry_run(recipe, *args)
                self.assertEqual(0, code, f"`just {recipe} {' '.join(args)}` is refused: {out.strip()}")
                self.assertIn(expected, out, "the recipe dropped the flag")

    def test_the_command_the_check_tells_the_operator_to_run_is_a_command_that_runs(self):
        # The bug in one line: the advice was unfollowable. Read the instruction out of the script,
        # so renaming the flag on one side fails loudly.
        with open(os.path.join(ROOT, "scripts", "guidance-check.sh"), encoding="utf-8") as f:
            advertised = re.findall(r"just guidance-check --[a-z-]+", f.read())
        self.assertTrue(advertised, "guidance-check.sh no longer names a just command to run")
        for instruction in sorted(set(advertised)):
            with self.subTest(instruction=instruction):
                code, out = self.dry_run(*instruction.split()[1:])
                self.assertEqual(0, code, f"the script prints {instruction!r}, which just refuses: {out.strip()}")


def guidance_files():
    """Every file the seed script's `"$DIR"/*.md` glob will loop over."""
    return sorted(glob.glob(os.path.join(GUIDANCE_DIR, "*.md")))


def slug_of(path):
    return os.path.basename(path)[: -len(".md")]


def expected_payload(path):
    """The PUT body this file has to produce (TP-234) — the contract, stated once, and read by
    indexing the closing fence where guidance-seed.sh splits on "---\\n": two implementations
    agreeing on ten real files is evidence, where one compared with itself is not."""
    with open(path, encoding="utf-8") as f:
        lines = f.read().split("\n")
    close = lines.index("---", 1)
    meta = {k.strip(): v.strip() for k, _, v in (ln.partition(":") for ln in lines[1:close])}
    return {"title": meta["title"], "kind": meta["kind"], "weight": int(meta.get("weight", 0)),
            "body": "\n".join(lines[close + 1:]).strip(),
            "source_paths": [p.strip() for p in meta.get("source_paths", "").split(",") if p.strip()]}


class StubGuidanceAPI:
    """GET /api/guidance (the `docs` the test named, or `existing` as bare slugs), GET
    /api/guidance/:slug (the served body, 404 — the real API's answer — when there is none), PUT
    (recorded, and it replaces the served body as the real one does) and POST /review (200, or the
    `curl -f` the seed script ends with would take the run down under `set -e`). `bodies` defaults to
    each file's own body, so a run comparing the two finds them in sync."""

    def __init__(self, existing=(), bodies=None, docs=None):
        self.docs = docs if docs is not None else [
            {"slug": s, "source_commit": None, "source_paths": []} for s in existing]
        self.bodies = {slug_of(p): expected_payload(p)["body"] for p in guidance_files()}
        self.bodies.update(bodies or {})
        self.puts = []      # (slug, headers, parsed body)
        self.reviews = []   # (slug, parsed body)
        stub = self

        class Handler(BaseHTTPRequestHandler):
            def _send(self, code, payload=b""):
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def _json(self, obj):
                self._send(200, json.dumps(obj).encode())

            def _read(self):
                return json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")

            def do_GET(self):
                if self.path == "/api/guidance":
                    return self._json(stub.docs)
                slug = self.path.rsplit("/", 1)[-1]
                if not re.fullmatch(r"/api/guidance/[^/]+", self.path) or slug not in stub.bodies:
                    return self._send(404)
                self._json({"document": {"slug": slug},
                            "revision": {"revision": 1, "body": stub.bodies[slug]}})

            def do_PUT(self):
                slug, payload = self.path.rsplit("/", 1)[-1], self._read()
                stub.puts.append((slug, self.headers, payload))
                stub.bodies[slug] = payload.get("body", "")
                if slug not in {d["slug"] for d in stub.docs}:
                    # The real API serves a document the moment it is created, which is what the
                    # baseline stamp at the end of a bootstrap run then loops over.
                    stub.docs.append({"slug": slug, "source_commit": None,
                                      "source_paths": payload.get("source_paths") or []})
                self._json({})

            def do_POST(self):
                stub.reviews.append((self.path.split("/")[-2], self._read()))
                self._json({})

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


def _run(argv, env, stub):
    try:
        return subprocess.run(argv, cwd=ROOT, capture_output=True, text=True,
                              env={**os.environ, "API": stub.url, **env}), stub
    finally:
        stub.close()


def run_seed(existing=(), *args, bodies=None, docs=None):
    """One real run of guidance-seed.sh against a fresh stub: (CompletedProcess, the stub)."""
    return _run([SEED, *args], {}, StubGuidanceAPI(existing, bodies, docs))


def run_check(root, docs, bodies=None, *args):
    """One real run of guidance-check.sh against a scratch checkout: (CompletedProcess, the stub)."""
    return _run([SCRIPT, *args], {"GUIDANCE_CHECK_ROOT": root}, StubGuidanceAPI(bodies=bodies, docs=docs))


def slugs_on_lines(output, word):
    """The slugs the script named on its `loaded`/`skip` lines."""
    return {ln.split()[1] for ln in output.splitlines() if ln.split()[:1] == [word]}


class WhatTheSeedPutsAndWhatItLeavesAlone(unittest.TestCase):
    """One PUT per file against an empty database, and a served document left alone until somebody
    asks for it — decided per slug, because a script that skipped everything whenever the database
    was non-empty would pass rows one and two and fail row three (TP-234). Asserted on the HTTP
    request, never on how the script got there, so the parser can be replaced underneath."""

    def test_what_is_put_skipped_and_loaded_follows_what_is_served_and_the_flag(self):
        every = [slug_of(p) for p in guidance_files()]
        present = every[: len(every) // 2]
        missing = [s for s in every if s not in present]
        for name, existing, flags, puts, skips in (
            ("nothing served yet", [], (), every, []),
            ("all present, no flag", every, (), [], every),
            ("some present, no flag", present, (), missing, present),
            ("all present, --replace", every, ("--replace",), every, []),
        ):
            with self.subTest(name):
                done, stub = run_seed(existing, *flags)
                self.assertEqual(0, done.returncode, done.stderr)
                sent = {slug: (headers, body) for slug, headers, body in stub.puts}
                self.assertEqual(sorted(puts), sorted(sent), "the wrong set of documents was written")
                self.assertEqual(set(skips), slugs_on_lines(done.stdout, "skip"))
                self.assertEqual(set(puts), slugs_on_lines(done.stdout, "loaded"))
                # One contract, not two: new or overwritten, a file is sent the same five fields —
                # weight an int not "0", the body without its frontmatter, [] for the one document
                # coupled to no code — as JSON, from the seed actor.
                for path in (p for p in guidance_files() if slug_of(p) in sent):
                    headers, body = sent[slug_of(path)]
                    self.assertEqual(expected_payload(path), body)
                    self.assertEqual("application/json", headers.get("Content-Type"))
                    self.assertEqual("guidance-seed", headers.get("X-Actor"))


# The banner the API prepends to a stale document on the way out (service.DeliverBody): one line,
# then a blank. It is in no file, and most of the corpus is stale most of the time.
BANNER_LINE = "> STALE: the code it describes changed at daeda38d and nobody has re-read it since.\n\n"
BODY = "- The first rule.\n\n- The second rule, which wraps\n  onto a second line.\n"


class WhatCountsAsADifferingLine(unittest.TestCase):
    """drift(file_body, served_body) -> (file_only, served_only), a bag of lines (TP-192). served_only
    is the number that matters: it is what a --replace would delete. If the banner counted nearly
    every document would read as drifted for ever, and a bag rather than a diff because moving a
    paragraph deletes nothing — a check that fired on that is one people learn to ignore."""

    def test_what_counts(self):
        for name, file_body, served_body, expected in (
            ("the same text both sides", BODY, BODY, (0, 0)),
            ("two lines only the server has", BODY, BODY + "\n- Third.\n- Fourth.\n", (0, 2)),
            ("one line only the file has", BODY + "\n- Third.\n", BODY, (1, 0)),
            ("both directions at once", BODY + "- mine.\n", BODY + "- theirs.\n- also theirs.\n", (1, 2)),
            ("the stale banner is not drift", BODY, BANNER_LINE + BODY, (0, 0)),
            ("the banner goes, a real difference stays", BODY, BANNER_LINE + BODY + "- Theirs.\n", (0, 1)),
            ("a leading banner only, not a quote in the body",
             BODY + "\n> STALE: what the check prints when a document rots.\n", BODY, (1, 0)),
            ("blank lines and indentation", BODY, BODY.replace("\n\n", "\n\n\n").replace("- The", "  - The"), (0, 0)),
            ("reordering a document", BODY, "\n".join(reversed(BODY.splitlines())), (0, 0)),
            ("no revision at all", BODY, "", (3, 0)),
        ):
            with self.subTest(name):
                self.assertEqual(expected, drift(file_body, served_body))


def guidance_file_text(slug, body, source_paths=("api/service.go",)):
    """A docs/guidance file as the parser reads it: fenced frontmatter, then the body."""
    return f"---\ntitle: {slug}\nkind: checklist\nsource_paths: {', '.join(source_paths)}\n---\n\n{body}"


def served(slug, commit, *paths):
    """One document as GET /api/guidance serves it, down to source_commit being null and not ''."""
    return {"slug": slug, "source_commit": commit, "source_paths": list(paths)}


class TheCheckReadsTheServedBodyOrSaysItCouldNot(unittest.TestCase):
    """A corpus that matches its files passes; one the check cannot read does not pass quietly."""

    def test_a_matching_corpus_passes_and_an_unreadable_one_is_reported(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = Repo(tmp)
            repo.commit("docs/guidance/insync.md", guidance_file_text("insync", BODY), "file insync")
            docs = [served("insync", repo.commit("api/service.go", "package service\n", "code"),
                           "api/service.go")]
            done, _ = run_check(tmp, docs, {"insync": BODY})
            self.assertEqual(0, done.returncode, done.stdout + done.stderr)
            self.assertNotIn("DRIFT", done.stdout)
            # The stub 404s a slug it has no body for. Silence there would be the same silence the
            # ticket is about: a comparison that did not happen, reported as one that passed.
            done, _ = run_check(tmp, docs, {})
            self.assertEqual(1, done.returncode, done.stdout)
            self.assertIn("unreadable insync", done.stdout)


class ReplaceRefusesToDeleteServedLines(unittest.TestCase):
    """`just guidance --replace` is the one door that overwrites a served document, and it is a
    whole-body PUT, so a file that is behind deletes the difference — which is how this batch's
    ticket was found. --replace counts first and refuses; --force is the deliberate override."""

    @classmethod
    def setUpClass(cls):
        cls.slugs = [slug_of(p) for p in guidance_files()]
        cls.behind, cls.ahead = cls.slugs[0], cls.slugs[1]
        body = {slug_of(p): expected_payload(p)["body"] for p in guidance_files()}
        # Two real documents: a served line no file has (a deletion, refused) and a file line the
        # server lacks (it deletes nothing, so it is not).
        cls.differing = {
            cls.behind: body[cls.behind] + "\n- A rule taught through the API and never written down.",
            cls.ahead: "\n".join(body[cls.ahead].splitlines()[:-2]),
        }
        # Every stale document arrives with the banner; refusing on it would make --replace unusable
        # for exactly the documents most likely to need updating.
        cls.bannered = {cls.behind: BANNER_LINE + body[cls.behind]}

    def test_replace_holds_back_only_the_file_that_would_delete_a_served_line(self):
        # Per file, not all or nothing: one document edited through the API must not stop the rest
        # of a deliberate replace.
        kept = [s for s in self.slugs if s != self.behind]
        for name, bodies, flags, code, put, said in (
            ("a served line at risk", self.differing, ("--replace",), 1, kept,
             ["refuse", self.behind, "1 served lines", "--force"]),
            ("--force overrides it", self.differing, ("--replace", "--force"), 0, self.slugs, []),
            ("only the stale banner differs", self.bannered, ("--replace",), 0, self.slugs, []),
            ("an unknown flag, before anything is put", None, ("--relpace",), 2, [], ["usage:"]),
        ):
            with self.subTest(name):
                done, stub = run_seed(self.slugs, *flags, bodies=bodies)
                sent = {slug: payload for slug, _, payload in stub.puts}
                self.assertEqual(code, done.returncode, done.stdout + done.stderr)
                self.assertEqual(sorted(put), sorted(sent), "the wrong set of documents was written")
                for text in said:
                    self.assertIn(text, done.stderr)
                if not said:
                    self.assertNotIn("refuse", done.stderr)
                # A file that is put at all is put its ordinary payload, forced or not.
                for path in (p for p in guidance_files() if slug_of(p) in sent):
                    self.assertEqual(expected_payload(path), sent[slug_of(path)])


class ReplaceRePinsOnlyWhatItPut(unittest.TestCase):
    """The seed script ends by stamping documents at HEAD, and it used to stamp EVERY one of them —
    including the ones the run never wrote and the ones it refused (TP-273). A pin means "this is
    the tree the reviewer read", so stamping a document nobody re-read launders its staleness: it
    deletes the STALE flag the loop's doc-review step keys off, and that document is then never
    re-read. Syncing one edited file with `just guidance --replace` did exactly that to the whole
    corpus. Asserted on the review POSTs, which is where the pin is actually set."""

    @classmethod
    def setUpClass(cls):
        cls.slugs = [slug_of(p) for p in guidance_files()]
        bodies = {slug_of(p): expected_payload(p)["body"] for p in guidance_files()}
        # A document with no source_paths is reported as unbound and never pinned either way, so it
        # is not part of what this is about.
        cls.bound = {slug_of(p) for p in guidance_files() if expected_payload(p)["source_paths"]}
        cls.behind = sorted(cls.bound)[0]
        # A file missing a served line: --replace refuses it, so by definition nobody re-read it.
        cls.differing = {cls.behind: bodies[cls.behind] + "\n- A rule taught through the API only."}
        head = subprocess.run(["git", "-C", ROOT, "rev-parse", "HEAD"],
                              check=True, capture_output=True, text=True).stdout.strip()
        # Served already pinned at HEAD, so nothing is stale for its own reasons and every pin the
        # run sets is one this script decided to set.
        cls.served_now = [served(slug_of(p), head, *expected_payload(p)["source_paths"])
                          for p in guidance_files()]

    @staticmethod
    def repinned(stub):
        return {slug for slug, body in stub.reviews if body.get("accepted")}

    def test_only_the_documents_this_run_put_are_re_pinned(self):
        for name, flags, bodies, code, expected in (
            ("a replace that writes every file re-pins every file", ("--replace",), None, 0, self.bound),
            ("the file --replace refused is not re-pinned", ("--replace",), self.differing, 1,
             self.bound - {self.behind}),
            ("a run that writes nothing re-pins nothing", (), None, 0, set()),
        ):
            with self.subTest(name):
                done, stub = run_seed(self.slugs, *flags, bodies=bodies, docs=list(self.served_now))
                self.assertEqual(code, done.returncode, done.stdout + done.stderr)
                self.assertEqual(expected, self.repinned(stub),
                                 "the wrong set of documents was stamped at HEAD")

    def test_a_bootstrap_still_stamps_everything_it_loads(self):
        # The baseline stamp is why the seed calls the check at all: against an empty database every
        # document is written, so every document is pinned and none reads as unreviewed.
        done, stub = run_seed([], docs=[])
        self.assertEqual(0, done.returncode, done.stdout + done.stderr)
        self.assertEqual(self.bound, self.repinned(stub))


class NothingRePinsTheWholeCorpus(unittest.TestCase):
    """Lesson TP-283, promoted by TP-287: a re-pin of every document at once launders the staleness of
    the ones nobody read. `ReplaceRePinsOnlyWhatItPut` above holds the seed script to re-pinning only
    what it wrote, and test_loop's doc-review prompt assertions hold the prompt to `--accept-only`.
    Neither stops a third caller being added, so this reads the scripts and agents themselves: nothing
    an agent or the loop runs may re-pin the corpus wholesale."""

    # A blanket re-pin, however it is spelled: `just guidance --replace` writes and stamps every
    # document, and a bare `--accept` stamps every document without writing any.
    BLANKET = (re.compile(r"guidance\s+--replace"), re.compile(r"guidance-check(?:\.sh)?\s+--accept(?!-only)"))
    # (path, what is on the line, why it is not a caller). Each excuses that line and nothing else.
    ALLOWED = (
        ("scripts/guidance-check.sh", "--accept",
         "the script that implements the flag, telling a human how to set a missing baseline"),
        ("scripts/loop.sh", "Do not run",
         "the doc-review prompt forbidding the command, which is the rule this test enforces"),
    )

    @staticmethod
    def callers(root):
        """Every line under scripts/ and .claude/ that runs a blanket re-pin, as "<path>:<line>: <text>".
        Tests are skipped (they name the command to check it) and so is a comment, which runs nothing."""
        found = []
        for base in ("scripts", ".claude"):
            for here, _, names in os.walk(os.path.join(root, base)):
                for name in sorted(names):
                    path = os.path.join(here, name)
                    rel = os.path.relpath(path, root)
                    if name.startswith("test_") or "__pycache__" in rel:
                        continue
                    try:
                        with open(path, encoding="utf-8", errors="replace") as f:
                            lines = f.read().splitlines()
                    except OSError:
                        continue
                    for n, line in enumerate(lines, 1):
                        blanket = NothingRePinsTheWholeCorpus
                        if line.lstrip().startswith("#") or not any(p.search(line) for p in blanket.BLANKET):
                            continue
                        if any(rel == f and needle in line for f, needle, _ in blanket.ALLOWED):
                            continue
                        found.append(f"{rel}:{n}: {line.strip()}")
        return found

    def test_no_script_or_agent_re_pins_every_document(self):
        offence = "./scripts/guidance-check.sh --accept   # re-pin the lot\n"
        for case, added, expected in (
            ("the repository as it stands", None, []),
            ("a shell script that re-pins the lot", ("scripts/scratch-sync.sh", offence),
             ["scripts/scratch-sync.sh:2"]),
            ("an agent told to run just guidance --replace",
             (".claude/agents/scratch.md", "Then run `just guidance --replace` to sync.\n"),
             [".claude/agents/scratch.md:2"]),
            ("the same command inside a test file, which is how a test names what it forbids",
             ("scripts/test_scratch.py", offence), []),
            ("the same command in a comment, which runs nothing", ("scripts/scratch.sh", "# " + offence), []),
        ):
            with self.subTest(case):
                root = ROOT
                if added:
                    root = tempfile.mkdtemp()
                    self.addCleanup(shutil.rmtree, root, True)
                    for base in ("scripts", ".claude"):
                        shutil.copytree(os.path.join(ROOT, base), os.path.join(root, base),
                                        ignore=shutil.ignore_patterns("__pycache__"))
                    path = os.path.join(root, added[0])
                    os.makedirs(os.path.dirname(path), exist_ok=True)
                    with open(path, "w", encoding="utf-8") as f:
                        f.write("#!/bin/sh\n" + added[1])
                found = self.callers(root)
                self.assertEqual(len(expected), len(found),
                                 "a blanket re-pin re-pins documents nobody read (lesson TP-283):\n"
                                 + "\n".join(found))
                for where in expected:  # the failure names the file and the line, so it can be fixed
                    self.assertTrue(any(f.startswith(where + ":") for f in found), found)


if __name__ == "__main__":
    unittest.main()
