---
name: unreachable
description: Names a model that does not exist, so the harness's malformed path is exercised.
model: claude-this-model-does-not-exist
tools: []
---
<!-- The obvious version of this probe — a prompt demanding prose — does not work: --json-schema
     constrains the output, so a figure told to write paragraphs still returns a valid verdict. That
     is a good property of the harness and a bad property of the probe. With the shape guaranteed,
     the realistic way a call produces nothing is that it never returns: a refusal, the budget cap,
     the timeout, a bad model id. This probe takes the last of those, because it is the only one
     that is deterministic. -->
Pick the better change and answer with the JSON object only.
