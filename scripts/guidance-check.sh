#!/usr/bin/env bash
# Compares each guidance document against the code it claims to describe.
#
# This cannot verify that a document is CORRECT — the API never sees the repository, and a document
# asserting `api/src/service.rs` is just a string. What it can do is notice that the ground has moved:
# if a commit that the reviewer cannot have seen has touched a document's source_paths, nobody has
# looked at the document since the code changed underneath it.
#
# That is exactly the failure that produced the three stale statements this feature was built for.
#
# "Cannot have seen" is an ancestry question — `git log <pin>..HEAD -- <paths>` — and asking it any
# other way is how this check spent two batches crying wolf. It used to compare the pin against
# `git log -1 -- <paths>`, the newest commit touching the paths, for string equality. git log
# simplifies history, so a merge commit that changed nothing against its first parent is never that
# answer: pin a document at a merge (which is exactly what happens when a document is reviewed at the
# tip of a finished ticket) and the check reports the paths as "changed" to a commit that is an
# ANCESTOR of the pin. Batch 5 and batch 6 both lost their eval to it (TP-162). An ancestry test has
# no opinion about which commit is "newest" and so cannot make that mistake.
#
# It also compares each served body against the docs/guidance file of the same name (TP-192). That is
# a second kind of drift, and the one this check used to be blind to: it looked at the database and at
# git history and never at the files, so the files could fall behind the served text unnoticed until
# somebody ran `just guidance --replace` and deleted the difference. At 08ab5ec the difference was
# three rules. A DRIFT line names both directions and fails, in every mode, because a file that is
# behind is a loaded gun whether or not anybody is reviewing anything today.
#
# Exit 1 when anything is stale, so CI fails. `--accept` instead records HEAD for every document
# without flagging anything: the "I have just reviewed these" button. `--accept-only <slug>...` is
# that button pressed for a named few — what the seed calls to baseline what it wrote — and
# everything else is still compared and still reported.
#
# `--changed-since <commit> <paths...>` is the comparison on its own, for scripts/test_guidance_check.py:
# it prints the offending commits, one per line, and exits 1 when there are any.
set -euo pipefail
API="${API:-http://localhost:8080}"
# GUIDANCE_CHECK_ROOT lets the test point the comparison at a scratch repository.
ROOT="${GUIDANCE_CHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
# Both readings of a guidance file live in one module, here and in guidance-seed.sh (TP-235).
LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/guidance_file.py"
MODE=${1:-}
# Stamping a document nobody re-read deletes the STALE flag the doc-review step keys off, and it is
# then never re-read. So --accept-only re-pins only the slugs named, leaving every other pin (TP-273).
ACCEPT_ONLY=
if [ "$MODE" = "--accept-only" ]; then shift; ACCEPT_ONLY=" $* "; fi

review() {
  curl -sf -X POST "$API/api/guidance/$1/review" \
    -H 'content-type: application/json' -H 'X-Actor: guidance-check' \
    ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} -d "$2" >/dev/null
}

# changed_since <commit> <paths...>: the commits reachable from HEAD but not from <commit> that touch
# these paths, newest first. Empty means the reviewer at <commit> had already seen everything there is
# to see about these paths, however the graph is shaped between there and here.
changed_since() {
  local commit=$1
  shift
  git -C "$ROOT" log --format='%H %s' "$commit..HEAD" -- "$@"
}

# file_vs_served <slug>: 1 and a DRIFT line when docs/guidance/<slug>.md and the served body carry
# different lines. Both counts are printed, but the one that matters is how many served lines the file
# lacks: those are what a `just guidance --replace` would delete.
#
# A served document with no file of that name is passed over in silence. Nothing here can put a file
# back over it, so there is nothing to warn about — and it is the ordinary case for a connected
# project, whose documents are copies served under their own project and were never files in this
# checkout.
file_vs_served() {
  local slug=$1 file="$ROOT/docs/guidance/$slug.md" served counts file_only served_only
  [ -f "$file" ] || return 0
  # || true: a 404 or an unreachable API is the caller's problem to report, not a reason for set -e to
  # kill the whole run in the middle of the corpus.
  served=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/guidance/$slug" || true)
  if [ -z "$served" ]; then
    echo "unreadable $slug: GET /api/guidance/$slug returned nothing, so its file was not compared"
    return 1
  fi
  counts=$(python3 "$LIB" drift "$file" <<<"$served")
  read -r file_only served_only <<<"$counts"
  [ "$file_only" = 0 ] && [ "$served_only" = 0 ] && return 0
  echo "DRIFT   $slug: file lacks $served_only served lines, has $file_only the server lacks"
  return 1
}

if [ "$MODE" = "--changed-since" ]; then
  [ "$#" -ge 3 ] || { echo "usage: guidance-check.sh --changed-since <commit> <path>..." >&2; exit 2; }
  since=$2
  shift 2
  out=$(changed_since "$since" "$@")
  [ -n "$out" ] || exit 0
  printf '%s\n' "$out"
  exit 1
fi

docs=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/guidance")
head=$(git -C "$ROOT" rev-parse HEAD)
stale=0

while IFS='|' read -r slug commit paths; do
  [ -z "$slug" ] && continue

  # Before anything about source_paths: whether the file and the served text still say the same
  # thing is true of every document, including the ones with no source_paths to be stale about.
  file_vs_served "$slug" || stale=1

  if [ -z "$paths" ]; then
    # A document with no source_paths is not coupled to anything, so there is nothing mechanical to
    # say about it. It still ages out through review_interval_days.
    echo "unbound $slug (no source_paths)"
    continue
  fi

  # Whether these paths have ANY history at all. Not a staleness test — that is changed_since below,
  # which is an ancestry question — just the difference between paths git knows and paths it does not.
  # shellcheck disable=SC2086 -- paths are deliberately word-split into git's pathspec list.
  anyhistory=$(git -C "$ROOT" log -1 --format=%H -- $paths)
  if [ -z "$anyhistory" ]; then
    # git says "no commits touch these paths" for two very different situations: the paths are not
    # in this repository at all, and the paths are sitting right there but have never been
    # committed. Only the first is the document's problem. Reporting the second as "no longer
    # exists" is how every document in this repo came to carry a banner telling agents that the
    # paths it names are gone — while they were in the working tree the agent had open.
    present=0
    for path in $paths; do
      if [ -e "$ROOT/$path" ] || compgen -G "$ROOT/$path" >/dev/null 2>&1; then
        present=1
        break
      fi
    done

    if [ "$present" = 0 ]; then
      echo "MISSING $slug: source_paths are not in this repository ($paths)"
      stale=1
      review "$slug" '{"stale":true,"stale_reason":"its source_paths are not in this repository"}'
    else
      # Present but never committed, so there is no commit to compare a review against and the
      # check has no opinion to give. Silence beats an opinion it cannot support: the STALE line is
      # prepended to the body of every document an agent reads, and one that is always on tells
      # them nothing about the document that is actually rotting.
      echo "unversioned $slug: $paths are here but have no commits yet"
      review "$slug" '{"stale":false}'
    fi
    continue
  fi

  if [ "$MODE" = "--accept" ] || [[ $ACCEPT_ONLY == *" $slug "* ]]; then
    # HEAD, not the newest commit touching the paths: the pin means "this is the tree the reviewer
    # read", and only HEAD says that. Pinning the older touching commit would leave every commit
    # between it and HEAD looking unreviewed the moment one of them touched the paths again.
    review "$slug" "$(printf '{"source_commit":"%s","stale":false,"accepted":true}' "$head")"
    echo "accept  $slug @ ${head:0:8}"
  elif [ -z "$commit" ]; then
    # Never baselined, which is a gap in operations rather than evidence about the document. It
    # fails CI so somebody runs --accept, but it does not set the document stale: the banner that
    # reaches the agent has to mean "the code moved under this", or it stops meaning anything.
    stale=1
    echo "unreviewed $slug: no baseline commit — run 'just guidance-check --accept' to set it to ${head:0:8}"
  elif ! git -C "$ROOT" cat-file -e "$commit^{commit}" 2>/dev/null; then
    # The pin names a commit this checkout does not have — a review recorded against a rebased or
    # deleted branch, or against a different repository. There is no ancestry to test, so say that
    # rather than guessing, and do not set the document stale: the text may be perfectly good.
    stale=1
    echo "unknown $slug: baseline ${commit:0:8} is not a commit in this repository — re-review it and run 'just guidance-check --accept'"
  else
    # shellcheck disable=SC2086 -- paths are deliberately word-split into git's pathspec list.
    newer=$(changed_since "$commit" $paths)
    if [ -z "$newer" ]; then
      review "$slug" '{"stale":false}'
      echo "ok      $slug"
    else
      stale=1
      newest=${newer%%$'\n'*}
      echo "STALE   $slug: $paths changed since ${commit:0:8} (now ${newest:0:8})"
      printf '%s\n' "$newer" | cut -c1-8,41- | sed 's/^/          /'
      review "$slug" "$(printf '{"stale":true,"stale_reason":"the code it describes changed at %s and nobody has re-read it since"}' "${newest:0:8}")"
    fi
  fi
done < <(python3 "$LIB" list <<<"$docs")

exit $stale
