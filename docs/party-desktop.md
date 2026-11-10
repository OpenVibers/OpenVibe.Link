# Host a party desktop with Link

Open [openvibe.party](https://openvibe.party), sign in, and choose **Start a shared computer → On my computer**. The party page gives you a command containing its party ID:

```sh
openvibe-link party host party-0123abcd
```

Use the ID from your own page. Link must already be paired to your OpenVibe account. Install Docker or Podman on this computer before starting. Link pulls a pinned neko Google Chrome image, runs a temporary container with a private loopback HTTP port, and opens a party-only Connect share. The browser inside the container is separate from your own browser and files. A service sign-in happens inside that shared browser, so everyone watching the desktop can see what you type there. Sign out of streaming services before ending the party if you want to clear their sessions immediately.

When the Link service is running, the command starts hosting in the background. Open **Link** from the tray or run `openvibe-link panel` to see **Hosting a party desktop**. Select **Stop** there, or end the party on openvibe.party. If Link is not running as a service, the command hosts in the foreground; press Ctrl-C to stop. Stopping removes the container and closes the Connect share.

The party's existing remote decides who can drive. Members use **Ask for it**, **Pass it**, and **Take it back** on openvibe.party. The shared browser is framed in the party page, and members must sign in to OpenVibe before they can watch. **Listen** uses the same browser for Spotify or YouTube Music.

For operators: Chat requires `TURN_URL=turns:<host>:443` with `TURN_AUTH_SECRET`, and the TURN service must accept TLS on port 443. Chat's `CHAT_PARTY_CONNECT_SECRET` and Connect's `CONNECT_PARTY_SHARED_SECRET` must be the same random value of at least 32 characters. The Link needs Network-issued node tokens for `openvibe.chat` and `openvibe.link`.

## OpenVibe's own computer pool

A machine can also serve parties from OpenVibe's own pool, with no person at it to run a command. The owner registers
the machine on [openvibe.party/pool](https://openvibe.party/pool) with its Link principal (`lnk_…`), a name, and how
many desktops it may host at once. On the machine, the Link service offers itself with:

```json
{
  "party_pool": { "enabled": true }
}
```

It then asks Chat every five seconds, with the machine's own `openvibe.chat` node token, what the pool is holding for
it (`GET /api/chat/parties/desktop/assignment`). An answer starts the same host path as `openvibe-link party host`: the
machine claims the party with its node token, opens its Connect share, reports ready, and renews by polling the
party's `access` while it hosts. The poll is also how the pool sees the machine is alive. `party_pool.party_url` is
only for a test copy of the site (an https origin, or loopback http).

The owner's switch on the pool panel is off by default; turning it off stops new parties being placed there. A hold the
machine never claims, or stops renewing, is released after the idle window, and the party page says the computer was
released.
