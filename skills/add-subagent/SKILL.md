---
name: add-subagent
description: Add or change a sub-agent so that it works in both Claude Code and Codex CLI. Use when defining a new sub-agent, or when editing an existing sub-agent's instructions, description, tools, model, or effort.
---

# Add a Sub-agent

Each sub-agent has one shared body with its instructions, and two small stubs that register it with Claude Code and with Codex CLI. The stubs only hold the settings each tool needs, and point to the body, so the instructions are written once.

| File | Contents |
| --- | --- |
| `agents/<name>.md` | The instructions: plain Markdown, no frontmatter. |
| `.claude/agents/<name>.md` | Claude Code stub: frontmatter settings and a pointer to the body. |
| `.codex/agents/<name>.toml` | Codex CLI stub: settings and a pointer to the body. |

Use a lowercase, hyphenated `<name>`, such as `charter-summarizer`, for all three files. `agents/charter-summarizer.md` and its stubs are a complete example.

## Inputs

Before writing any files, make sure you have:

- **Name and purpose**: what the sub-agent does, what it receives, and what it produces.
- **Tools**: what the sub-agent needs to do, such as only reading files, writing files, or running commands.
- **Claude Code model and effort**: a model such as `sonnet`, `opus`, or `haiku`, and an effort level (`low`, `medium`, `high`, `xhigh`, or `max`).
- **Codex CLI model and reasoning effort**: a model such as `gpt-6-luna`, and a reasoning effort such as `medium` or `high`.

Ask the user for any input that the request doesn't settle. The models and efforts depend on how demanding the sub-agent's task is, so don't pick them silently. If the user has no preference, suggest `sonnet` with `medium` effort and `gpt-6-luna` with `high` reasoning effort, the settings `charter-summarizer` uses.

## Steps

1. **Write the body** in `agents/<name>.md`, starting with a `# Title` heading. Write it for any agent: describe what the sub-agent receives, what it produces, and how it replies, without naming tools that only exist in one CLI. Sub-agents exchange files through `.msgboard/`, following the rules the orchestrator gives them (see the Message board section of `AGENTS.md`).

2. **Add the Claude Code stub** in `.claude/agents/<name>.md`:

   ```markdown
   ---
   name: <name>
   description: <when to delegate to this sub-agent, and what to pass it>
   tools: <comma-separated tools, such as Read, Write>
   model: <Claude Code model>
   effort: <Claude Code effort>
   ---

   Follow the instructions in @agents/<name>.md.
   ```

   List only the tools the sub-agent needs. Leave out `Bash`, `Edit`, and `Write` unless it must run commands or write files.

3. **Add the Codex CLI stub** in `.codex/agents/<name>.toml`:

   ```toml
   name = "<name>"
   description = "<the same description as the Claude Code stub>"
   model = "<Codex CLI model>"
   model_reasoning_effort = "<Codex CLI reasoning effort>"
   sandbox_mode = "<read-only or workspace-write>"
   developer_instructions = """
   Follow the instructions in @agents/<name>.md.
   """
   ```

   Codex has no per-tool allowlist, so derive `sandbox_mode` from the Claude Code `tools`: `read-only` if the sub-agent only reads, and `workspace-write` if it writes files or runs commands.

4. **List the sub-agent** in the Sub-agents section of `AGENTS.md`, with one line on what it does, and describe where orchestrators should use it.

5. **Run `just precommit`**, which formats the Markdown and runs `just check-agent-stubs` to confirm that both stubs exist and their descriptions match.

## Changing a Sub-agent

- To change what a sub-agent does, edit only its body in `agents/<name>.md`.
- Keep the `description` identical in both stubs, and keep `tools` and `sandbox_mode` consistent with each other.
- When changing a model or effort, ask the user for the new settings for both CLIs, and update both stubs together.
