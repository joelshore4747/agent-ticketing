#!/usr/bin/env python3
"""A role file as the JSON `claude --agents` takes, so a step can run in another project's checkout.

    agent_def.py <.claude/agents/role.md>   -> {"role": {"description", "prompt", "model"?, "tools"?}}

`claude --agent <role>` looks the role up in the working directory's `.claude/agents/`, and a step
working a connected project runs in that project's checkout, where the role files do not exist (the
first real FS step failed on exactly that, 2026-10-15). Passing the definition with `--agents` makes the
role travel with the step instead of depending on where it runs.
"""
import json
import os
import sys


def definition(path):
    """The frontmatter's description, model and tools, and the body as the prompt."""
    with open(path, encoding="utf-8") as f:
        text = f.read()
    _, front, body = text.split("---", 2)
    fields = {}
    for line in front.strip().splitlines():
        key, _, value = line.partition(":")
        fields[key.strip()] = value.strip()
    name = fields.get("name") or os.path.splitext(os.path.basename(path))[0]
    agent = {"description": fields.get("description", ""), "prompt": body.strip()}
    if fields.get("model"):
        agent["model"] = fields["model"]
    if fields.get("tools"):
        agent["tools"] = [t.strip() for t in fields["tools"].split(",") if t.strip()]
    return {name: agent}


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    print(json.dumps(definition(sys.argv[1])))
