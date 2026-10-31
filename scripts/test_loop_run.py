"""A whole loop run, and a second project connected and worked, for real (TP-272, TP-336).

scripts/test_loop.py pins what loop.py decides from a snapshot; this pins what actually happens when
scripts/loop.sh is turned loose on a roadmap: the batch budget pauses the run, the next run resumes
from the record alone, each ticket step's cost is posted under that step's own key, the plan's usage
is recorded after every step, a failed step and a second send-back stop the run, and a run pointed
at a second project works that project's checkout rather than the loop's own (goal 7). All of those
were pinned only by the orchestration demo scripts, deleted in TP-270.

ConnectASecondProject, at the bottom, pins what has to be true before such a run is possible at all:
a project created with a fixture checkout is listed, inherits the core guidance and none of this
repository's stack documents, answers loop.py's `where` and `next`, takes a finding with no areas
defined, and hands its own conventions to whoever claims its tickets (goal 7, TP-336).

It needs Docker, the Postgres `just db` starts, and Go — because the loop's
subject is the API's own state machine: a stub would be a second implementation of it, and the
orchestration bugs this catches all live in the gap between the two. The agents are
scripts/testdata/fake_claude.py, so no model is called and no real repository is touched: the API is
a throwaway database on a spare port, and the checkout the loop works is an empty scratch repo.
"""
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
from lib.guidance_file import parse  # noqa: E402  the one reader of docs/guidance/*.md

LOOP = os.path.join(ROOT, "scripts", "loop.sh")
FAKE = os.path.join(ROOT, "scripts", "testdata", "fake_claude.py")
GO = os.path.join(os.path.expanduser("~"), ".local", "go", "bin")
ORDER = ["strategist", "approver-accept", "review-before", "builder", "review", "evaluator",
         "narrator", "approver-checkpoint"]
# The steps loop.sh does itself, with no agent and so no step log, cost or ledger line (lean harness).
SCRIPTED = {"review-before"}
# What a newly created project is copied and what it is not (service/guidance.go's seedGuidanceSlugs):
# how work is decided and run is the same wherever the code is, how one repository's Go is written is
# not. Spelling the list out here rather than reading it from the API is the point — a slug quietly
# dropped from the seed would otherwise pass as correct.
CORE_GUIDANCE = ["key-goals", "planning-a-batch", "working-a-ticket", "the-agents"]
STACK_GUIDANCE = "go-service-layer"
api = None

# A CLAUDE_BIN that records the identity loop.sh gave one step and then runs the fake agent itself.
# Nothing else can see that identity: the MCP config is rewritten before the next step starts, and
# TICKETING_TOKEN lives only in that one child's environment (TP-316).
IDENTITY_SPY = '''#!/usr/bin/env python3
import json, os, subprocess, sys

agent = sys.argv[sys.argv.index("--agent") + 1]
with open(sys.argv[sys.argv.index("--mcp-config") + 1]) as f:
    given = {"mcp": json.load(f), "token": os.environ.get("TICKETING_TOKEN")}
with open(os.path.join(os.environ["IDENTITY_DIR"], agent + ".json"), "w") as f:
    json.dump(given, f)
sys.exit(subprocess.run([sys.executable, os.environ["FAKE"], *sys.argv[1:]]).returncode)
'''


def psql(sql):
    subprocess.run(["docker", "exec", "ticketing-db", "psql", "-qtAX", "-U", "ticketing", "-d", "postgres",
                    "-c", sql], check=True, capture_output=True)


def setUpModule():
    """A throwaway database and an API on a spare port, both gone when the module ends."""
    global api, _server, _db
    _db = f"ticketing_looprun_{os.getpid()}"
    psql(f"CREATE DATABASE {_db}")
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    api = f"http://127.0.0.1:{port}"
    _server = subprocess.Popen(
        ["go", "run", "."], cwd=os.path.join(ROOT, "api"), start_new_session=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        # GIT_COMMIT is this checkout's HEAD so that loop.sh sees the API already serving it: without
        # it the loop would decide the process is stale and run ./scripts/dev.sh api, which is the
        # real one on the real port. A test may never reach outside its own throwaway.
        # TICKETING_TOKENS is dropped: this throwaway database holds no credentials, so the API must
        # run in local mode however Joel has login set for the live one (TP-316).
        env={**{k: v for k, v in os.environ.items() if k != "TICKETING_TOKENS"},
             "PATH": GO + os.pathsep + os.environ["PATH"], "PORT": str(port),
             "GIT_COMMIT": subprocess.run(["git", "-C", ROOT, "rev-parse", "HEAD"], check=True,
                                          capture_output=True, text=True).stdout.strip(),
             "DATABASE_URL": f"postgres://ticketing:ticketing@127.0.0.1:5434/{_db}"})
    for _ in range(90):
        try:
            urllib.request.urlopen(api + "/healthz", timeout=2).read()
            return
        except Exception:
            if _server.poll() is not None:
                raise AssertionError("the throwaway API died:\n" + _server.communicate()[0])
            time.sleep(1)
    raise AssertionError("the throwaway API did not come up")


def tearDownModule():
    os.killpg(os.getpgid(_server.pid), signal.SIGTERM)
    _server.wait(timeout=30)
    _server.stdout.close()
    psql(f"DROP DATABASE IF EXISTS {_db} WITH (FORCE)")


def call(method, path, body=None, actor="test"):
    req = urllib.request.Request(api + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", "X-Actor": actor})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read() or b"null")


def graphql(query, variables=None):
    """The human's transport. A ticket opened here is `source: human`, so it does not wait in triage
    the way an AI-filed one does (rule 17) and can be claimed straight away."""
    out = call("POST", "/graphql", {"query": query, "variables": variables or {}})
    assert "errors" not in out, out["errors"]
    return out["data"]


def mcp(tool, arguments, actor="test"):
    """One MCP tools/call, handshake and all. The agents reach the tracker over this transport and
    report_findings has no REST neighbour (api/internal/rest/rest.go registers none), so a test of
    what a connected project can actually be worked with has to speak it."""
    session = {}

    def rpc(method, params, notification=False):
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = 1
        req = urllib.request.Request(
            api + "/mcp", method="POST", data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "X-Actor": actor,
                     "Accept": "application/json, text/event-stream", **session})
        with urllib.request.urlopen(req, timeout=60) as r:
            if given := r.headers.get("Mcp-Session-Id"):
                session["Mcp-Session-Id"] = given
            raw = r.read().decode()
        # The streamable transport answers inside an SSE frame when the client accepts one.
        return json.loads(raw.split("data: ", 1)[1]) if raw.startswith("event:") else json.loads(raw or "null")

    rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                       "clientInfo": {"name": "test", "version": "1"}})
    rpc("notifications/initialized", {}, notification=True)
    result = rpc("tools/call", {"name": tool, "arguments": arguments})["result"]
    text = result["content"][0]["text"]
    assert not result.get("isError"), text
    return json.loads(text)


def roadmap(title, batches, project=None):
    """A roadmap with `batches` placeholder batches planned into it, which is the state a project is
    in before its Strategist has ever run."""
    rm = call("POST", "/api/roadmaps", {"title": title, "spec": "a placeholder to plan into",
                                        **({"project": project} if project else {})})
    for n in range(1, batches + 1):
        call("POST", "/api/batches/plan",
             {"roadmap_id": rm["id"], "title": f"{title} {n}", "what": "w", "how": "h", "why": "y",
              "demo": "d", "tickets": [], **({"project": project} if project else {})})
    return rm["id"]


class LoopRun(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.repo = tmp.name
        subprocess.run(["git", "-C", self.repo, "init", "-q", "-b", "main"], check=True)
        self.usage_file = os.path.join(self.repo, "state", "usage-windows.json")
        self.ledger = os.path.join(self.repo, "state", "ledger.jsonl")
        # A gate of its own, because this is not the loop's own checkout and the loop therefore
        # records each ticket's gate itself (TP-346): the agent working here cannot reach
        # run-checks.sh, which lives in the loop's. `pwd` is this recipe saying which checkout it ran
        # in, and it passes, so a run is judged by this repository's gate and not by ticketing's.
        # Untracked on purpose: batch-merge.sh refuses a repository with uncommitted tracked changes.
        with open(os.path.join(self.repo, "Justfile"), "w") as f:
            f.write("# The gate of the project this run is working (scripts/test_loop_run.py).\n"
                    "gate:\n    pwd > gate-ran-here\n")

    def loop(self, *args, roadmap=None, repo="", **env):
        """One real run. Every LOOP_* and TICKETING_* of the caller's is dropped, so a run gets
        loop.sh's own defaults plus what this test asks for (TP-176), and never a real credential."""
        clean = {k: v for k, v in os.environ.items()
                 if not k.startswith("LOOP_") and not k.startswith("TICKETING_") and k != "REPO"}
        p = subprocess.run([LOOP, *args], capture_output=True, text=True, timeout=600, env={
            **clean, "PATH": GO + os.pathsep + os.environ["PATH"], "API": api, "CLAUDE_BIN": FAKE,
            "LOOP_USAGE_FILE": self.usage_file, "LOOP_LEDGER": self.ledger,
            # The scripts the loop shells out to reach Postgres directly, because gate_passed is on
            # no transport (rule 21): DB names this module's throwaway, or run-checks.sh would record
            # a verdict against whatever ticket wears that key in the live tracker.
            "DB": _db,
            **({} if repo is None else {"REPO": repo or self.repo}),
            **({"LOOP_ROADMAP": roadmap} if roadmap else {}), **env})
        steps = [l.split("] ", 1)[1].split(" ", 1)[0] for l in p.stdout.splitlines() if l.startswith("[")]
        return p, steps

    def test_a_run_pauses_at_its_budget_the_next_resumes_and_every_ticket_step_is_costed(self):
        rm = roadmap("Paced", 2)
        first, steps = self.loop(roadmap=rm, LOOP_BATCHES="1")
        out = first.stdout + first.stderr
        self.assertEqual(0, first.returncode, out)
        self.assertEqual(ORDER, steps[:8], out)
        self.assertIn("paused: 1 batches finished", out)
        states = [b["state"] for b in call("GET", f"/api/roadmaps/{rm}")["batches"]]
        self.assertEqual("finished", states[0], states)
        self.assertNotEqual("finished", states[1], "the budget stops the run before the second batch")

        second, steps = self.loop(roadmap=rm, LOOP_BATCHES="5")
        self.assertEqual(0, second.returncode, second.stdout + second.stderr)
        self.assertEqual("strategist", steps[0], "the next run picks up from the record, not from a handover")
        self.assertIn("roadmap is complete", (second.stdout + second.stderr).strip().splitlines()[-1])

        # Goal 9: one post per ticket step, under that step's own key, from the loop itself — so a
        # session nobody instrumented is still measured (TP-182). The other steps are logged only.
        run_dir = out.split("Logs: ")[1].splitlines()[0]
        with open(os.path.join(run_dir, "usage.log")) as f:
            logged = [line.split(" ", 4)[1:4:2] for line in f]
        batch = call("GET", f"/api/roadmaps/{rm}")["batches"][0]["batch"]
        keys = [t["key"] for t in call("GET", f"/api/batches/{batch['id']}")["tickets"]]
        self.assertEqual([["builder", keys[0]], ["evaluator", keys[1]]],
                         [s for s in logged if s[1] != "-"], logged)
        agents = [step for step in ORDER if step not in SCRIPTED]
        self.assertEqual(len(agents), len(logged), "every agent step is logged, whether or not it has a ticket")
        # The ledger outlives the run's temp directory and holds every step, model included, because a
        # claim keeps only the last step posted to its ticket (lean harness step 1).
        with open(self.ledger) as f:
            ledger = [json.loads(line) for line in f]
        self.assertEqual(agents, [r["step"] for r in ledger][:len(agents)])
        self.assertTrue(all(r["model"] and r["project"] for r in ledger), ledger[0])
        cost = call("GET", f"/api/batches/{batch['id']}")["batch"]["digest"].split("## Cost\n\n", 1)[1]
        self.assertIn("from 2 of 2 claims", cost.splitlines()[0],
                      "and the figures reach the digest, not just the log")

        # Goal 9 for a project worked somewhere else (TP-346): the loop records the gate of each
        # ticket it finished, because run-checks.sh lives in the loop's checkout and an agent is
        # denied any path outside the one it was given. The marker is this checkout's own gate saying
        # where it ran: without it the figure could have come from running ticketing's gate instead.
        self.assertTrue(call("GET", f"/api/tickets/{keys[0]}")["gate_passed"],
                        f"{keys[0]} was finished with no gate recorded")
        self.assertIsNone(call("GET", f"/api/tickets/{keys[1]}")["gate_passed"],
                          "an eval ticket is planned with no command checks (service/plans.go), its "
                          "gate being the commands in its description: there is nothing here to record")
        with open(os.path.join(self.repo, "gate-ran-here")) as f:
            self.assertEqual(os.path.realpath(self.repo), os.path.realpath(f.read().strip()),
                             "the gate ran somewhere other than the checkout the agents worked in")

        # Goal 8: the spending guard's input, recorded by the run itself after every step (TP-229),
        # so the next run and a dry run both see it. The figures are the fake's later rate-limit
        # event, not its earlier one, and seen_at is stamped now.
        with open(self.usage_file) as f:
            windows = json.load(f)
        self.assertEqual({"utilization": 0.12, "resets_at": 1790672400}, windows.get("five_hour"), windows)
        self.assertEqual({"utilization": 0.24, "resets_at": 1791172800}, windows.get("seven_day"), windows)
        self.assertLess(abs(time.time() - windows.get("seen_at", 0)), 600, windows)

    def test_a_run_stops_on_the_conditions_loop_sh_is_the_only_reader_of(self):
        """loop.py decides these from a snapshot; what is pinned here is that a real run acts on the
        verdict — the exit code it leaves and the reason it prints — which nothing else exercises."""
        for name, env, want_code, want_last, want_steps in [
            ("a failed step stops the run with its reason, not a retry",
             {"FAKE_FAIL": "backend-builder"}, 1, "backend-builder step failed", None),
            ("a second send-back of the same plan stops the run for Joel",
             {"FAKE_SEND_BACK": "always"}, 0, "back twice",
             ["strategist", "approver-accept", "strategist", "approver-accept", "stop"]),
        ]:
            with self.subTest(name):
                run, steps = self.loop(roadmap=roadmap(name[:40], 1), **env)
                out = (run.stdout + run.stderr).strip()
                self.assertEqual(want_code, run.returncode, out)
                self.assertIn(want_last, out.splitlines()[-1])
                if want_steps is not None:
                    self.assertEqual(want_steps, steps, out)

    def test_every_step_runs_under_its_own_identity(self):
        """TP-316: the MCP config is written per step, X-Actor naming the role, Authorization carrying
        that role's credential when it has one. Until the API reads either (TP-317) this is the only
        place it can be seen, and it is what keeps the agents working that batch attributed."""
        work = tempfile.mkdtemp()
        self.addCleanup(subprocess.run, ["rm", "-rf", work])
        spy = os.path.join(work, "spy.py")
        with open(spy, "w") as f:
            f.write(IDENTITY_SPY)
        os.chmod(spy, 0o755)

        def run(name, **tokens):
            """One short run — every plan sent back, so the Strategist and the Approver each get a
            step and nothing else does — with a credential file for each role named."""
            seen, token_dir = os.path.join(work, name), os.path.join(work, name + "-tokens")
            os.makedirs(seen)
            os.makedirs(token_dir)
            for role, token in tokens.items():
                with open(os.path.join(token_dir, role), "w") as f:
                    f.write(token + "\n")
            got, _ = self.loop(roadmap=roadmap("Identity " + name, 1), CLAUDE_BIN=spy, FAKE=FAKE,
                               IDENTITY_DIR=seen, TICKETING_TOKEN_DIR=token_dir, FAKE_SEND_BACK="always")
            self.assertEqual(0, got.returncode, got.stdout + got.stderr)
            given = {}
            for role in os.listdir(seen):
                with open(os.path.join(seen, role)) as f:
                    given[role[: -len(".json")]] = json.load(f)
            return given

        issued = run("issued", approver="approver-only-token")
        ticketing = issued["approver"]["mcp"]["mcpServers"]["ticketing"]
        self.assertEqual(api + "/mcp", ticketing["url"], "the server it was always pointed at")
        self.assertEqual({"X-Actor": "approver", "Authorization": "Bearer approver-only-token"},
                         ticketing["headers"])
        self.assertEqual("approver-only-token", issued["approver"]["token"],
                         "and in the step's own environment, so an agent's own curl sends it too")
        # The Strategist ran in the same loop against the same directory: a role is handed its own
        # credential or none, never the one next to it.
        self.assertEqual({"X-Actor": "strategist"},
                         issued["strategist"]["mcp"]["mcpServers"]["ticketing"]["headers"])
        self.assertEqual("", issued["strategist"]["token"])

        local = run("local")
        self.assertEqual({"X-Actor": "approver"},
                         local["approver"]["mcp"]["mcpServers"]["ticketing"]["headers"],
                         "no credential issued anywhere is local mode: X-Actor and nothing else")

    def test_a_second_project_is_worked_in_its_own_checkout(self):
        # Goal 7: the same machinery on another repository. A dry run is enough — what is pinned is
        # which step the loop names for a freshly connected project and where it would run it.
        checkout = tempfile.mkdtemp()
        self.addCleanup(subprocess.run, ["rm", "-rf", checkout])
        subprocess.run(["git", "-C", checkout, "init", "-q", "-b", "main"], check=True)
        call("POST", "/api/projects", {"key": "DEMO", "name": "Demo", "repo_path": checkout})
        rm = roadmap("DEMO's roadmap", 1, project="DEMO")
        # REPO is not passed: the point is that the loop reads the project's own repo_path.
        run, steps = self.loop("--dry-run", "--project", "DEMO", roadmap=rm, repo=None)
        out = run.stdout + run.stderr
        self.assertEqual(0, run.returncode, out)
        self.assertEqual(["strategist"], steps, out)
        self.assertIn("DEMO's roadmap 1", out, "the step names DEMO's own batch")
        self.assertIn(f"loop: project DEMO, roadmap {rm}, in {checkout}", out)


class ConnectASecondProject(unittest.TestCase):
    """Connecting a project, end to end on a fixture checkout (TP-336, goal 7).

    Every part of this already existed — loop.py's `--project` and `repo_path`, CreateProject copying
    the core guidance and the starter tags — and no test put them together for a project that is not
    TP, so the procedure was only ever known to work by the one project that was there from the
    first migration. What is pinned here is the whole arrival: the project row with its checkout, the
    guidance it inherits and the guidance it does not, loop.py settling where to work and what to do
    next, a finding landing in a project whose human has defined no areas yet, and a claim handing an
    agent that project's conventions. The case above pins one step of a real loop run on top of this.
    """

    @classmethod
    def setUpClass(cls):
        # 1. The fixture checkout: a real repository with one commit. loop.py asks git whether a
        # batch branch has reached main, and a repository with no HEAD has nothing to answer from.
        cls.checkout = tempfile.mkdtemp()

        def git(*args, commit=False):
            # Signing is off: Joel's global config signs through 1Password, which would wait here for
            # an approval no test can give.
            identity = ["-c", "user.email=t@example.com", "-c", "user.name=t",
                        "-c", "commit.gpgsign=false"] if commit else []
            subprocess.run(["git", "-C", cls.checkout, *identity, *args], check=True, capture_output=True)

        git("init", "-q", "-b", "main")
        with open(os.path.join(cls.checkout, "README.md"), "w") as f:
            f.write("a connected project\n")
        git("add", "-A")
        git("commit", "-qm", "first", commit=True)

        # This throwaway database has run the migrations and nothing else: `just guidance` loads the
        # corpus against a running API, so TP's documents are not in it and a copy from TP would be
        # vacuously empty. One stack document goes in beside the core four, so what the seed leaves
        # behind is observably a filter rather than an empty shelf.
        #
        # The real docs/guidance bodies, not stub text, because this API is shared with LoopRun above
        # and its run shells out to guidance-check, which compares every served body against the file
        # of the same name and fails on any difference (TP-192). Stub text is DRIFT on all five slugs
        # and stops that run. source_paths are dropped, exactly as seedGuidance drops them
        # (service/guidance.go): they name files in THIS checkout, and both that run and a connected
        # project resolve them against another, where each would read as MISSING. Dropped, every
        # document is `unbound`, which the loop counts as quiet (loop.py's GUIDANCE_QUIET).
        for slug in CORE_GUIDANCE + [STACK_GUIDANCE]:
            payload = parse(os.path.join(ROOT, "docs", "guidance", f"{slug}.md"))
            call("PUT", f"/api/guidance/{slug}",
                 {k: v for k, v in payload.items() if k != "source_paths"})

        # 2 and 3: the project, named with the checkout its code lives in, and the roadmap a
        # connected project starts with. This is the whole of what connecting one takes.
        cls.project = call("POST", "/api/projects",
                           {"key": "CONN", "name": "Connected", "repo_path": cls.checkout})
        cls.roadmap = roadmap("CONN's roadmap", 1, project="CONN")

    @classmethod
    def tearDownClass(cls):
        subprocess.run(["rm", "-rf", cls.checkout], check=False)

    def loop_py(self, *args):
        """loop.py as loop.sh runs it, with no REPO and no credential of the caller's: the checkout it
        reports can only have come from the project's own row, and the throwaway is in local mode."""
        run = subprocess.run(
            [sys.executable, os.path.join(ROOT, "scripts", "loop.py"), *args],
            capture_output=True, text=True, timeout=120,
            env={**{k: v for k, v in os.environ.items()
                    if k != "REPO" and not k.startswith("TICKETING_") and not k.startswith("LOOP_")},
                 "API": api, "PATH": GO + os.pathsep + os.environ["PATH"]})
        # A Problem exits non-zero with `loop: ...` on stderr and prints no JSON at all, so this is
        # also the assertion that the two questions were answerable for a project that is not TP.
        self.assertEqual(0, run.returncode, run.stdout + run.stderr)
        return json.loads(run.stdout)

    def test_the_project_is_listed_with_the_checkout_it_was_connected_with(self):
        # 2. repo_path is the only thing that says where this project's code is: without it on the
        # row, a run that names the project has nowhere to work (loop.py's pick_repo).
        listed = {p["key"]: p for p in call("GET", "/api/projects")}
        self.assertIn("CONN", listed, f"the new project is not listed: {sorted(listed)}")
        self.assertEqual(self.checkout, listed["CONN"]["repo_path"])

    def test_loop_py_settles_where_to_work_and_names_a_step(self):
        # 4. The two questions every run answers before it does anything, asked for CONN.
        self.assertEqual({"project": "CONN", "roadmap": self.roadmap, "repo": self.checkout},
                         self.loop_py("where", "--project", "CONN"))
        # And the step is work on CONN's own batch, not a stall and not TP's: a project whose
        # placeholder batch has no tickets is waiting for its Strategist to plan into it.
        got = self.loop_py("next", "--project", "CONN")
        self.assertEqual("strategist", got["step"], got)
        self.assertIn("CONN's roadmap 1", got["reason"], got)

    def test_a_finding_lands_in_a_project_with_no_areas(self):
        # 5. A scout can file against a project before its human has written a single area: routing a
        # finding by its file paths is the step that would have needed one.
        filed = mcp("report_findings", {"project": "CONN", "findings": [{
            "title": "the first finding anyone filed against this project",
            "description": "filed by a scout that has never seen this repository before",
            "type": "chore", "goals": [7],
            "locations": [{"file_path": "README.md"}]}]})
        self.assertEqual([], call("GET", "/api/areas?project=CONN"),
                         "a connected project starts with no areas; that is the state being tested")
        self.assertEqual(["triage"], [f["status"] for f in filed],
                         "it waits for the human, because no area of CONN's auto-accepts AI (rule 17)")
        # CONN-2, not CONN-1: plan_batch gave the placeholder batch an eval ticket of its own, and
        # that is this project's first. What the key proves is that the run of numbers is CONN's own
        # and started at 1 — a finding filed into a connected project is not numbered in TP's.
        self.assertEqual(["CONN-2"], [f["key"] for f in filed], filed)
        self.assertEqual("eval", call("GET", "/api/tickets/CONN-1")["type"],
                         "CONN-1 should be the placeholder batch's eval ticket")

    def test_a_human_opened_ticket_claims_with_that_projects_guidance(self):
        # 7. The envelope is the point of connecting a project at all: an agent that claims CONN's
        # work is handed CONN's conventions. Nothing else here would notice if it were handed TP's.
        opened = graphql("""mutation($project: String!) {
              createTicket(project: $project, input: {
                title: "the first job on the newly connected project", type: chore, goals: [7]
              }) { key status }
            }""", {"project": "CONN"})["createTicket"]
        self.assertEqual("open", opened["status"], "a human's own ticket does not wait in triage")

        envelope = mcp("claim_next_ticket", {"project": "CONN"})
        self.assertEqual(opened["key"], envelope["key"], envelope)
        guidance = envelope["guidance"]
        # `omitted` is absent rather than empty when the whole corpus fitted the token budget, which
        # is the usual case for a project holding only the four core documents.
        served = [doc["slug"] for doc in guidance["docs"] + (guidance.get("omitted") or [])]
        self.assertEqual(sorted(CORE_GUIDANCE), sorted(served),
                         "the claim carried the wrong project's corpus")

    def test_the_new_project_is_given_the_core_guidance_and_none_of_the_stack(self):
        # 6. seedGuidanceSlugs: how work is decided and run travels to another repository, how this
        # one's Go is written does not — go-service-layer names api/internal files that a different
        # checkout does not have, and guidance-check would report every one of them MISSING.
        slugs = [doc["slug"] for doc in call("GET", "/api/guidance?project=CONN")]
        self.assertEqual(sorted(CORE_GUIDANCE), sorted(slugs), slugs)
        self.assertIn(STACK_GUIDANCE, [doc["slug"] for doc in call("GET", "/api/guidance")],
                      "TP still has to hold the stack document, or its absence above proves nothing")


if __name__ == "__main__":
    unittest.main()
