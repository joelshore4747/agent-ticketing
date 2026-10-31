#!/usr/bin/env bash
# docs/api.md is the GraphQL contract; this copies its SDL block to where gqlgen reads it, so the
# doc and the served schema cannot drift. A test in internal/app checks the two still match.
set -euo pipefail
cd "$(dirname "$0")/.."

python3 - <<'PY'
import re
doc = open('docs/api.md').read()
block = re.search(r'## GraphQL schema.*?```graphql\n(.*?)```', doc, re.S)
if not block:
    raise SystemExit('docs/api.md has no GraphQL SDL block')
sdl = block.group(1).replace('scalar DateTime   # RFC 3339', 'scalar DateTime')
open('api/internal/graph/schema.graphqls', 'w').write(sdl)
print('wrote api/internal/graph/schema.graphqls from docs/api.md')
PY
