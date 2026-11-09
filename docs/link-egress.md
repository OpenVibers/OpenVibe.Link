# Browsing through your own Link

Link automatically offers an authenticated browsing endpoint for its owner's cloud sessions after pairing.
The switch, offline choice, monthly limit (20 GB by default) and site exceptions are on openvibe.link.
Only public web destinations on TCP ports 80 and 443 are allowed. DNS is resolved and pinned on the Link device;
private, local, metadata and IPv6 translation addresses are refused.

Connect carries a WebSocket byte stream through its existing share relay. The stream contains HTTP CONNECT,
ordinary HTTP proxy requests, or authenticated SOCKS5 CONNECT. Both Connect and Link ask Network about every
new stream. Network compares the current device owner with the Run session owner, checks the credential hash,
expiry, opt-out and cap. A cookie or an ordinary share API key never grants access to the browsing endpoint.
Link checks active streams again every five seconds, closing them on expiry, revocation, opt-out or a reported
battery/metered state. OS readings are best effort: Linux battery/NetworkManager, macOS battery, Windows battery/network cost.

The shared desk plugin adds only the desk name to `desk-sandbox`. The Link authenticates to Network, reads the
session assigned to its own machine, and gets a fresh credential kept in its proxy process. Chrome never receives
that credential. Its namespace reaches one gateway proxy socket only; all direct forwarding is refused. Chrome
also disables direct DNS, QUIC and non-proxied WebRTC UDP, with the loopback bypass disabled.

The device persists bandwidth reservations before forwarding bytes, shared across its sessions and directions.
Reservations may overcount after a failed write or crash; restarting Link does not reopen the monthly allowance.
The allowance is per Link device. A damaged usage file refuses service until repaired by the owner.

Run must renew authorization within two minutes and revoke it when ending the desk. A missing or unavailable
Network never means direct fallback. The saved offline choice applies when the Link connection cannot be made:
use OpenVibe's address or wait. Explicit site exceptions use OpenVibe's address and public-only DNS there.

Deployment order: Network's expand migration and routes, Connect's expand migration and route, Link including
the updated computer plugin on cloud desk hosts and home devices, then Run. Existing cloud hosts must be upgraded
before enabling Run's integration. The integration applies to browser desks created through Run; providers must
use that session path to inherit the policy. No blind-fill files are modified.
