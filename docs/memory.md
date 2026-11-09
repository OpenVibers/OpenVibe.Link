# Memory: the Link reads what your tools remember

The Link reads the memory files of the coding tools on your machine and tells
you what it found. The scan is **read-only, local and redacted**:

- it opens the files it knows about and nothing else;
- it never writes a file and never opens a network connection;
- it removes the secrets before anything is printed;
- it never follows a symlink out of the home folder or the project folders it
  was given.

The upload sends what it found to **your own memory store** (OpenVibe.AI),
after it has shown you what would leave the machine and after you have said
yes. The store is yours: it is sealed, only your own token opens it, and
nothing but the items themselves is sent.

This page is stage M1 of OpenVibe Memory (the readers), stage M2a (the
upload) and stage M4 (the sync back into the tools). The page that shows your
memory comes later.

## The commands

```sh
openvibe-link memory scan [--tools a,b] [--project DIR …] [--json]
openvibe-link memory upload [--tools a,b] [--project DIR …] [--dry-run] [--yes]
openvibe-link memory status [--show]
openvibe-link memory sync [--tools a,b] [--project DIR …] [--dry-run] [--yes]
openvibe-link memory export [--out FILE] [--scope SCOPE] [--force]
openvibe-link memory import --file FILE [--dry-run] [--yes]
```

| Flag | What it does |
|---|---|
| `--tools a,b` | only these adapters (default: all) |
| `--project DIR` | read this project as well; may be given more than once |
| `--json` | print the items as JSON (scan) |
| `--dry-run` | print the summary and send nothing (upload, import) |
| `--yes` | do not ask before sending (upload, import) |
| `--show` | print the items themselves (status) |
| `--endpoint URL` | the memory store (default `https://ai.openvibe.services`) |
| `--token-env NAME` | the variable the access token is read from (default `OPENVIBE_TOKEN`) |
| `--user-home DIR` | read this directory as the person's home (default: yours) |
| `--out FILE` | export: where to write the portable file (`-` for stdout; default `openvibe-memory-YYYY-MM-DD.json`) |
| `--scope SCOPE` | export: only `user` or one `project:<slug>` |
| `--force` | export: replace a file that already exists |
| `--file FILE` | import: the portable file (`-` for stdin) |
| `--tools a,b` | sync: turn these tools on |
| `--disable a,b` | sync: turn these tools off and remove their blocks |
| `--undo` | sync: remove every block this command wrote and turn the tools off |
| `--state FILE` | sync: the opt-in file (default: the Link's state directory) |

Without `--project` only the user-level files are read: the files that hold
your own preferences across every project. With `--project` the project files
are read too, and their items carry the scope `project:<absolute path>`.

The scan prints every adapter, every path it looked at and whether it was
there, and how many items each file gave:

```
Memory scan of /home/you
No project given: only the user-level files were read.

Claude Code (claude-code) — 3 items
  docs: https://code.claude.com/docs/en/memory
  - /home/you/.claude/CLAUDE.md: 3 items
  - /home/you/.claude/rules/**/*.md: not there
...
```

## The adapters

Each adapter is data: where to look, and how to read what is there.

| Adapter | id | What it reads | Docs |
|---|---|---|---|
| Claude Code | `claude-code` | `~/.claude/CLAUDE.md`, `~/.claude/rules/**/*.md`, `~/.claude/projects/*/memory/*.md` (the `MEMORY.md` index is skipped), and in a project `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAUDE.local.md`, `.claude/rules/**/*.md` | [code.claude.com/docs/en/memory](https://code.claude.com/docs/en/memory) |
| AGENTS.md | `agents` | `~/.codex/AGENTS.md` and the project's `AGENTS.md` | [agents.md](https://agents.md/) |
| Gemini CLI | `gemini` | `~/.gemini/GEMINI.md` and the project's `GEMINI.md` or `.gemini/GEMINI.md` | [geminicli.com/docs/cli/gemini-md](https://geminicli.com/docs/cli/gemini-md/) |
| Cursor | `cursor` | the project's `.cursor/rules/*.mdc` | [cursor.com/docs/context/rules](https://cursor.com/docs/context/rules) |
| GitHub Copilot | `copilot` | the project's `.github/copilot-instructions.md` and `.github/instructions/*.instructions.md` | [docs.github.com](https://docs.github.com/en/copilot/customizing-copilot/adding-repository-custom-instructions-for-github-copilot) |
| Windsurf | `windsurf` | `~/.codeium/windsurf/memories/**` and the project's `.windsurf/rules/**` | [docs.windsurf.com](https://docs.windsurf.com/windsurf/cascade/memories) |
| Cline | `cline` | the project's `.clinerules`, `.clinerules/**` and `memory-bank/*.md` | [docs.cline.bot/features/cline-rules](https://docs.cline.bot/features/cline-rules) |
| Roo Code | `roo` | the project's `.roo/rules/**` and `memory-bank/*.md` | [docs.roocode.com](https://docs.roocode.com/features/custom-instructions) |
| Kiro | `kiro` | `~/.kiro/steering/*.md` and the project's `.kiro/steering/*.md` | [kiro.dev/docs/steering](https://kiro.dev/docs/steering/) |
| Continue | `continue` | `~/.continue/rules/**` and the project's `.continue/rules/**` | [docs.continue.dev/customize/rules](https://docs.continue.dev/customize/rules) |
| Aider | `aider` | the project's `CONVENTIONS.md` and the files its `.aider.conf.yml` names under `read:` | [aider.chat/docs/usage/conventions](https://aider.chat/docs/usage/conventions.html) |
| Command Code | `command-code` | `~/.commandcode/**` and the project's `.commandcode/**` | [commandcode.ai/docs/memory](https://commandcode.ai/docs/memory) |
| OpenCode | `opencode` | `~/.config/opencode/AGENTS.md`, the project's `AGENTS.md` and the files its `opencode.json` names under `instructions` | [opencode.ai/docs/rules](https://opencode.ai/docs/rules/) |

### What is never read

- The chat history of any tool (Aider's `.aider.chat.history.md`, Claude's
  transcripts, Continue's sessions).
- The settings, the hooks, the skills and the plugins of any tool.
- Any folder that is not one of the paths above. The home folder itself is
  never read as a whole: each adapter names the exact files and the narrow
  globs of its own tool, and a glob always starts at a fixed folder.
- A file over 256 KB (it is skipped with a note) and a binary file.

## How a file becomes items

- The frontmatter is read, not turned into items.
- A markdown file is split by its headings: every top-level bullet is one
  item, and every paragraph under a heading is one item.
- An item is at most 2,000 characters; a longer one is cut and marked
  `truncated`.
- An item that is only a secret is dropped.

## The kinds

| Kind | What it is |
|---|---|
| `preference` | how the person wants things done |
| `fact` | a fact about the person or the project |
| `project_rule` | a rule that holds in a project |
| `procedure` | a way of doing something |
| `decision` | a decision that was taken, with its reason |
| `lesson` | something that was learned the hard way |

The kind comes from the tool when it says it (Claude Code's auto memory writes
a `metadata.type` in its frontmatter: `user`, `feedback`, `project` or
`reference`), otherwise from the heading the item came from, otherwise from the
scope: a file of the person is about them, a file of a project holds that
project's rules.

## The secrets

Nothing leaves the parser without being scrubbed first. The scrubbing runs over
the **whole file body before it is split into items**, and again over every
item. A secret that spans two items — a key block whose BEGIN and END lines
land in different items, an `.env` block in a fence — is only seen by the
whole-file pass. The frontmatter is scrubbed too, before any of its values is
used: a `description` becomes the heading of the item.

The patterns are the ones of OpenVibe.Codes' harness (`harness/events.js`,
`SECRET_PATTERNS`) plus the common gitleaks shapes and the shapes that turn up
in a person's own files:

- OpenAI- and Anthropic-style keys (`sk-…`), Stripe keys (`sk_live_…`);
- GitHub tokens (`ghp_…`, `gho_…`, `github_pat_…`), GitLab (`glpat-…`);
- AWS keys (`AKIA…`, `ASIA…`) and `aws_secret_access_key`;
- Slack tokens (`xoxb-…`) and Slack app tokens (`xapp-…`);
- Google (`AIza…`), npm (`npm_…`), Hugging Face (`hf_…`), SendGrid
  (`SG.….…`), Telegram and Discord bot tokens, Twilio keys;
- JWTs;
- **every PEM private key form** — RSA, EC, DSA, OPENSSH, PGP and ENCRYPTED —
  from `-----BEGIN … PRIVATE KEY …-----` to its END line. A key block with
  **no END line** (a truncated file, a cut paste) is redacted from its BEGIN
  line to the end of its fenced block, or to the end of the text when it is
  not in a fence;
- **env-style assignments**: a name of `[A-Za-z0-9_.-]*` that ends in
  `password`, `passwd`, `pwd`, `passphrase`, `secret`, `token`, `api_key`,
  `apikey`, `api-key`, `access_key`, `private_key`, `client_secret`, `auth`,
  `credential`, `credentials`, `cookie` or `session`, followed by `=`, `:` or
  `=>` and a value. The name and the separator stay, the value goes; a quoted
  value keeps its quotes;
- **prose**: "the password is …", "the passphrase was …";
- **headers**: `Authorization: Bearer …`, `Authorization: Basic …`,
  `x-api-key: …`, and a bare `Bearer …`;
- **webhook URLs** of Slack and Discord, whole;
- **card numbers**: 13 to 19 digits, with spaces or dashes, that pass the Luhn
  check. A long number that does not pass it stays: an order number is not a
  card;
- connection strings that carry a password.

Each match becomes `[redacted]`. The item is marked `redacted` and keeps a
count of how many secrets were removed; the values are never kept. An item
that held nothing but a secret is dropped.

Over-redaction is preferred over a leak: a harmless `max_tokens: 100000` is
redacted too. It stays bounded — "use 4 spaces for indentation", "the build
takes 12 minutes" and a 10-digit number that fails the Luhn check are left
alone.

Every adapter's fixture in `internal/memory/testdata` holds one secret of a
different shape, and the test proves that none of them reaches an item. The
Claude Code fixture holds more: a key block split over two items, a key block
with no END line, and an `.env` block.

## The upload

```sh
openvibe-link memory upload [--tools a,b] [--project DIR …] [--dry-run] [--yes]
```

The upload runs the same scan as `memory scan` — the same adapters, the same
paths, the same redaction — and sends what it found to **your own memory
store** (OpenVibe.AI).

The token is your own OpenVibe access token, the same one the OpenVibe.Codes
CLI uses. It is read from the environment (`OPENVIBE_TOKEN`, or the variable
`--token-env` names), never from the config file, and it never appears in a
log line, in an error or in a URL.

Nothing is sent before you have seen the summary and said yes. `--dry-run`
never sends anything at all:

```
Memory upload of /home/you
Projects: /srv/work/demo

Claude Code (claude-code) — 20 items
  - .claude/CLAUDE.md: 3 items
  - .claude/rules/sandbox.md: 6 items
  - CLAUDE.md: 3 items
  2 items redacted (3 secrets removed)

Scopes: project:demo, user
20 items to send, 2 redacted (3 secrets removed).

Send these 20 items to your memory store? [y/N]
```

### What leaves the machine

| Field | What it is |
|---|---|
| `text` | the item, already redacted |
| `kind` | `preference`, `fact`, `project_rule`, `procedure`, `decision` or `lesson` |
| `scope` | `user`, or `project:<slug>`: the slug is the project's folder name, lowercased and slugged |
| `source` | the id of the adapter the item came from (`claude-code`, …) |
| `source_ref` | the path of the file relative to the folder it was scanned from, and the heading it was under |
| `confidence` | `1`: it is your own file |

What a coding tool wrote is a suggestion until you accept it, so the items are
marked as written by a tool when they arrive at the store.

### What never leaves the machine

- **the absolute path of any file**: the path that is sent is relative to the
  folder that was scanned (`.claude/CLAUDE.md`, not `/home/you/.claude/CLAUDE.md`);
- **the name of the machine and the name of the person**;
- **the value of a secret**: the redaction runs before the item is counted,
  and the store scrubs again everything it receives;
- **an item whose text is empty** after the redaction;
- the files themselves, the settings of a tool and the chat history of a tool:
  the scan never reads them.

### The store

The items go to `POST https://ai.openvibe.services/api/v1/memory/items:batch`
in batches of 200, with your token as a Bearer. A store that is busy or broken
is tried again with a wait. The store scrubs the text again, refuses an item
that is nothing but a secret, and counts what it did:

```
Sent: 18 new, 2 already there, 0 refused.
```

An item the store would not take is counted as refused and the rest of the
batch is sent again: nothing is dropped without being said.

## The status

```sh
openvibe-link memory status [--show]
```

It shows what your own store holds: how many items there are, per source and
per scope. The text of an item is only printed with `--show`.

```
Your memory store holds 42 items.

Per source:
  claude-code: 30
  cursor: 12

Per scope:
  project:demo: 17
  user: 25
```

## Deleting everything from one source

The store has no soft delete: a delete removes the item. To remove everything
that came from one adapter, ask the store to delete that source:

```sh
curl -X DELETE "https://ai.openvibe.services/api/v1/memory/items?source=claude-code" \
  -H "Authorization: Bearer $OPENVIBE_TOKEN"
```

That removes every item of that source, for you only.

## The JSON

```json
{
  "home": "/home/you",
  "projects": ["/srv/work/demo"],
  "adapters": [
    {
      "id": "claude-code",
      "name": "Claude Code",
      "docs": "https://code.claude.com/docs/en/memory",
      "files": [
        {
          "path": "/home/you/.claude/CLAUDE.md",
          "found": true,
          "items": 3
        },
        {
          "path": "/home/you/.claude/rules/**/*.md",
          "found": false
        }
      ],
      "items": 3
    }
  ],
  "items": [
    {
      "text": "Prefer tabs in Go files",
      "kind": "preference",
      "scope": "user",
      "source": "claude-code",
      "path": "/home/you/.claude/CLAUDE.md",
      "updated": "2026-10-30T15:06:16Z",
      "heading": "Personal setup"
    }
  ]
}
```

An item may also carry:

| Field | Meaning |
|---|---|
| `redacted` | the item held a secret that was removed |
| `redactions` | how many secrets were removed |
| `truncated` | the item was longer than 2,000 characters |

A file that was looked at but could not be used carries a `note`:
`skipped: larger than 256 KB`, `skipped: a binary file`,
`skipped: link leaves the scanned paths`.

## The safety tests

`internal/memory` has a test for each of these:

- no adapter ever points at the home folder, and no glob is wider than the
  tool's own folders;
- a scan without a project reads only the user-level files;
- a symlink out of the scanned paths is not followed;
- a file over 256 KB and a binary file are skipped;
- the package does not import `net/http`: it never opens a network
  connection.

`internal/memoryupload` and `internal/memorystore` have the tests of the
upload:

- the scope of a project is the slug of its folder name, never an absolute
  path;
- the path that is sent is relative to the folder that was scanned;
- an item that is empty after the redaction never leaves the machine;
- a dry run sends nothing, and nothing is sent before the person says yes;
- the items go in batches of 200, and a busy store is tried again;
- the request that leaves holds no absolute path, no hostname and no name of
  the person;
- the token is never in an error message.

## The sync: back into the tools

```sh
openvibe-link memory sync [--tools a,b] [--project DIR …] [--dry-run] [--yes]
openvibe-link memory sync --disable a,b
openvibe-link memory sync --undo
```

The sync is the other direction: it writes **your own** profile and the
project's rules from your memory store into the places the coding tools
already read. It is **off for every tool** until you turn that tool on:

| Tool | On with | Files it writes |
|---|---|---|
| AGENTS.md | `--tools agents` | `~/.codex/AGENTS.md`, the project's `AGENTS.md` |
| Claude Code | `--tools claude-code` | `~/.claude/CLAUDE.md`, the project's `CLAUDE.md`: a thin `@` import of the AGENTS.md block, not a copy |
| Gemini CLI | `--tools gemini` | `~/.gemini/GEMINI.md`, the project's `GEMINI.md` |
| Cursor | `--tools cursor` | the project's `.cursor/rules/openvibe-memory.mdc` |
| GitHub Copilot | `--tools copilot` | the project's `.github/copilot-instructions.md` |

`--disable a,b` turns those tools off and takes their blocks out again.
`--undo` does that for every tool that is on, also when `--project` is not
named again: the state file remembers every file it wrote.

### One marked block, never your text

Every write is inside one block, and the text outside it is not touched:

```markdown
<!-- openvibe-memory:start -->
<!-- Managed by OpenVibe Link from your OpenVibe Memory. … -->
## About you

…the cached profile…

## Project rules

- …an active project_rule item…
<!-- openvibe-memory:end -->
```

- A file that already has the block has only that block replaced.
- A file with two blocks, or half a block, is refused and left alone.
- The memory text is scrubbed of the markers before it is written, so an item
  cannot open or close a block.
- `--undo` removes the block and the separator the Link added before it; a file
  that the Link created (the Cursor rule) is removed again. The state file
  records the separator so the hand-written part is byte-identical afterwards.

### The diff gate

The first time the Link writes a file it shows a diff and asks. Anything but
`y` or `yes` leaves the file as it was. `--yes` answers for you, for scripted
setups; `--dry-run` shows every diff and writes nothing. A later sync of a file
that was already confirmed shows the diff and writes it without asking again.

Project files may be shared or committed: the command says so before it writes
one, and the block says so too. Turn the tool off with `--disable` if that is
not what you want.

### Where the content comes from

The sync uses the same person token as the upload (`OPENVIBE_TOKEN`, or
`--token-env`), never a copy held by the Link. It reads:

- `GET /api/v1/memory/profile?scope=user`: the cached profile of you;
- `GET /api/v1/memory/profile?scope=project:<slug>` and
  `GET /api/v1/memory/items?scope=project:<slug>`: the project's profile and its
  active `project_rule` items.

The project slug is the same slug the upload uses: the folder's name,
lowercased and slugged, never an absolute path. Nothing is written when the
store cannot be read; the command fails before it touches a file.

The opt-in state lives in the Link's state directory
(`memory-sync.json`, mode 0600). It holds the tools that are on, the paths that
were written and whether the first diff was confirmed: paths and choices, never
the memory text.

## The portable file

```sh
openvibe-link memory export [--out FILE] [--scope SCOPE] [--force]
openvibe-link memory import --file FILE [--dry-run] [--yes]
```

`memory export` writes the store's one portable file, `openvibe.memory-export@1`
(see `OpenVibe.AI/docs/memory-m5.md` and the `ai.memory-export@1` contract): a
JSON document with every note's text, kind, scope, source, status, confidence
and source reference, and deliberately no account id, note id, merge link or
conflict pair. The file is written mode 0600, and an existing file is not
replaced unless `--force` is given; `--out -` writes it to stdout.

`memory import` reads the same file back into the store. It refuses another
format before calling the store, shows how many notes the file holds, and asks
unless `--yes` is given (`--dry-run` shows the count and sends nothing). The
store redacts every item again, skips an item that is only a secret, imports a
`conflict` item as a suggestion (its pair did not travel), and merges a note
that says the same thing as one already there by its text MAC.

The token is the same `OPENVIBE_TOKEN` the upload uses; it is never printed and
never written to the file. The file itself is the person's plaintext memory:
treat it like a password export.
