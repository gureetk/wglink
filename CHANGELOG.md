# Changelog

Notable changes to wglink. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [0.3.0] - 2026-11-17

Build networks from the dashboard: add nodes and connect them there, then
plan and apply.

### Added

- The dashboard adds nodes and connects them. `n` opens a form for a new
  node, and `c` one that joins checked nodes in a new or existing network,
  then shows its plan to apply. Both make the same changes as
  `wglink node add` and `wglink connect`.

### Fixed

- `wglink connect` and `wglink net join` no longer reset the pinned
  address of a node that is already a member.

## [0.2.0] - 2026-11-16

Reach more machines and shape networks more ways: Proxmox VMs, phones, hubs,
routed subnets and IPv6, with key rotation, preshared keys, safer changes
over SSH and a terminal dashboard.

### Added

- Proxmox VMs as nodes, through the QEMU guest agent: `wglink node add vm200
  --qm 200 --via pve1`. The VM needs `qemu-guest-agent` and no SSH.
- Manual nodes for devices wglink can't reach, such as phones:
  - `wglink node add phone --manual` adds one.
  - `wglink export lab phone` prints its config and makes its key pair on
    the first run.
  - `plan` says when a device's config is out of date, and `status` judges
    its links from the other end.
- Safe changes over SSH:
  - Before changing an SSH node, `apply` arms a systemd timer there that
    restores the previous config, then cancels it over a new SSH connection.
  - If the change cut off the route wglink uses, the node undoes the change
    by itself after a minute.
  - `--no-revert` turns this off, and `plan` warns about SSH nodes without
    systemd.
- `wglink discover HOST` lists a Proxmox host's containers and VMs with their
  addresses. `--add` adds the running ones as nodes.
- `wglink state rebuild` recovers a lost or stale `wglink.state.json` from
  the configs on the nodes. It keeps the old file as `.bak`.
- Hub-and-spoke networks:
  - `--hub NODE` on `connect`, `net create` and `net set`.
  - Spokes connect only to the hub and send the whole network through it.
  - The hub turns on forwarding for its WireGuard interface alone.
  - `status` shows the hub's links.
- Routed subnets:
  - `wglink routes NETWORK NODE SUBNET...` lets the other members reach a
    member's LAN through it, through the hub in hub networks.
  - Auto subnets avoid routed ones.
  - `plan` stops you when a member is on a routed subnet itself, and warns
    when the routing member doesn't forward.
- `wglink rotate NETWORK [NODE...]` replaces members' keys:
  - New keys are made on the members and loaded live on them and their
    peers.
  - The probe now compares the key file with the keys in the config and in
    the running interface. `plan` loads a new key that hasn't been picked up,
    for example after an interrupted rotation.
- Preshared keys, with `--psk` on `connect`, `net create` and `net set`:
  - `apply` makes a key for each pair of peers and stores it on both before
    either config uses it.
  - Nodes report keys by their hashes, so `plan` finds pairs that don't
    match and keys that aren't loaded yet.
  - `rotate` replaces a member's preshared keys too, and `export` gives a
    phone new ones.
- IPv6 inside the tunnels, with `--ipv6` on `connect`, `net create` and
  `net set`:
  - A random unique local /64 by default, or a fixed subnet.
  - Each member's IPv6 address has the same host number as its IPv4 one.
  - Hubs forward IPv6 for their interface alone on Linux 6.17 and later;
    `plan` warns when an older hub forwards no IPv6.
  - Routed subnets can be IPv6 too.
- `wglink ui`, a terminal dashboard: every network with the health of its
  links, checked again every few seconds, with plan and apply for the
  selected network a key away.

### Fixed

- A config edited by hand without a final newline no longer breaks `plan`
  and `status` for its node.
- Turning a network's keepalive off now takes effect on a live update, not
  only after the interface restarts. `wg syncconf` keeps settings a config
  leaves out, so wglink sets them to zero.

## [0.1.0] - 2026-11-16

The first release: describe machines and networks, preview, apply, and check
that tunnels pass traffic.

### Added

- `wglink.yaml` inventory of nodes and networks. Edit it by hand, or with
  `init`, `node`, `net`, `connect` and `ip`, which keep comments and layout.
- Four ways to reach a node:
  - SSH, using the system OpenSSH client with connections reused across commands.
  - Local execution.
  - Any exec prefix, such as `docker exec -i`.
  - Proxmox containers, through `pct exec` on their host.
- Automatic subnets, ports and addresses that stay the same across runs.
  Any of them can be pinned: a host part (`.10`), a full address, a subnet or
  a port. Picks avoid addresses, routes and UDP ports already in use on each
  node.
- `plan` and `apply`:
  - Private keys are made on the nodes that use them and never leave them.
  - Configs are written for wg-quick, and systemd is used where present.
  - Peer changes load live with `wg syncconf`.
  - Edits made on a node are detected.
  - Configs wglink didn't write are left alone unless `--adopt` is given.
- `status`:
  - Health of each link from handshakes and pings through the tunnel, with
    traffic counters and drift.
  - `--json` output and `--exit-code` for scripts.
  - `--local` checks a node by itself, without an inventory.
- Tests:
  - Node scripts are tested under dash, bash and busybox sh.
  - End-to-end tests run against real WireGuard in network namespaces, over
    exec, SSH and systemd.

[0.3.0]: https://github.com/gureetk/wglink/releases/tag/v0.3.0
[0.2.0]: https://github.com/gureetk/wglink/releases/tag/v0.2.0
[0.1.0]: https://github.com/gureetk/wglink/releases/tag/v0.1.0
