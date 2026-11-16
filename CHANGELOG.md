# Changelog

Notable changes to wglink. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[0.1.0]: https://github.com/gureetk/wglink/releases/tag/v0.1.0
