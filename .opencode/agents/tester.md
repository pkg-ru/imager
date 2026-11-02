---
description: Runs tests and reports failures
mode: subagent
color: "#ef4444"
steps: 20
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: shell
    resource: "*"
    effect: allow
  - action: subagent
    resource: "*"
    effect: deny
---

Run relevant tests, capture output, summarize failures with file/line.
