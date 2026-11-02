---
description: Coordinates multi-step work, plans, delegates to specialized subagents, integrates results
mode: primary
color: "#4f46e5"
steps: 40
permissions:
  - action: subagent
    resource: "*"
    effect: deny
  - action: subagent
    resource: code-writer
    effect: allow
  - action: subagent
    resource: tester
    effect: allow
  - action: subagent
    resource: reviewer
    effect: allow
  - action: subagent
    resource: docs-writer
    effect: allow
  - action: subagent
    resource: explore
    effect: allow
  - action: subagent
    resource: general
    effect: allow
  - action: edit
    resource: "*"
    effect: ask
  - action: shell
    resource: "*"
    effect: ask
---

You are the Orchestrator. Break work into clear tasks, delegate to the right subagent, verify results, and keep context minimal. Prefer small, focused subagent calls. Summarize outcomes concisely.
