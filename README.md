# wglink

[![ci](https://github.com/gureetk/wglink/actions/workflows/ci.yml/badge.svg)](https://github.com/gureetk/wglink/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/gureetk/wglink)](https://github.com/gureetk/wglink/releases)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**Connect Linux machines over WireGuard from one inventory file.** List your
machines in `wglink.yaml`, preview the change with `wglink plan`, make it
with `wglink apply`, and check each tunnel with `wglink status`.

wglink is one static binary with no agent, no coordination server and no
account. It reaches machines the way you already do: SSH, local commands,
`docker exec`, and Proxmox containers and VMs. What it leaves behind are
ordinary `wg-quick` configs that keep working without it. Private keys are
made on the machines that use them and never leave.

```sh
wglink connect pve1 nas pi --name lab
wglink apply
```

```console
$ wglink status
network lab   wg-lab   10.99.0.0/24   udp/51820
  pve1  10.99.0.1       up
  nas   10.99.0.2       up
  pi    10.99.0.3       up

  pve1 <-> nas  ok  handshake 14s ago, ping 0.4 ms  rx 1.2 MiB  tx 880.0 KiB
  pve1 <-> pi   ok  idle, ping 1.1 ms
  nas <-> pi    ok  handshake 1m ago, ping 0.9 ms   rx 52.3 KiB  tx 61.0 KiB

All 3 links are ok.
```

## Features

- **Full-mesh or hub-and-spoke networks** between any Linux machines: bare
  metal, VMs, containers, Raspberry Pis, Proxmox hosts and their guests, plus
  phones and other devices you set up by hand.
- **Automatic addressing that stays put.** Subnets, ports and tunnel
  addresses are picked for you, kept across runs, and chosen to avoid
  addresses, routes and ports already in use on each machine. Pin any of them
  when you care: a host part (`.10`), a full address, a subnet or a port.
- **IPv6, preshared keys and key rotation.** Give any network a unique local
  IPv6 subnet and every pair of peers a preshared key, and replace keys with
  one command.
- **Plan before you apply.** Every change is previewed. Peer changes load
  live with `wg syncconf`; only address, port and MTU changes restart an
  interface.
- **Drift detection.** Edits made by hand on a machine show up in `plan` and
  `status`, and `apply` puts the file back.
- **Health at a glance.** `status` judges every link from handshakes and pings
  through the tunnel. It prints JSON for scripts, gives exit codes for
  monitoring, and has `--local` mode for checking one machine by itself.
- **Agentless.** wglink sends small POSIX `sh` scripts over stdin to the
  machines it manages. They need only `wireguard-tools`, and nothing of
  wglink's stays running on them.
- **Careful by default.** wglink won't overwrite configs it didn't write,
  keeps a backup of every config it replaces, and lets only one `apply` run
  at a time per inventory. A change that cuts wglink off from an SSH node is
  undone by the node itself a minute later.

## Install

Download a release for Linux on amd64, arm64 or armv7:

```sh
VERSION=0.1.0 ARCH=amd64
curl -fsSLO "https://github.com/gureetk/wglink/releases/download/v$VERSION/wglink_${VERSION}_linux_$ARCH.tar.gz"
curl -fsSLO "https://github.com/gureetk/wglink/releases/download/v$VERSION/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf "wglink_${VERSION}_linux_$ARCH.tar.gz"
sudo install "wglink_${VERSION}_linux_$ARCH/wglink" /usr/local/bin/
```

Or build it with Go 1.26 or newer:

```sh
go install github.com/gureetk/wglink/cmd/wglink@latest
```

wglink runs on one machine, the *controller*, which can be one of the nodes
too. Every node needs:

- Linux with `sh`, `ip` (iproute2) and `wireguard-tools` (`wg` and `wg-quick`).
- Root, or passwordless `sudo` for the user wglink connects as.
- The WireGuard kernel module, which Linux has included since 5.6, or
  `wireguard-go`.
- Optionally `ping` (for `status`) and systemd (so tunnels come back after a
  reboot).

## Quick start

```sh
wglink init                                         # writes wglink.yaml here
wglink node add pve1 --ssh root@192.168.1.10
wglink node add nas  --ssh admin@192.168.1.20 --sudo
wglink node add pi   --ssh pi@192.168.1.30 --sudo
wglink connect pve1 nas pi --name lab
wglink plan
```

`node add` connects once to check the machine and tells you if anything is
missing:

```console
$ wglink node add nas --ssh admin@192.168.1.20 --sudo
Added node nas.
nas is reachable (debian; wireguard-tools installed).
```

`plan` connects to every node, works out addresses and shows what would
change. Nothing is changed yet:

```console
$ wglink plan
network lab   wg-lab   10.99.0.0/24   udp/51820
  + pve1  10.99.0.1       new interface, 2 peers, new key
  + nas   10.99.0.2       new interface, 2 peers, new key
  + pi    10.99.0.3       new interface, 2 peers, new key

3 to create, 0 to change live, 0 to restart, 0 to remove.
Run "wglink apply" to make these changes.
```

`apply` shows the same plan, asks for confirmation, then makes the change:

```console
$ wglink apply
...
Apply these changes? [y/N] y

  pve1: created a key for wg-lab
  nas: created a key for wg-lab
  pi: created a key for wg-lab
  pve1: created wg-lab
  nas: created wg-lab
  pi: created wg-lab
Done.
```

Then `wglink status` checks every pair, as shown at the top of this page.
After that, change `wglink.yaml` with commands or by hand, and run `plan` and
`apply` again. For example, giving `pi` a fixed address restarts only `pi`'s
interface:

```console
$ wglink ip lab pi .50
$ wglink plan
network lab   wg-lab   10.99.0.0/24   udp/51820
  ~ pve1  10.99.0.1       peer pi: allowed IPs 10.99.0.3/32 -> 10.99.0.50/32 (live)
  ~ nas   10.99.0.2       peer pi: allowed IPs 10.99.0.3/32 -> 10.99.0.50/32 (live)
  ~ pi    10.99.0.50      address 10.99.0.3/24 -> 10.99.0.50/24 (restart)

0 to create, 2 to change live, 1 to restart, 0 to remove.
Run "wglink apply" to make these changes.
```

## Reaching machines

| How | Example | Notes |
| --- | --- | --- |
| SSH | `--ssh root@192.168.1.10`, `--ssh pi-garage` | Uses your OpenSSH client, keys and `~/.ssh/config`. Runs in batch mode, so it needs key-based login. Opens one connection per node and reuses it. |
| Local | `--local` | The machine wglink runs on. |
| Exec | `--exec "docker exec -i web"` | Any command that runs `sh -s` with stdin attached. |
| Proxmox container | `--pct 101 --via pve1` | Runs `pct exec` on the Proxmox host node `pve1`. |
| Proxmox VM | `--qm 200 --via pve1` | Runs commands through the QEMU guest agent (`qm guest exec`), so the VM needs `qemu-guest-agent` and no SSH. |

Add `--sudo` to SSH and local nodes to use `sudo -n` when the login isn't
root. wglink never asks for or stores a password.

On a Proxmox host that is already a node, `wglink discover` lists its
containers and VMs with their addresses. With `--add`, it adds the running
ones as nodes, reached through the host:

```console
$ wglink discover pve1 --add
VMID   TYPE   NAME             STATUS    ADDRESS         NODE
101    lxc    media            running   192.168.1.101   media (added)
102    lxc    test             stopped   -               -
200    qemu   home-assistant   running   192.168.1.200   home-assistant (added)
201    qemu   win11            running   -               QEMU guest agent is not running

Added media, home-assistant. Next, put them in a network: wglink net join NETWORK media home-assistant
Left out win11 201 (QEMU guest agent is not running); add it by hand, for example with --ssh.
```

Peers dial each node at its **endpoint**. For SSH nodes the endpoint is the
host that SSH connects to (as `ssh -G` resolves it), so most nodes need
nothing more. Other nodes need `--endpoint ADDRESS`. A machine behind NAT that
nobody can dial gets `--endpoint none --keepalive 25`: it dials out to the
others and keeps the path open.

## Phones and other devices

Some peers are devices wglink can't reach, such as phones, tablets or a
friend's laptop. Add them as **manual** nodes. wglink gives them an address
and adds them to every other member's config, and you load their own config
on the device:

```sh
wglink node add phone --manual --keepalive 25
wglink net join lab phone
wglink export lab phone -o phone.conf    # makes the phone's key pair
qrencode -t ansiutf8 < phone.conf        # scan it with the WireGuard app
wglink apply                             # the other members accept the phone
```

The phone's private key is only in that exported config; wglink keeps just
the public key. If the device already has a key, pass its public key with
`--pubkey` instead. When the phone's config changes, for example because a
member joined or moved, `wglink plan` says so and `wglink export` gives the
new one.

## Hub and spoke

By default every member of a network connects to every other. With a hub,
members connect only to the hub, which passes traffic between them. This
helps when only one machine can be reached from everywhere, such as a VPS,
or for laptops and phones on the road:

```sh
wglink connect vps laptop phone --name road --hub vps
```

Spokes send the whole network's traffic to the hub. The hub turns on
forwarding for its WireGuard interface alone when that comes up, through a
`PostUp` line in its config, so nothing else on the machine changes.

## Routed subnets

A member can carry traffic for a subnet behind it, such as its LAN, so the
other members can reach machines that don't run WireGuard:

```sh
wglink routes lab nas 192.168.50.0/24
```

The other members then send 192.168.50.0/24 through `nas`. For that to work:
- `nas` needs IP forwarding on. `plan` warns if it's off.
- Hosts on that LAN need a route back to the network through `nas`, for
  example on the router.
- No other member can be on that subnet itself, since it couldn't also route
  it through `nas`. `plan` stops you if one is.

In `wglink.yaml` the routes live on the member:
`nas: {routes: [192.168.50.0/24]}`.

## IPv6

Networks carry IPv4 by default. Add IPv6 inside the tunnels with `--ipv6`:

```sh
wglink connect pve1 nas vps --name lab --ipv6       # a random unique local /64
wglink net set lab --ipv6=fd12:3456:789a::/64       # or a fixed subnet
```

Each member gets the IPv6 address with the same host number as its IPv4
one, so `10.99.0.5` goes with `fd12:3456:789a::5`. The random subnet follows
RFC 4193, so networks set up apart won't clash if you join them later. Like
the IPv4 subnet, it's recorded at the first apply and kept.

In a hub network, the hub forwards IPv6 for its WireGuard interface alone on
Linux 6.17 and later. Older kernels forward IPv6 only with it on for the
whole machine (`net.ipv6.conf.all.forwarding=1`), and `plan` warns until it
is. Endpoints can be IPv6 addresses too.

## Preshared keys

WireGuard can mix a preshared key into each pair's handshake on top of the
key pairs, a second layer that guards against a future break of Curve25519.
Turn it on for a network:

```sh
wglink connect pve1 nas vps --name lab --psk
wglink net set lab --psk          # or for an existing network
```

`apply` then makes a key for every pair of peers and stores it on both, as
`/etc/wireguard/IFACE.psk.PEER` with mode 0600, before either config uses
it. `plan` compares keys by their SHA-256 hashes, so it can tell when a pair
doesn't match, or a new key isn't loaded yet, without the keys leaving the
nodes. `wglink rotate` replaces a member's preshared keys along with its key
pair. For a phone, each `wglink export` makes new keys: its peers store
theirs, and the config carries the phone's.

## The inventory

The commands above edit `wglink.yaml` for you, keeping its comments and
layout. You can also edit it by hand. A typical file:

```yaml
version: 1

defaults:
  pool: 10.99.0.0/16 # automatic subnets come from here
  prefix: 24 # size of each automatic subnet
  port: 51820 # first listen port tried

nodes:
  pve1:
    ssh: root@192.168.1.10
  nas:
    ssh: admin@nas.lan
    sudo: true
    endpoint: 192.168.1.20
  ct101:
    pct: {via: pve1, vmid: 101}
    endpoint: 192.168.1.101
  laptop:
    local: true
    endpoint: none
    keepalive: 25

networks:
  lab:
    members:
      pve1: auto
      nas: ".10" # pinned: host 10 of the subnet
      ct101: auto
  laptop-nas:
    members: [laptop, nas]
```

Each network becomes one WireGuard interface on each member (`wg-lab` here)
with every other member as a peer. A node can be in several networks.

Network settings, all optional:

| Setting | Default | |
| --- | --- | --- |
| `subnet` | the first free `/24` in the pool | IPv4 subnet for tunnel addresses |
| `port` | the first port from 51820 free on every member | UDP listen port |
| `iface` | `wg-NAME` | interface name |
| `mtu` | chosen by wg-quick | |
| `keepalive` | off | seconds between keepalives, for members behind NAT |
| `hub` | none: a full mesh | member the others connect through |
| `psk` | off | a preshared key for each pair of peers |
| `ipv6` | off | `auto` for a random unique local /64, or a subnet |

What wglink picks is recorded in `wglink.state.json` next to the inventory, so
addresses don't move when you add members later. The state file holds public
keys, addresses and hashes, and no secrets. Keep both files together, for
example in a private git repository. If the state file is lost,
`wglink state rebuild` recovers it from the configs on the nodes.

## Status

`wglink status` asks every member for its live WireGuard state and pings each
peer through the tunnel. Each pair gets one of these states:

| State | Meaning |
| --- | --- |
| `ok` | A handshake in the last 3 minutes, or a ping reply through the tunnel. |
| `idle` | No recent handshake, and `--no-ping` was given, so it wasn't tested. |
| `down` | Both ends are up, but nothing gets through. |
| `missing` | An end isn't applied yet, or its interface is down. |
| `unknown` | Neither end could be reached. |

One reachable end is enough to judge a link. Handshake ages are measured with
the clock of the node that saw them, so clock differences between machines
don't skew the result.

For scripts and monitoring:

```sh
wglink status --exit-code     # exits 2 unless every link is ok
wglink status --json          # stable JSON: states, handshake ages, round trips, bytes
wglink status --net lab       # only some networks
```

On a node, `sudo wglink status --local` shows that machine's WireGuard
interfaces without needing the inventory. Peers are named from the comments
wglink writes in its configs. It also runs when no `wglink.yaml` is found.

## How it works

1. **Probe.** For `plan`, wglink connects to every node in parallel and runs
   a read-only script that reports addresses, routes, UDP ports in use,
   WireGuard interfaces and their configs, with private keys removed.
2. **Resolve.** Pinned values come first, then values recorded from earlier
   runs, then new picks that avoid everything the nodes reported.
3. **Diff.** wglink renders each member's `wg-quick` config and compares it
   with what's on the node. Peer changes can load live. Address, port and MTU
   changes need a restart. A config whose hash no longer matches the last
   apply was edited by hand.
4. **Apply.** First, missing keys are created on their nodes with
   `wg genkey`. Only public keys come back. Then each config is sent without
   its private key, and the node inserts the key as it writes the file. The
   change is loaded with `wg syncconf` or by restarting `wg-quick`, through
   systemd's `wg-quick@IFACE` where it's available, so tunnels come back
   after a reboot.
5. **Confirm.** Before changing an SSH node, wglink arms a systemd timer
   there that puts the previous config back. After the change, it opens a
   new SSH connection and cancels the timer. If the change cut off the route
   wglink uses, the new connection fails, and a minute later the node undoes
   the change by itself. `--no-revert` turns this off.

The scripts are embedded in the binary and fed to `sh -s` over stdin. They
are tested under dash, bash and busybox. Only a few `WG_*` variables change
between runs, and each is validated and quoted.

## Security

- **Private keys stay on their nodes.** Each is created with `wg genkey` on
  the machine that uses it, stored as `/etc/wireguard/IFACE.key` with mode
  0600, and never printed. The scripts mask keys in everything they report.
  The integration tests check that no private key appears in wglink's
  output, inventory or state. The one exception is a manual node: wglink
  can't run anything there, so `wglink export` makes its key and writes it
  into that one config, readable only by you.
- **Keys you can replace.** `wglink rotate NETWORK [NODE...]` makes new keys
  on the members and hands their peers the new public keys, all loaded live.
  If a rotation is interrupted, the next `plan` notices a config or
  interface still using the old key and loads the new one.
- **Optional preshared keys.** With `--psk`, wglink makes a key for each
  pair from the operating system's random source and sends it to both
  members over the same connection as the scripts, on standard input. It
  never writes them on the controller; nodes report only their hashes. The
  tests fail if a preshared key shows up in any script's output.
- **No secrets on the controller.** The state file holds public keys,
  addresses, and hashes of configs and preshared keys, with the secrets
  removed before hashing.
- **No shell on the controller.** wglink runs commands as argument lists.
  Values sent to remote shells are validated and single-quoted.
- **Your SSH setup, unchanged.** wglink uses your SSH client in batch mode
  with your keys, agent and known hosts, and only ever runs `sudo -n`.
- **Hands off foreign configs.** wglink marks the files it writes and won't
  replace a config without that marker unless you pass `--adopt`. It keeps
  the previous file as `IFACE.conf.prev`.

## Compared with other tools

- **Writing `wg-quick` configs by hand:** wglink writes the same files, but it
  handles keys and addresses and keeps every peer list in sync across
  machines.
- **Mesh VPN platforms** (Tailscale, Headscale, Netmaker, NetBird): those run
  an agent and a coordination service and handle NAT traversal and
  roaming. wglink suits machines that can already reach each other, such as a
  homelab, a rack or a few VPS hosts. Its result is plain WireGuard with
  nothing running in the background.
- **Configuration management** (Ansible and similar): wglink is narrower and
  knows WireGuard. It picks non-conflicting addresses, generates keys on the
  nodes, loads changes live and checks link health.

## Development

```sh
go test ./...          # unit tests; node scripts run under dash, bash and busybox if present
go vet ./...
```

The integration tests in `test/integration` build the binary and run it
against real WireGuard interfaces in network namespaces. They change the
machine they run on, so they need root (or passwordless sudo) and an explicit
opt-in:

```sh
WGLINK_INTEGRATION=1 go test -tags integration -v ./test/integration/
```

CI runs them on every pull request, with the runner itself as a systemd node.

To release, add the version's section to `CHANGELOG.md`, then push a `vX.Y.Z`
tag, or run the **release** workflow from the Actions tab with the version
(and, optionally, a commit). Either way it tests, builds the archives and
publishes them. To redo a release, run the workflow with **replace** ticked.
It removes the old release and tag only after the new build passes.

| Package | Role |
| --- | --- |
| `cmd/wglink` | entry point |
| `internal/cli` | commands, flags and output |
| `internal/config` | `wglink.yaml`: parsing, validation and comment-preserving edits |
| `internal/ipam` | subnet, port and address selection |
| `internal/engine` | plan, apply and status |
| `internal/node` | the embedded scripts and their parsers |
| `internal/runner` | local, SSH, exec, `pct` and guest agent transports |
| `internal/wgconf` | rendering, parsing and comparing `wg-quick` configs |
| `internal/state` | `wglink.state.json` |

## Roadmap

- **0.3:**
  - A terminal UI.

See [CHANGELOG.md](CHANGELOG.md) for what each release added.

## License

[MIT](LICENSE)
