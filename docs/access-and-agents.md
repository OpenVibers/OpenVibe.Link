# Machine access and agent messages

Network-paired Links can ask to use another machine. The target owner answers on that machine through the same approval list and tray notification used by computer actions, or from a signed-in Network surface. An approval creates a time-limited grant for only the selected scopes. Revoking it ends further use.

## Request and answer

```sh
openvibe-link access request 'Alex desktop' --scope screen,desk --for 1h --reason 'Checking the build'
openvibe-link access list
openvibe-link access approve req_… --scope screen --for 30m
openvibe-link access deny req_…
openvibe-link access revoke gra_…
```

`--for` defaults to 1 hour and accepts a Go duration through 30 days, or `1d` through `30d`. `forever` is accepted only for `agent` and `send`; Network also requires both machines to have the same owner. The target can narrow scopes and shorten duration when approving. `access list` groups waiting requests, allowed grants and ended access. `--json` returns the same data for scripts without bearer grant tokens.

The scopes are `agent` (message local agents), `send` (offer Locker-sealed files), `screen` (capture or watch the screen), `desk` (use a private desk), `computer` (run only the target's enabled computer actions), and `worker` (run sandbox jobs). A computer grant never raises the target's own `observe`, `ask`, `trusted`, or `full` level. At `ask`, each acting command still needs the target owner's local answer. The target's capability switches and stop latch still apply.

The tray shows a machine request with **Allow** and **Deny** actions. `openvibe-link approvals` lists it beside computer requests, and `openvibe-link approve <nonce>` or `deny <nonce>` uses the same local approval socket. Network settles an answer once, even if two signed-in surfaces present it at once.

Each relayed use carries Network's signed grant JWT. Bot checks the sending device, target, scope, and current grant before forwarding. The target Link independently verifies Network's published signing key, signature, issuer, machine audience, sender, scope, expiration, and current Network grant state. It records the use in local activity. The direct LAN send path requires the same signed grant and live check in addition to the owner's LAN HMAC. A stopped, expired, revoked, or unavailable grant fails closed.

## Agent messages

Set `agents_talk: true` in Link's config to turn on **Let my agents talk**. It is off by default. Once on, Link registers the installed agent CLIs found by its fleet probe with Network's directory. Incoming `agent.task` frames enter a local inbox only while the switch is on. Cross-machine tasks also require an `agent` grant. Tasks carry the sending agent, person and thread; unanswered threads expire after 24 hours. An agent's message supplies information, never authority to operate the machine.

`openvibe-link mcp` serves the local MCP tools over stdio and the running Link's protected local control socket:

| Tool | Example arguments | Answer |
| --- | --- | --- |
| `agents_list` | `{}` | Directory entries |
| `agent_ask` | `{"agent":"agt_…","text":"Can you check this?","wait_seconds":30}` | Completed result, or a pending task id after the wait |
| `agent_tell` | `{"agent":"agt_…","text":"Build passed"}` | Created task and thread |
| `agent_inbox` | `{}` | Tasks addressed to this local CLI |
| `agent_reply` | `{"task":"tsk_…","text":"Done"}` | Completed task and thread |
| `machines_list` | `{}` | Machines and held grants, without tokens |
| `machine_request_access` | `{"machine":"lnk_…","scopes":["send"],"for_seconds":3600}` | Pending request |
| `machine_use` | `{"machine":"lnk_…","scope":"send","action":"offer","data":{"envelope":"<sealed Locker envelope>"}}` | One operation's answer, or a refusal |

`machine_use` accepts `task` for `agent`, `offer` for `send`, `screen` or `screen_stream` for `screen`, `desk` or `desk.stream` for `desk`, `exec`, `fs`, `input`, `browser` or `probe` for `computer`, and `run`, `status` or `cancel` for `worker`. Its `data` is the existing local feature's JSON value, except an `offer` wraps an `envelope`, `run` carries a platform job, and worker `status`/`cancel` take `{ "id": "job_…" }`. The target checks the specific scope before dispatch. Agent tasks are queued; they do not silently start an agent CLI.

For a CLI that supports an MCP server, Link can print the configuration to add by hand, or offer to install it. Installing prompts before writing any tool configuration:

```sh
openvibe-link mcp --print-config claude
openvibe-link mcp --print-config codex
openvibe-link mcp --print-config muse
openvibe-link mcp --print-config opencode
openvibe-link mcp install codex
```

Use the corresponding CLI name with `install` for Claude Code, Muse or OpenCode. The server identifies the calling CLI; `agent_inbox` and `agent_reply` are restricted to that CLI's registered directory entry.
