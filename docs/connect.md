# OpenVibe Connect from the Node

[OpenVibe Connect](https://connect.openvibe.website) puts one port on your computer on the web at its own address,
`https://<name>--<you>.openvibe.website`, and only the people you pick can open it, after they sign in with OpenVibe.
The Node speaks Connect's protocol itself (`internal/connect`, `ovconnect-ws/1`), so a share can stay up as part of
the Node service, across reboots, without the one-file client.

## A share this computer serves, with no key

A computer added on [openvibe.bot/machines](https://openvibe.bot/machines) has its own OpenVibe sign-in (its node
principal). On a share's page on connect.openvibe.website, pick it under "Or serve it from one of your computers",
then on the computer:

```sh
openvibe-link share 8790                          # serve the share this computer was picked for on that port
openvibe-link share mine                          # the shares this computer was picked for
openvibe-link share --share shr_…                 # serve one now, until Ctrl-C
openvibe-link share --share shr_… --port 8790     # the same, with the port named
sudo openvibe-link share add --share shr_…        # or keep it up with the Node service
sudo openvibe-link share add 8790                 # the same, found by its local port
```

There is no key to copy or leak. Before every connection the Node buys its node token for OpenVibe Connect (audience
`openvibe.website`, 5 minutes) and asks Connect for a tunnel token for that one share (60 seconds, one use), and says
hello with both (ADR-057 decision 4, CON-4). While connected it sends a fresh node token every 45 seconds. A computer
removed on OpenVibe stops serving within about five minutes, and the Node stops that share instead of retrying.

Without `--port`, the Node serves the port the share's page names when you run or add it, and keeps that port: if the
page names another one later, the Node keeps serving the port it was given and says so in the log. Connect can never
point it at a different local service. Pass `--port N` to choose the port yourself.

## Make a share

Sign in at connect.openvibe.website, give the share a name and the port, and pick who can open it. The page shows its
connect key once (`ovc_…`). Keep it like a password: it lets a computer serve that share.

## Serve it now

```sh
openvibe-link share --key ovc_… --port 8790
```

It prints the address, then one line per request, until Ctrl-C. The key can also come from `OV_CONNECT_KEY`, which
keeps it off the command line and out of your shell history:

```sh
OV_CONNECT_KEY=ovc_… openvibe-link share --port 8790
```

## Keep it up with the Node service

```sh
openvibe-link share add --key ovc_… --port 8790
openvibe-link share list
openvibe-link share remove 8790        # or the key, or the address
```

`openvibe-link status` lists every share the service keeps up: its local port, its address on Connect, whether the
tunnel is connected (and why not), and how many requests the app has answered. A share with no address yet says so.

The saved shares are in the Node's config directory, `connect-shares.json`, readable only by the Node's account
(0600). The service starts every saved share when it starts, and stops every one when the Node stops; restart it after
`add` or `remove`. A share the server ends for good (its key was replaced, it was closed, or another computer took it
over) stays stopped and says so in the log; `share list` shows each share's address once it has connected.

## What the Node does and does not do

- It opens one outgoing WebSocket per share to `wss://connect.openvibe.website/tunnel` (port 443, through Cloudflare),
  so it works behind any router, firewall or phone hotspot, with no open ports.
- It only ever connects to `localhost` on the share's own port. The server cannot make it reach anything else.
- Requests stream both ways with backpressure; server-sent events stream; WebSocket upgrades become a byte pipe.
- It reconnects after a drop with backoff from 1 to 30 seconds, and the server pings every 25 seconds.
- Your app sees `Host: localhost:<port>`, the share's address in `X-Forwarded-Host`, and who the visitor is in
  `X-OpenVibe-User` and `X-OpenVibe-Subject`. It never sees the gate's own cookie.

Who may open a share, public shares and the limits are Connect's: see connect.openvibe.website and
[OpenVibe.Connect's README](https://github.com/OpenVibers/OpenVibe.Connect).
