# Your own models for your OpenVibe work

If you already run a model at home (Ollama, LM Studio or llama.cpp's server on a gaming PC or a spare box with a GPU),
OpenVibe can use it for your own work: Vibe's answers, an agent's thinking, a summary. It costs nothing, and the prompt
never leaves your network. The machine opens no port: its OpenVibe Node already connects out to OpenVibe.Bot, and the
request comes back over that connection.

## How it works

1. OpenVibe.AI, on your behalf, asks OpenVibe.Bot to run one chat completion on your machine (`bot.command@1` kind
   `ai`, contracts 0.219.0).
2. Bot sends it to your Node, but only for your own machine, and only when you turned on **Let my AI use this
   computer's models** on openvibe.bot/machines. One person's request never goes to another person's machine.
3. The Node's `ai` plugin relays it to your model server, and the reply goes back the same way.

The plugin reaches one place only: the model server in its config. It reads nothing else on the machine and changes
nothing there. It runs one request at a time, because a model is heavy; another request is refused as busy, not
queued. The audit on Bot keeps the model and the token counts, never the text.

## Set it up

The installer does it with `--ai` (Node v0.28.0), added to the command openvibe.bot/machines gives you or to an
install you already have. It says which model server answers, or how to get one:

```sh
curl -fsSL https://node.openvibe.network/install | sh -s -- --network … --pairing pair_… --code … --ai
```

`--ai-endpoint http://127.0.0.1:8090` names a server on another port (it implies `--ai`). `--ai` works with `--worker`,
`--computer` and `--desk-host` too, and a driver chosen later keeps it.

By hand, add the plugin to the Node's config (`config.json`):

```json
{ "plugins": [{ "name": "ai" }] }
```

With nothing else set, it uses the first of these that answers:

| Server | Address |
|---|---|
| Ollama | `http://127.0.0.1:11434` |
| LM Studio | `http://127.0.0.1:1234` |
| llama.cpp's server | `http://127.0.0.1:8080` |

It offers every model the server lists. Restart the Node (`openvibe-node restart`), then turn on **Let my AI use this
computer's models** for the machine on openvibe.bot/machines.

| key | default | meaning |
|---|---|---|
| `endpoint` | the first that answers above | the model server's OpenAI-compatible base, without `/v1` |
| `models` | everything the server lists | the models you offer (a list) |
| `max_tokens` | `2048` | the most one reply may be (at most 32768) |
| `timeout_s` | `180` | how long one reply may take (at most 900) |
| `offer` | off | the network offer, below |

## Offer to the network

Your machine can serve other people's OpenVibe requests, and earn credits for it. It is off until you turn it on, and
it is opt-in for the caller too: only a request that asks for community capacity is ever placed here, because your
machine sees the prompt. Every such answer is labelled as coming from a community machine.

```json
{
  "plugins": [{
    "name": "ai",
    "offer": {
      "enabled": true,
      "models": ["llama3.1:8b"],
      "hours": { "start": "18:00", "end": "23:30", "tz": "Europe/Lisbon" },
      "concurrency": 2,
      "price": { "in_per_million": 0, "out_per_million": 0 },
      "audience": "everyone",
      "idle_only": true,
      "quiet_minutes": 5
    }
  }]
}
```

| key | default | meaning |
|---|---|---|
| `enabled` | `false` | offer this machine's models to the network |
| `models` | the models you serve | which of them may be used; the router only sends one it sees here |
| `hours` | all day | `{start, end, tz}` in 24-hour local time; an overnight window wraps, and start == end is all day |
| `concurrency` | `1` | how many replies may run at once, 1-8; another is refused as busy, never queued |
| `price` | `0` / `0` | credits per million input and output tokens; `0` volunteers the model |
| `audience` | `everyone` | `everyone`, `followers` (the owner's channel followers) or `group` (with a `group` id) |
| `idle_only` | `true` | pause the offer while the machine is in use (GPU busy, a game/OBS/Steam process, or you at the keyboard) |
| `quiet_minutes` | `5` | how long it must be quiet before the offer resumes |
| `busy_gpu_percent` | `20` | GPU utilization at or over this is "in use" |
| `busy_processes` | OBS, Steam, Lutris, Heroic, overlays | process names that pause the offer |

The Node enforces `enabled`, `hours`, `models`, `concurrency` and idle-only on the machine. The router (OpenVibe.AI)
enforces `audience`, because only it can resolve a requester's followers or group. Before any traffic, the router runs
canary prompts with known answers and checks the model name the local server reports against the one you declared;
about 1% of answers are re-run on another trusted machine with the same model and compared. A new machine serves the
free tier only until it passes. Failed verification stops traffic to it.

**Idle-only.** While you game, stream or sit at the keyboard, the offer pauses and community requests are refused;
after `quiet_minutes` with no signal it resumes. The plugin reads what each OS offers: NVIDIA/AMD GPU utilization
(`nvidia-smi`/`rocm-smi`), the process list (`ps` or `tasklist`) for the names in `busy_processes`, and keyboard/mouse
idle (`xprintidle` on X11, `HIDIdleTime` on macOS, `GetLastInputInfo` on Windows). A probe that is not available is
never treated as busy.

**Privacy, said plainly.** A community machine sees the prompt. Only requests that allow community capacity are sent
here, and every answer is labelled Community. The Node advertises that sentence (`ai.offer.privacy`) and
openvibe.bot/machines shows it when the offer is on.

`status.capabilities.ai` advertises the offer as `ai.offer` (`enabled`, `models`, `hours`, `concurrency`, `price`,
`audience`, `idle_only`, `quiet_minutes`, `open_now`, `paused`, `privacy`, `ready`) and per-model speed as `ai.speed`
(tokens a second, for the seller card) so Bot and Network can list it. It also reports `ai_community_requests`,
`ai_community_tokens_in` and `ai_community_tokens_out` once it has served something. Credits are recorded by the
router as pending until the requester is charged; a volunteer (price 0) earns kudos instead, which never expire and
buy queue priority and a bigger free limit, never cash. Cash payouts are a separate, later switch.

## Where your models answer

Once "Let my AI use this computer's models" is on for the machine, on openvibe.bot/machines:

- **OpenVibe.Chat.** Under "Your AI key" in your settings, choose "A model on my own computer". The machine card's
  "Use these models in OpenVibe.Chat" fills it in. Your Vibe (tick "Vibe thinks with it too") and the AI members you
  set up in rooms then think with it.
- **OpenVibe.Live.** A streamer's AI chat viewers: Dashboard, Chat AI, AI Chat Viewers, Key & Budget, AI source "A
  model on my own computer (free)".
- **Any of your computers.** Choose "any" instead of one machine. OpenVibe asks the fastest of yours that is on and
  has the model (by `ai_tokens_per_s`), then the next when one is busy. It is never anyone else's machine.
- **Your own code.** `openvibe-sdk/bot`: `bot.machines.chat(id, { model, messages })` asks one of the machine's
  models as you (SDK 0.67.0).

When the machine is off or its model server is down, the request fails and says so. Nothing else answers in its place.

## The command

`ai` `{"model", "messages": [{"role": "system"|"user"|"assistant", "content"}], "max_tokens"?, "temperature"?}` →
`{model, message: {role: "assistant", content}, usage?, finish_reason?, ms}`.

- At most 200 messages of 64 KB each. A model the machine does not offer is refused (`bad_value`), and so is a request
  while another runs (`not_ready`).
- When no model server answers, the command is `not_ready` and `describe` says `ai.ready: false`.
- Its telemetry (Node v0.29.0) says whether a model is answering now (`ai_busy`). Once one has answered, it also gives
  how fast the last model answered here (`ai_model`, `ai_tokens_per_s`): the reply's tokens over the whole answer's
  time, averaged with the earlier answers, the latest counting most. openvibe.bot/machines shows it beside the models.
- It never acts on the machine, so it also runs while the machine's stop is latched.
