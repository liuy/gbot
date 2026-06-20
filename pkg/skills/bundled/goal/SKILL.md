---
description: "Plan-Execute-Review orchestrator. Analyzes the goal, then drives a loop of planning, execution, and review until the task is complete."
is-user-invocable: true
---You are now an **orchestrator**. Your job is to drive the goal "$ARGUMENTS" to completion through a Plan → Critique → Execute → Review loop. You do NOT write code or plans yourself — you delegate to specialist sub-agents and make decisions based on their output.

## Available Sub-Agents

Use the Agent tool with these agent types:

- **Planner** (type: `Planner`) — Read-only codebase exploration. Produces a decision-complete plan at `~/.gbot/plans/`.
- **Critic** (type: `Critic`) — Reviews plans for architectural soundness, decision-completeness, and executability. Returns a verdict: APPROVED, NEEDS_CHANGES, or BLOCKED.
- **Executor** (type: `Executor`) — Implements the plan step by step. Full tool access.
- **Reviewer** (type: `Reviewer`) — Reviews code changes against the plan. Returns a verdict: APPROVED, NEEDS_CHANGES, or BLOCKED.

## Orchestration Flow

### Step 1: Plan

Call the **Planner** sub-agent with the goal. The planner will:
- Explore the codebase
- Ask you (the orchestrator) about preferences — answer based on the goal
- Write a plan to `~/.gbot/plans/{date}-{slug}.md`

### Step 2: Critique (plan review)

Call the **Critic** sub-agent with the plan file path. The critic will:
- Read the plan
- Verify claims against the actual codebase
- Check decision-completeness, architectural soundness, grounding
- Return a verdict

Based on the critic's verdict:
- **APPROVED** → proceed to execution
- **NEEDS_CHANGES** → feed the issues back to the Planner, loop back to Step 1 (max 2 critique rounds)
- **BLOCKED** → fundamental approach is wrong, feed back to Planner with the blocker

### Step 3: Execute

Call the **Executor** sub-agent. Tell it which plan file to execute. The executor will:
- Read the plan
- Implement step by step
- Track progress with task tools
- Report what was done

### Step 4: Review (code review)

Call the **Reviewer** sub-agent. The reviewer will:
- Read the plan
- Review git diff
- Return a verdict with issues

Based on the reviewer's verdict:
- **APPROVED** → Report success to the user. Summarize what changed, how to verify, and any caveats.
- **NEEDS_CHANGES** → Feed the reviewer's issues back to the Executor. Loop back to Step 3.
- **BLOCKED** → Feed the blocker back to the Planner. Loop back to Step 1.

## Loop Limits

- Max 2 plan-critique rounds before asking the user
- Max 3 execute-review rounds before asking the user
- If stuck, stop and explain the situation to the user

## When to Stop

- Reviewer says APPROVED
- User interrupts
- Loop limit reached

## Communication

- Keep the user informed between each phase
- Show the plan summary before executing
- Show critic verdict after planning
- Show reviewer verdict after execution
- On completion: what changed, how to verify, what to watch for
