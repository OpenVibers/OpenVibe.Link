# Skills: what OpenVibe writes into your coding tools

OpenVibe.Codes keeps skills, rules and MCP server records per person and per project
([openvibe.codes/skills](https://openvibe.codes/skills)). This command copies the ones you chose into the folders the
tools already read, on this machine, inside one marked block.

```sh
openvibe-link skills sync --tools claude-code,codex --project ~/code/my-project
openvibe-link skills sync --tools claude-code --dry-run      # the diffs, nothing written
openvibe-link skills sync --undo                             # take every block out again
```

`OPENVIBE_TOKEN` must hold your OpenVibe access token (the same one `openvibe-link memory sync` uses). The sync asks
`GET /api/v1/me/skills` on openvibe.codes, with `?project=<slug>` for a project, so it only ever sees what you chose
there.

## Where each tool reads

| Tool | Skills | Rules |
|---|---|---|
| Claude Code | `~/.claude/skills/<slug>/SKILL.md` | `~/.claude/CLAUDE.md` |
| Codex | `~/.codex/skills/<slug>/SKILL.md` | `~/.codex/AGENTS.md` |
| OpenCode | `~/.config/opencode/skills/<slug>/SKILL.md` | `~/.config/opencode/AGENTS.md` |
| Command Code | `~/.commandcode/skills/<slug>/SKILL.md` | `~/.commandcode/AGENTS.md` |
| Aider | — | `<project>/CONVENTIONS.md` |

In a project the sync writes `.claude/skills/`, `.codex/skills/` (or `.opencode/skills/`, `.commandcode/skills/`) and
that project's `CLAUDE.md`/`AGENTS.md`/`CONVENTIONS.md`. A project file may be committed, so the command says so.

## The marked block

```
---
name: "ship-a-change"
description: "Open a PR, wait for CI, and merge only when it is green."
---
<!-- openvibe-skills:start -->
# Ship a change
...
<!-- openvibe-skills:end -->
```

The frontmatter is written only when the Link creates the file; an existing `SKILL.md` keeps its own header. Everything
else the Link writes is inside the block, and the block's markers are scrubbed out of content before it is written, so a
record cannot open or close a block. The rest of the file — your own notes above or below — is never changed.

Every tool is off until you pass `--tools`. The first write to a file shows a line diff and asks; `--yes` skips the
question. `--dry-run` reads the hub, shows the same diffs and writes nothing (not even the state file). `--undo` removes
every block the Link wrote and deletes a file or skill folder the Link created when nothing else was in it; `--disable
a,b` does that for named tools.

## What is not written yet

The hub stores MCP servers, slash commands and hooks too (`codes.skill@1` kind `mcp`, `command`, `hook`), and they can
be published, versioned and assigned like a skill. This sync writes skills and rules only: each tool keeps MCP servers
in a different JSON or TOML config (`~/.codex/config.toml`, OpenCode's `opencode.jsonc`, …), and writing those safely
needs a per-tool adapter and its own dry run, so it is a follow-up rather than a half-written config here.

## State

The opt-in choices and what was written live in the Link's state directory, `skills-sync.json`, mode 0600. It holds
paths and versions, never the text of a skill. `--state FILE` and `--user-home DIR` are for tests and unusual installs.
