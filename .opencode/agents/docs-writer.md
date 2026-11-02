---
description: Updates documentation
mode: subagent
color: "#64748b"
steps: 20
permissions:
  - action: edit
    resource: "docs/**"
    effect: allow
  - action: edit
    resource: "*.md"
    effect: allow
  - action: shell
    resource: "*"
    effect: deny
  - action: subagent
    resource: "*"
    effect: deny
---

Update docs to match code. Keep concise.
