---
description: "Set up a self-enforcing workflow state machine for task completion"
allowed-tools: ["Read", "Write", "mcp__agnt__session"]
---

Create a workflow state machine that prevents premature completion claims and enforces review cycles.

## The Problem

LLMs often:
- Claim "complete" after phase 1 of a 3-phase task
- Skip work or invent phases mid-task
- Need reminding to finish ALL the work
- Require multiple review/fix cycles before truly done

## The Solution

A self-transitioning state machine that:
1. Tracks current workflow phase
2. Intercepts "complete" signals
3. Enforces review cycles before allowing completion
4. Automatically prompts for next phase

## Workflow Templates

### Standard Development Workflow

Create `.agnt/workflow.json`:

```json
{
  "name": "development",
  "description": "Standard dev workflow with code review and testing",
  "states": {
    "init": {
      "type": "work",
      "prompt": "Starting development workflow. Please implement the requested feature/fix. Do not claim completion until ALL requirements are implemented.",
      "on_complete": "self_review"
    },
    "self_review": {
      "type": "review",
      "prompt": "Implementation reported complete. Before proceeding, please:\n1. List ALL files you modified\n2. Verify each requirement was addressed\n3. Check for any TODOs or incomplete sections\n4. Identify any edge cases not handled\n\nOnly say 'complete' when you've verified everything.",
      "review_prompt": "Please review more carefully. Check:\n- Did you implement ALL requirements?\n- Are there any incomplete sections?\n- Did you handle error cases?\n- Is the code properly tested?",
      "max_attempts": 2,
      "on_complete": "testing"
    },
    "testing": {
      "type": "gate",
      "prompt": "Self-review passed. Now run the test suite:\n1. Run all relevant tests\n2. Fix any failures\n3. Add tests for new functionality if needed\n\nReport 'complete' only when all tests pass.",
      "on_complete": "code_review"
    },
    "code_review": {
      "type": "review",
      "prompt": "Tests passing. Final code review:\n1. Check code style and conventions\n2. Look for potential bugs or security issues\n3. Verify documentation is updated\n4. Ensure no debug code or console.logs remain\n\nIf issues found, fix them. Say 'complete' when code is production-ready.",
      "review_prompt": "Please be thorough. Check:\n- Security vulnerabilities?\n- Performance issues?\n- Code duplication?\n- Missing error handling?",
      "max_attempts": 2,
      "on_complete": "final"
    },
    "final": {
      "type": "final",
      "prompt": "All phases complete! The implementation has passed:\n✓ Self-review\n✓ Testing\n✓ Code review\n\nThe task is now truly complete."
    }
  }
}
```

### Strict Review Workflow (More Cycles)

```json
{
  "name": "strict",
  "description": "Strict workflow with multiple mandatory review cycles",
  "states": {
    "init": {
      "type": "work",
      "prompt": "Starting strict workflow. Implement ALL requirements before reporting complete.",
      "on_complete": "review_1"
    },
    "review_1": {
      "type": "review",
      "prompt": "Review Round 1: Check completeness\n- Are ALL requirements implemented?\n- Any skipped or partial implementations?\n- List every file changed and why.",
      "review_prompt": "Not thorough enough. List EVERY change made and verify EVERY requirement.",
      "max_attempts": 2,
      "on_complete": "review_2"
    },
    "review_2": {
      "type": "review",
      "prompt": "Review Round 2: Check quality\n- Any bugs or edge cases?\n- Error handling complete?\n- Code readable and maintainable?",
      "review_prompt": "Look more carefully for issues. Check error paths and edge cases.",
      "max_attempts": 2,
      "on_complete": "testing"
    },
    "testing": {
      "type": "gate",
      "prompt": "Run ALL tests. Fix any failures. Add new tests if needed.",
      "on_complete": "review_3"
    },
    "review_3": {
      "type": "review",
      "prompt": "Final Review: Production readiness\n- Security checked?\n- Performance acceptable?\n- Documentation updated?\n- No debug code?",
      "review_prompt": "Final check - be extremely thorough.",
      "max_attempts": 1,
      "on_complete": "final"
    },
    "final": {
      "type": "final",
      "prompt": "Workflow complete after 3 review rounds and testing."
    }
  }
}
```

### Fix-Review Loop Workflow

```json
{
  "name": "fix-loop",
  "description": "Workflow that loops between fixing and reviewing",
  "states": {
    "init": {
      "type": "work",
      "prompt": "Implement the feature/fix.",
      "on_complete": "review"
    },
    "review": {
      "type": "review",
      "prompt": "Review the implementation. If ANY issues found, list them and we'll fix.",
      "review_prompt": "Look again - are you SURE there are no issues?",
      "max_attempts": 2,
      "on_complete": "confirm_no_issues"
    },
    "confirm_no_issues": {
      "type": "gate",
      "prompt": "You found no issues. Confirm by saying 'no issues confirmed' or list issues to fix.",
      "next": "fix",
      "on_complete": "testing"
    },
    "fix": {
      "type": "fix",
      "prompt": "Fix the identified issues, then report complete.",
      "on_complete": "review"
    },
    "testing": {
      "type": "gate",
      "prompt": "Run tests to verify.",
      "on_complete": "final"
    },
    "final": {
      "type": "final",
      "prompt": "Complete! All issues resolved and tests passing."
    }
  }
}
```

## State Types

| Type | Behavior |
|------|----------|
| `work` | Normal work phase, transitions on completion |
| `review` | Enforces N attempts before allowing "complete" |
| `gate` | Checkpoint that requires explicit passage |
| `fix` | Fix phase that loops back to review |
| `final` | Terminal state, workflow complete |

## State Properties

| Property | Description |
|----------|-------------|
| `type` | State type (see above) |
| `prompt` | Message sent when entering this state |
| `review_prompt` | Message sent when review attempt insufficient |
| `max_attempts` | Minimum review attempts before allowing passage |
| `on_complete` | Next state when "complete" is valid |
| `next` | Alternative next state |

## Checking Workflow Status

The current state is stored in `.agnt/workflow-state.json`:

```json
{
  "current_state": "review_1",
  "history": [
    {"from": "init", "to": "review_1", "reason": "Work complete", "time": "..."}
  ],
  "attempts": {
    "review_1": 1
  }
}
```

## Tips

1. **Be specific in prompts** - Tell the agent exactly what to check
2. **Use max_attempts** - Require multiple review passes for thoroughness
3. **Include checklists** - Give concrete items to verify
4. **Loop for fixes** - Use fix→review loops for iterative improvement
5. **Gate important transitions** - Use gates before testing/deployment
