#!/usr/bin/env bash
# The body_sha256 to register a tripwire with (TP-134). The definition itself is in tripwire_hash.py.
#
#   ./scripts/tripwire-hash.sh api/internal/service/service_test.go:TestClaimNextTakes…
#   ./scripts/tripwire-hash.sh --json <ref> [<ref>…]    ready to POST to /api/tripwires
#   ./scripts/tripwire-hash.sh web/scripts/render-check.mjs:render-check    (the whole script)
#   ./scripts/tripwire-hash.sh 'web/src/test/board.test.tsx > the board > groups by status'
#
# Paths are relative to the repository root, whatever directory you run this from — a tripwire's
# test_ref is stored as written, and one registered as ../api/x_test.go would match nothing.
#
# Why a script at all: RegisterTripwire drops the human's confirmation whenever the hash it is sent
# differs from the row's, and the API cannot check the hash it is handed (it never touches the
# repository). Two agents hashing the same test slightly differently would therefore silently throw
# away a confirmation a human gave. One command, so there is one answer.
set -euo pipefail
cd "$(dirname "$0")/.."
exec python3 scripts/tripwire_hash.py "$@"
