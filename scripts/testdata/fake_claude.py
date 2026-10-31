#!/usr/bin/env python3
"""A stand-in for `claude -p --agent <role>` in the loop test (TP-127).

It asks loop.py which step it is being run for, does that role's tracker action directly, and prints
the stream-json lines the real CLI ends with: rate_limit_events carrying made-up plan usage, then a
result line with made-up cost (TP-229). FAKE_FAIL=<agent> makes that role fail. FAKE_SEND_BACK=once makes the Approver send a plan back the first time and accept the
replan; FAKE_SEND_BACK=always makes it send every plan back, so the second send-back stops the loop.
"""
import json, os, subprocess, sys, urllib.request, urllib.error

API = os.environ["API"]
HERE = os.path.dirname(os.path.abspath(__file__))
REPORT = "## What was added\nx\n## Why\nx\n## What it achieved\nx\n## Where in the code\nx\n## How it moves the goals\nx\n"


def call(method, path, body=None, actor="fake"):
    headers = {"Content-Type": "application/json", "X-Actor": actor}
    if os.environ.get("TICKETING_TOKEN"):  # the credential loop.sh put in this step's environment (TP-316)
        headers["Authorization"] = "Bearer " + os.environ["TICKETING_TOKEN"]
    req = urllib.request.Request(API + path, data=json.dumps(body).encode() if body is not None else None,
                                 method=method, headers=headers)
    try:
        with urllib.request.urlopen(req) as r:
            return json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        sys.exit(f"fake {actor}: {method} {path} → {e.code} {e.read().decode()}")


agent = sys.argv[sys.argv.index("--agent") + 1]
if os.environ.get("FAKE_FAIL") == agent:
    sys.exit(f"fake {agent}: failing on purpose")
step = json.loads(subprocess.run([sys.executable, os.path.join(HERE, "..", "loop.py"), "next"], capture_output=True, text=True, check=True).stdout)
batch, ticket = step["batch"], step["ticket"]
gql = lambda q, v: call("POST", "/graphql", {"query": q, "variables": v}, actor="approver (delegated by joel)")


def eval_ticket(batch):
    ev = next(t for t in call("GET", f"/api/batches/{batch}")["tickets"] if t["type"] == "eval")
    return call("GET", f"/api/tickets/{ev['key']}")


if step["step"] == "strategist" and not batch:
    # A complete roadmap asks for the next one rather than stopping (TP-183). The real Strategist
    # then fills it with placeholder batches; the run ends either way, because it pinned this roadmap.
    call("POST", "/api/roadmaps", {"title": "the next roadmap", "spec": "proposed by the fake"}, actor="strategist")
elif step["step"] == "strategist":
    ev = eval_ticket(batch)
    current = next((d for d in ev.get("decisions", []) if d["id"] == ev.get("decision_id")), None)
    if current and current["kind"] == "reframed":
        # A replan, the way the real Strategist is told to finish one: record accepted on the eval
        # ticket, which hands the plan back to the Approver.
        call("POST", f"/api/tickets/{ev['key']}/decisions", {"kind": "accepted", "rationale": "fixed what the Approver asked",
                                                             "evidence_refs": [current["id"]]}, actor="strategist")
    else:
        t = call("POST", "/api/tickets", {"title": f"work for {batch[:8]}", "type": "chore", "goals": [3]}, actor="strategist")
        call("POST", f"/api/batches/{batch}/tickets", {"key": t["key"]}, actor="strategist")
elif step["step"] == "approver-accept":
    mode, ev = os.environ.get("FAKE_SEND_BACK"), eval_ticket(batch)
    sent_before = any(d["kind"] == "reframed" for d in ev.get("decisions", []))
    if mode == "always" or (mode == "once" and not sent_before):
        call("POST", f"/api/tickets/{ev['key']}/decisions", {"kind": "reframed", "rationale": "deletes nothing", "evidence_refs": ["goal 3"]}, actor="approver")
    else:
        gql('mutation($id: ID!) { acceptBatch(id: $id) { id } }', {"id": batch})
elif step["step"] == "review":
    call("POST", f"/api/batches/{batch}/reviews", {"phase": "after", "commit": "abc1234", "body": "fake review"}, actor="reviewer")
elif step["step"] in ("builder", "evaluator"):
    t = call("GET", f"/api/tickets/{ticket}")
    if t["status"] == "open":
        call("POST", "/api/claim", {"batch_id": batch}, actor=agent)
        t = call("GET", f"/api/tickets/{ticket}")
    call("PATCH", f"/api/tickets/{ticket}", {"version": t["version"], "status": "done", "resolution": "done by the fake",
                                             "verified_by": "fake: just test — ok"}, actor=agent)
elif step["step"] == "narrator":
    call("PATCH", f"/api/batches/{batch}", {"finish": True, "summary": REPORT}, actor="narrator")
elif step["step"] == "approver-checkpoint":
    gql('mutation($id: ID!) { checkpointBatch(id: $id, decision: continue, note: "fake") { id } }', {"id": batch})
else:
    sys.exit(f"fake: no role for step {step['step']}")

# The real CLI emits these unasked while a step runs, which is how the loop learns how much of the
# plan is left without paying for a probe. Two of them, rising, so the demo also proves that what gets
# recorded is the later one.
for five, seven in ((0.10, 0.20), (0.12, 0.24)):
    print(json.dumps({"type": "rate_limit_event", "rate_limit_info": {"unifiedWindows": {
        "five_hour": {"utilization": five, "resetsAt": 1790672400},
        "seven_day": {"utilization": seven, "resetsAt": 1791172800}}}}))
print(json.dumps({"type": "result", "total_cost_usd": 0.01, "duration_ms": 2000,
                  "usage": {"input_tokens": 100, "output_tokens": 50, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 900}}))
