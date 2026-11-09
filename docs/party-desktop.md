# Host a party desktop with Link

Open [openvibe.party](https://openvibe.party), sign in, and choose **Start a shared computer → On my computer**. The party page gives you a command containing its party ID:

```sh
openvibe-link party host party-0123abcd
```

Use the ID from your own page. Link must already be paired to your OpenVibe account. Install Docker or Podman on this computer before starting. Link pulls a pinned neko Google Chrome image, runs a temporary container with a private loopback HTTP port, and opens a party-only Connect share. The browser inside the container is separate from your own browser and files. A service sign-in happens inside that shared browser, so everyone watching the desktop can see what you type there. Sign out of streaming services before ending the party if you want to clear their sessions immediately.

When the Link service is running, the command starts hosting in the background. Open **Link** from the tray or run `openvibe-link panel` to see **Hosting a party desktop**. Select **Stop** there, or end the party on openvibe.party. If Link is not running as a service, the command hosts in the foreground; press Ctrl-C to stop. Stopping removes the container and closes the Connect share.

The party's existing remote decides who can drive. Members use **Ask for it**, **Pass it**, and **Take it back** on openvibe.party. The shared browser is framed in the party page, and members must sign in to OpenVibe before they can watch. **Listen** uses the same browser for Spotify or YouTube Music.

For operators: Chat requires `TURN_URL=turns:<host>:443` with `TURN_AUTH_SECRET`, and the TURN service must accept TLS on port 443. Chat's `CHAT_PARTY_CONNECT_SECRET` and Connect's `CONNECT_PARTY_SHARED_SECRET` must be the same random value of at least 32 characters. The Link needs Network-issued node tokens for `openvibe.chat` and `openvibe.link`.
