
<!-- TOKENOMICS:START -->
## Token Optimization Insights

_Last updated: 2026-06-06_

### Context Management
- Your context snowballs at **turn 22** on average (16% of sessions). Use `/compact` proactively after turn 20-22 on long sessions to prevent unbounded growth.
- You could benefit from subagents for parallel tasks. Consider splitting multi-file operations into parallel agent tasks.
- You read files you don't end up using. Use `Grep` first to locate relevant files before reading them — reduces unnecessary context by ~0%.
- You receive verbose command output. Prefer `Grep`/`Read` tools over bash commands when searching files to reduce output tokens.

### Prompt Quality
- **5%** of your prompts are under 10 words. Include specific file paths, function names, and expected outcomes to reduce clarification rounds.

### Model Usage
- You use Opus/Claude for **9%** of simple tasks. Prefer **Sonnet** for editing, small fixes, and exploration tasks to reduce token usage by ~5x on those sessions.
<!-- TOKENOMICS:END -->
