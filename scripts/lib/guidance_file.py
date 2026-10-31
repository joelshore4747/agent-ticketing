#!/usr/bin/env python3
"""One reader of docs/guidance/*.md and of the guidance the API serves, for both guidance scripts
and for whoever is third (goal 3).

    guidance_file.py payload FILE   the JSON body PUT /api/guidance/:slug takes
    guidance_file.py list           `slug|commit|paths` a line, from GET /api/guidance JSON on stdin
    guidance_file.py drift FILE     `file_only served_only` line counts, from GET /api/guidance/:slug
                                    JSON on stdin
"""
import json
import sys
from collections import Counter

# What the API prepends to a stale document's body on the way out (service.DeliverBody). It is in no
# file and never should be, so drift() drops it rather than counting it.
BANNER = "> STALE:"


def parse(path):
    """The PUT payload a guidance file describes: `key: value` frontmatter, then the body.

    weight is an int because the API's column is one and the string "0" 400s there; source_paths
    defaults to [] for the one document coupled to no code.
    """
    _, frontmatter, body = open(path, encoding="utf-8").read().split("---\n", 2)
    meta = {k.strip(): v.strip() for k, sep, v in
            (line.partition(":") for line in frontmatter.splitlines()) if sep}
    return {"title": meta["title"], "kind": meta["kind"], "weight": int(meta.get("weight", 0)),
            "body": body.strip(),
            "source_paths": [p.strip() for p in meta.get("source_paths", "").split(",") if p.strip()]}


def lines(docs):
    """One `slug|commit|paths` line per served document, for the shell loop in guidance-check.sh.

    A pipe, not a tab: bash collapses runs of IFS whitespace, so a document with no source_commit
    would shift its paths into the commit field and read as unbound.
    """
    return [f"{d['slug']}|{d['source_commit'] or ''}|{' '.join(d['source_paths'])}" for d in docs]


def drift(file_body, served_body):
    """(file_only, served_only): how many content lines each body has that the other lacks (TP-192).

    Lines, counted as a bag, because that is what a whole-body PUT puts at risk: `just guidance
    --replace` sends the file's body in place of the served one, so every line the server has and the
    file lacks is deleted, and at 08ab5ec that was three rules nobody would have missed until they
    were gone. Reordering a document is not drift and neither is reindenting one — a line is compared
    stripped, and blank lines are not compared at all.

    served_only is the number that matters: it is what a replace would delete.
    """
    file_lines = _content(file_body)
    served_lines = _content(_undelivered(served_body))
    return sum((file_lines - served_lines).values()), sum((served_lines - file_lines).values())


def _content(body):
    return Counter(stripped for stripped in (line.strip() for line in body.splitlines()) if stripped)


def _undelivered(served_body):
    """The served body as the database holds it: without the STALE banner delivery adds.

    Most of the corpus is stale most of the time, so counting the banner would report almost every
    document as drifted, and a check that is always red is a check nobody reads.
    """
    lines = served_body.lstrip().splitlines()
    return "\n".join(lines[1:]) if lines and lines[0].startswith(BANNER) else served_body


if __name__ == "__main__":
    if sys.argv[1:2] == ["payload"]:
        print(json.dumps(parse(sys.argv[2])))
    elif sys.argv[1:2] == ["list"]:
        print("\n".join(lines(json.load(sys.stdin))))
    elif sys.argv[1:2] == ["drift"]:
        # GET /api/guidance/:slug is {document, revision}, and revision is null for a document whose
        # every revision has been withdrawn: no served text, so the file can only add.
        served = json.load(sys.stdin).get("revision") or {}
        print("%d %d" % drift(parse(sys.argv[2])["body"], served.get("body") or ""))
    else:
        sys.exit("usage: guidance_file.py payload FILE | guidance_file.py list < guidance.json"
                 " | guidance_file.py drift FILE < document.json")
