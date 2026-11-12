# Your own models for your OpenVibe work

If you already run a model at home (Ollama, LM Studio or llama.cpp's server on a gaming PC or a spare box with a GPU),
OpenVibe can use it for your own work: Vibe's answers, an agent's thinking, a summary. It costs nothing, and the prompt
never leaves your network. The machine opens no port: its OpenVibe Link already connects out to OpenVibe.Bot, and the
request comes back over that connection.

## How it works

1. OpenVibe.AI, on your behalf, asks OpenVibe.Bot to run one chat completion on your machine (`bot.command@1` kind
   `ai`, contracts 0.219.0).
2. Bot sends it to your Link, but only for your own machine, and only when you turned on **Let my AI use this
   computer's models** on openvibe.link/machines. One person's request never goes to another person's machine.
3. The Link's `ai` plugin relays it to your model server, and the reply goes back the same way.

The plugin reaches one place only: the model server in its config. It reads nothing else on the machine and changes
nothing there. It runs one request at a time, because a model is heavy; another request is refused as busy, not
queued. The audit on Bot keeps the model and the token counts, never the text.

## Set it up

The installer does it with `--ai` (Link v0.28.0), added to the command openvibe.link/machines gives you or to an
install you already have. It says which model server answers, or how to get one:

```sh
curl -fsSL https://openvibe.link/install | sh -s -- --network … --pairing pair_… --code … --ai
```

`--ai-endpoint http://127.0.0.1:8090` names a server on another port (it implies `--ai`). `--ai` works with `--worker`,
`--computer` and `--desk-host` too, and a driver chosen later keeps it.

By hand, add the plugin to the Link's config (`config.json`):

```json
{ "plugins": [{ "name": "ai" }] }
```

With nothing else set, it uses the first of these that answers:

| Server | Address |
|---|---|
| Ollama | `http://127.0.0.1:11434` |
| LM Studio | `http://127.0.0.1:1234` |
| llama.cpp's server | `http://127.0.0.1:8080` |

It offers every model the server lists. Restart the Link (`openvibe-link restart`), then turn on **Let my AI use this
computer's models** for the machine on openvibe.link/machines.

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

The Link enforces `enabled`, `hours`, `models`, `concurrency` and idle-only on the machine. The router (OpenVibe.AI)
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
here, and every answer is labelled Community. The Link advertises that sentence (`ai.offer.privacy`) and
openvibe.link/machines shows it when the offer is on.

`status.capabilities.ai` advertises the offer as `ai.offer` (`enabled`, `models`, `hours`, `concurrency`, `price`,
`audience`, `idle_only`, `quiet_minutes`, `open_now`, `paused`, `privacy`, `ready`) and per-model speed as `ai.speed`
(tokens a second, for the seller card) so Bot and Network can list it. It also reports `ai_community_requests`,
`ai_community_tokens_in` and `ai_community_tokens_out` once it has served something. Credits are recorded by the
router as pending until the requester is charged; a volunteer (price 0) earns kudos instead, which never expire and
buy queue priority and a bigger free limit, never cash. Cash payouts are a separate, later switch.

## Where your models answer

Once "Let my AI use this computer's models" is on for the machine, on openvibe.link/machines:

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
- Its telemetry (Link v0.29.0) says whether a model is answering now (`ai_busy`). Once one has answered, it also gives
  how fast the last model answered here (`ai_model`, `ai_tokens_per_s`): the reply's tokens over the whole answer's
  time, averaged with the earlier answers, the latest counting most. openvibe.link/machines shows it beside the models.
- It never acts on the machine, so it also runs while the machine's stop is latched.

## Machine pools (Linux)

Several of your machines on the same local network can serve one model together, as one offer: a gaming PC and a
laptop, or a few servers in one rack (the pooled machines design, 2026-11-06). The engine is llama.cpp's RPC backend:
`rpc-server` on every member and `llama-server --rpc` on the head, which sees the prompt and answers; the members
lend their memory and only receive tensors. It runs on any GPU llama.cpp supports, and on the CPU. Pools across the
internet, and pools of machines that are not all yours, are not possible.

llama.cpp's RPC has no authentication of its own, so the plugin never exposes it:

- **The engines run in their own network namespace** (an unprivileged Linux user and network namespace with only
  loopback). `rpc-server` and `llama-server` cannot be reached from the local network or from the host's own
  loopback; a private Unix socket (mode 0600) is their only way in or out. The worker refuses to start on the host's
  network, and it dies with the plugin.
- **The private link** is the only door to a member's `rpc-server`: TLS 1.3 on the member's private address (an RFC
  1918 address configured on one of its interfaces; never 0.0.0.0, loopback or a public address), with mutual
  certificate pinning. A member accepts only the pool head's certificate; the head accepts only each member's. Each
  machine makes its own Ed25519 key when the person at it approves the pool; only the public certificate goes to
  Network. The link carries tensors, status, run and pause, and the link probe, nothing else: no machine can run
  anything on another.
- **Leaving drops the link and the key at once**: `pool_cli leave`, the owner's Stop pool on openvibe.network,
  removing a machine, signing it out or pairing it to someone else. Every machine renews its readiness with Network
  every two seconds and sees the change there; the pool needs every machine to approve again.
- **Idle-only and the kill switch hold for the whole pool.** The head asks every member every half second; any member
  that is in use (a game, OBS, the person at the keyboard, another program on the GPU; the pool's own GPU use does not
  count), outside its hours, offline or stopped by its kill switch pauses the whole pool, and a member the head stops
  calling pauses itself after three seconds. The kill switch (an stop of any machine) holds the pool until
  its latch is cleared. Network marks the pool's offer not ready when any machine misses a renewal for ten seconds.

### Setting one up

You need Linux on every machine, `bubblewrap` (recommended) or `unshare` and `ip` (util-linux and iproute2), OpenSSL,
and llama.cpp built with `GGML_RPC=ON` (the tested revision is `bcb0152876e8f0b684ba70d3defcab6cdf35adb5`; its RPC
server is `ggml-rpc-server`, `rpc-server` in older releases). Each machine must be paired through OpenVibe.Network.
The engine runs in a private network namespace: bubblewrap is tried first and works on a default Ubuntu 24.04 even
though `kernel.apparmor_restrict_unprivileged_userns=1`; without bubblewrap, `unshare` needs unprivileged user
namespaces allowed (`kernel.apparmor_restrict_unprivileged_userns=0` on Ubuntu 24.04). If neither can make a
namespace, pooling refuses instead of using the host network and says: "Pooling needs an isolated network for the
engine. Install bubblewrap (sudo apt install bubblewrap) and try again."

1. On every machine, give the ai plugin a pool configuration:

   ```json
   {
     "pool_directory": "/home/me/.local/share/openvibe-link/pool",
     "pool_credential_file": "/home/me/.config/openvibe-link/credential.json",
     "pool_address": "192.168.1.2/24",
     "pool_engine": {
       "rpc_binary": "/opt/llama.cpp/bin/ggml-rpc-server",
       "llama_binary": "/opt/llama.cpp/bin/llama-server",
       "model_path": "/models/llama-3.3-70b-instruct-q4_k_m.gguf",
       "context": 4096,
       "model": {"model": "llama-3.3-70b-instruct-q4_k_m", "parameters_b": 70, "memory_mb": 45000}
     },
     "offer": {"enabled": true, "price": {"in_per_million": 0.2, "out_per_million": 0.6}}
   }
   ```

   `pool_address` defaults to the address of the default route; `pool_port` defaults to 50053. `memory_mb` is what the
   model needs, weights and context. Every machine announces itself to Network every minute until it is in a pool
   (or once by hand: `python -m openvibe_ai.pool_cli announce --credential-file … --directory …`).
2. On openvibe.link/machines, "Pool these machines" lists your machines that checked in from the same local network
   (one public address, one private subnet). Choose two to eight; the head defaults to the one with the most free
   VRAM.
3. On **each** machine, approve it:

   ```sh
   python -m openvibe_ai.pool_cli approve --credential-file /home/me/.config/openvibe-link/credential.json \
       --directory /home/me/.local/share/openvibe-link/pool --pool pool_…
   ```

   It shows the owner, the head and the members, says what the head sees, and needs the word `approve`. The key stays
   on the machine; its certificate is valid for 30 days, after which the pool is made again.
4. When every machine has approved, the head measures each link (three round trips under 5 ms; at least 300 Mb/s, or
   1 Gb/s for a model over 30B) and checks the pool's free memory against the model, then starts. The offer appears
   as one offer, labelled "Community pool (N machines)", with one price for the pool. Before choosing a model,
   `pool_cli fit --models models.json [--benchmarks measured.json]` on the head prints the links and what fits; a speed
   is shown only from a dated, sourced measurement on the same hardware, never a guess.

A pool is never edited: another head, member or key is a new pool, approved again on every machine. Leave from any
machine, even offline: `python -m openvibe_ai.pool_cli leave --credential-file … --directory … --pool pool_…` deletes
the key first, then tells Network, which stops the pool for every machine.

### Tests

`plugins/ai/tests/test_pool.py` checks the trust boundary with real TLS and real keys (only the head reaches
`rpc-server`, another member or a stranger is refused, plain connections are refused, no wildcard, loopback or public
listener, the probe refuses a machine off the network), the pool rules on two machines (the head starts only after
every member runs; idle-only on a member or the head pauses everyone; the kill switch stops the pool at once; a
silent head pauses its members; leaving or a revoked membership closes the link and deletes the key; closing keeps
it) and the plugin's stops. `test_pool_linux.py` runs the engines in real namespaces: the worker refuses the host
network, `rpc-server` is reachable only through its private socket, and two machines on a throwaway network pool and
serve through the tunnel with stand-in engines. The pool workflow (`.github/workflows/pool.yml`) builds llama.cpp at
the pinned revision and runs the same two-machine scenario with a real model.
