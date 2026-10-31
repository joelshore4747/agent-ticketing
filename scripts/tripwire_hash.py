"""The one definition of a tripwire's body_sha256 (TP-134).

A tripwire pins a human's confirmation to a test's source: `RegisterTripwire` clears
`confirmed_by` whenever the hash it is sent differs from the one on the row. That only works if
everyone who ever registers the same test computes the same hash from the same bytes. Nothing
could enforce it — the API never touches the repository, so it cannot check a hash it is handed —
so the definition lives here, and `test_tripwire_hash.py` pins it against a committed fixture.

THE DEFINITION. `body_sha256` is the lowercase hex SHA-256 of a slice of the file `test_ref` names:

  path:TestName                 a Go test function: the lines from `func TestName(` through the
                                first following line that is exactly `}`, inclusive, each ending in
                                a single \n (so the slice always ends with a newline)
  path                          the whole file, exactly the bytes on disk
  path:<basename-no-extension>  the whole file too — naming the script itself means the script is
                                the test (web/scripts/render-check.mjs:render-check)
  path > describe > it          a vitest case: the `it`/`test` line through the line that closes it
                                at the same indentation, inclusive, same newline rule

Those first three were not invented here. They are what the seven tripwires registered before this
script was written actually used: six of the seven reproduce byte for byte, and the seventh is the
one whose irreproducibility filed this ticket.

TP-158 audited every registered row against this definition and closed that seventh out. It was
`api/internal/app/planning_test.go:TestAPlannedTicketIsParsedTheSameOverRESTAndMCP`, and its stored
hash reproduced from nothing: not the whole file, not any `func Test…` slice, at any commit that
file has ever had — the test it names did not exist until TP-117, three quarters of an hour after
the row was written. It has been re-registered with the hash this script gives, which cleared its
confirmation, so Needs you now asks a human to re-confirm it against a body anyone can recompute.
Every row registered since reproduces here. A row whose recomputed hash differs today therefore
means what it is supposed to mean — the guard's body changed and the confirmation on it is stale —
and not that two people hashed the same test two ways.

A name that matches none of the rules is an error that says what was looked for. The tempting
fallback — hash the whole file when the function cannot be found — is the worst option available,
because a misspelt test name would then produce a hash that looks fine, registers fine, and pins a
human's confirmation to a file rather than to the test they read.

These are line rules, not a parser, and deliberately: what a tripwire needs is one answer everybody
reaches, not a faithful AST. The one place the two differ is a Go test holding a raw string with a
line that is just `}` (gofmt does not indent inside a raw string), where the slice stops early. It
is still the same slice for everyone, so the confirmation still holds; it just covers less of the
test than you would think. Write a Go parser here only if that ever actually bites.
"""
import hashlib
import json
import os
import re
import sys

USAGE = "usage: tripwire_hash.py [--json] <test_ref>...  (path:TestName | path | 'path > describe > it')"


class Problem(Exception):
    """A test_ref that names nothing this file can slice. The message says what was looked for."""


def parse_ref(ref):
    """Split a test_ref into (path, name, kind). kind is 'go', 'file' or 'vitest'."""
    ref = ref.strip()
    if ">" in ref:
        parts = [p.strip() for p in ref.split(">")]
        if len(parts) != 3 or not all(parts):
            raise Problem(f"{ref}: a vitest ref is 'path > describe > it', three parts, none empty")
        return parts[0], (parts[1], parts[2]), "vitest"
    path, sep, name = ref.partition(":")
    if not sep:
        return path, None, "file"
    if not path or not name:
        raise Problem(f"{ref}: a ref is 'path:TestName', both parts non-empty")
    # Naming the script itself is how a whole-file check is written.
    if name == os.path.splitext(os.path.basename(path))[0]:
        return path, None, "file"
    return path, name, "go"


def _lines(path):
    try:
        with open(path, "rb") as f:
            raw = f.read()
    except OSError as e:
        raise Problem(f"{path}: {e.strerror}")
    return raw, raw.split(b"\n")


def _slice(lines, start, end):
    """The lines start..end inclusive, each terminated by one \n."""
    return b"".join(line + b"\n" for line in lines[start:end + 1])


def go_function(path, name):
    """`func TestName(` through the first following line that is exactly `}`."""
    _, lines = _lines(path)
    opener = re.compile(rb"^func " + re.escape(name.encode()) + rb"\b")
    for i, line in enumerate(lines):
        if opener.match(line):
            for j in range(i + 1, len(lines)):
                if lines[j] == b"}":
                    return _slice(lines, i, j)
            raise Problem(f"{path}: `func {name}` is never closed by a `}}` in column 1")
    raise Problem(f"{path}: no line matching `func {name}`")


def vitest_case(path, describe, it):
    """The `it`/`test` line under the right `describe`, through the line closing it at that indent."""
    _, lines = _lines(path)
    block = re.compile(rb"^(\s*)describe(?:\.\w+)?\s*\(\s*['\"`]" + re.escape(describe.encode()) + rb"['\"`]")
    case = re.compile(rb"^(\s*)(?:it|test)(?:\.\w+)?\s*\(\s*['\"`]" + re.escape(it.encode()) + rb"['\"`]")
    indent = None
    for i, line in enumerate(lines):
        if indent is None:
            m = block.match(line)
            if m:
                indent = m.group(1)
            continue
        if line.startswith(indent + b"}"):  # the describe closed without the case in it
            indent = None
            continue
        m = case.match(line)
        if not m or len(m.group(1)) <= len(indent):
            continue
        inner = m.group(1)
        for j in range(i + 1, len(lines)):
            # The line closing a call opened at this indent: `});`, `})`, `}),` …
            if lines[j].startswith(inner + b"}"):
                return _slice(lines, i, j)
        raise Problem(f"{path}: `it({it!r})` is never closed at its own indentation")
    raise Problem(f"{path}: no `it`/`test` called {it!r} inside `describe({describe!r})`")


def body(ref):
    """The exact bytes a test_ref names."""
    path, name, kind = parse_ref(ref)
    if kind == "file":
        return _lines(path)[0]
    if kind == "go":
        return go_function(path, name)
    return vitest_case(path, *name)


def body_sha256(ref):
    return hashlib.sha256(body(ref)).hexdigest()


def main(argv):
    as_json = "--json" in argv
    refs = [a for a in argv if a != "--json"]
    if not refs:
        print(USAGE, file=sys.stderr)
        return 2
    out, failed = [], 0
    for ref in refs:
        try:
            out.append({"test_ref": ref, "body_sha256": body_sha256(ref)})
        except Problem as e:
            print(f"tripwire-hash: {e}", file=sys.stderr)
            failed += 1
    if as_json:
        print(json.dumps(out if len(out) != 1 else out[0], indent=2))
    else:
        for row in out:
            print(f"{row['body_sha256']}  {row['test_ref']}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
