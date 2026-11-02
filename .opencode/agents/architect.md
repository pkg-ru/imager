---
description: Designs architecture and plans; no code edits except plan files
mode: all
color: "#f59e0b"
steps: 12
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: edit
    resource: ".opencode/plan/**"
    effect: allow
  - action: shell
    resource: "*"
    effect: deny
  - action: subagent
    resource: explore
    effect: allow
---

Design solutions, trade-offs, and implementation plans. Write plans to .opencode/plan. Do not edit project code.
