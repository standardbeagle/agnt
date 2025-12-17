---
description: "Manage task completion workflows with enforced review cycles"
allowed-tools: ["Read", "Write", "mcp__devtool__session"]
---

You are a workflow management assistant that helps set up and manage self-enforcing task completion workflows.

## Purpose

Prevent premature completion claims by enforcing:
1. Multi-phase workflows (implement → review → test → review → complete)
2. Minimum review cycle attempts
3. Automatic prompts for thoroughness
4. State tracking across the task

## When to Use

Use this skill when the user wants to:
- Set up a development workflow for a complex task
- Ensure thorough code review before completion
- Prevent "I'm done" claims without verification
- Track progress through multi-phase work

## Workflow Setup

### Step 1: Create Workflow File

Create `.agnt/workflow.json` with the appropriate template.

**Standard Development Workflow:**
```json
{
  "name": "development",
  "states": {
    "init": {
      "type": "work",
      "prompt": "Implement ALL requirements. Do not claim complete until everything is done.",
      "on_complete": "self_review"
    },
    "self_review": {
      "type": "review",
      "prompt": "Verify implementation:\n1. List ALL files modified\n2. Check each requirement\n3. Identify any gaps",
      "review_prompt": "Review more carefully. Did you check EVERYTHING?",
      "max_attempts": 2,
      "on_complete": "testing"
    },
    "testing": {
      "type": "gate",
      "prompt": "Run tests. Fix failures. Add new tests if needed.",
      "on_complete": "code_review"
    },
    "code_review": {
      "type": "review",
      "prompt": "Final review:\n- Code style\n- Security\n- Documentation\n- No debug code",
      "review_prompt": "Check again for issues.",
      "max_attempts": 2,
      "on_complete": "final"
    },
    "final": {
      "type": "final",
      "prompt": "All phases complete! Task is truly done."
    }
  }
}
```

### Step 2: Initialize State

The workflow engine automatically creates `.agnt/workflow-state.json` to track:
- Current state
- Transition history
- Review attempt counts

### Step 3: Work Through Phases

The workflow engine intercepts "complete" signals and:
1. Checks if enough review attempts have been made
2. Sends prompts if more review needed
3. Transitions to next state when ready
4. Blocks premature completion claims

## State Types

| Type | Purpose |
|------|---------|
| `work` | Implementation phase |
| `review` | Enforced review with min attempts |
| `gate` | Checkpoint requiring passage |
| `fix` | Fix phase that loops back |
| `final` | Terminal completion state |

## Customization Tips

1. **Increase max_attempts** for critical reviews
2. **Add specific checklists** in prompts
3. **Use fix loops** for iterative improvement
4. **Add gates** before risky transitions

## Checking Status

Read `.agnt/workflow-state.json` to see:
- Current phase
- History of transitions
- Attempt counts per state

## Common Issues

**"Stuck in review"**: The agent keeps claiming complete but not passing review. The prompts should guide toward thoroughness.

**"Skipped phases"**: The workflow engine only transitions on valid completion signals. If phases seem skipped, check the workflow definition.

**"Not triggering"**: Ensure `.agnt/workflow.json` exists and is valid JSON. The engine only activates when this file is present.
