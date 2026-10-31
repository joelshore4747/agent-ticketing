#!/usr/bin/env python3
"""Records a batch's before-review without an agent (lean harness).

    before_review.py <batch-id>      (API, LOOP_PROJECT, REPO and TICKETING_TOKEN from the environment)

Rule 24 will not let a reviewed batch be claimed until a before-review exists, and that review used to
be a whole agent session re-reading the memory. What it has to say before anything is built is
mechanical: which memory the batch starts from, and how far the code has moved since that memory's
commit. The reading is done once, by the review step, after the batch is built.
"""
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request

API = os.environ.get("API", "http://localhost:8080")
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"


def call(method, path, body=None):
    headers = {"Content-Type": "application/json", "X-Actor": "loop"}
    if os.environ.get("TICKETING_TOKEN"):
        headers["Authorization"] = "Bearer " + os.environ["TICKETING_TOKEN"]
    req = urllib.request.Request(API + path, method=method, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req) as r:
        return json.load(r)


def git(repo, *args):
    run = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True)
    return run.stdout.strip() if run.returncode == 0 else None


def body(memory, head, moved):
    """The review text: the memory it starts from, and the diff since that memory's commit."""
    review = (memory or {}).get("review")
    if not review:
        return (f"Recorded by the loop, no agent. There is no project memory yet, so this batch starts from the "
                f"repository at {head[:12]}. The review step before the eval reads the code and writes the first memory.")
    since = review["commit"]
    return (f"Recorded by the loop, no agent. Starts from the memory: the after-review of "
            f"\"{memory.get('batch_title') or 'the last batch'}\" at {since[:12]}. "
            f"Diff since then (git diff --shortstat {since[:12]}..{head[:12]}): {moved or 'nothing has changed'}. "
            f"The review step before the eval reads the memory and this batch's whole diff.")


def main(argv):
    if len(argv) != 2:
        sys.exit(__doc__)
    project, repo = os.environ.get("LOOP_PROJECT") or None, os.environ.get("REPO") or "."
    # A repository with no commits yet is described by git's empty tree: the API wants a sha, and that
    # is the honest one for "nothing has been written here".
    head = git(repo, "rev-parse", "HEAD") or EMPTY_TREE
    memory = call("GET", "/api/project-memory" + (f"?{urllib.parse.urlencode({'project': project})}" if project else ""))
    since = ((memory or {}).get("review") or {}).get("commit")
    moved = git(repo, "diff", "--shortstat", f"{since}..{head}") if since else None
    if since and moved is None:
        moved = f"unknown: commit {since[:12]} is not in this checkout"
    review = {"phase": "before", "commit": head, "body": body(memory, head, moved)}
    if project:
        review["project"] = project
    call("POST", f"/api/batches/{argv[1]}/reviews", review)


if __name__ == "__main__":
    main(sys.argv)
