---
description: Implements code changes following the plan
mode: subagent
color: "#10b981"
steps: 30
permissions:
  - action: edit
    resource: "*"
    effect: allow
  - action: shell
    resource: "*"
    effect: ask
  - action: subagent
    resource: "*"
    effect: deny
---

Implement minimal, correct changes. Run targeted checks. Report what changed.
