# wglink

> One command to connect Linux machines over WireGuard.

wglink (Wireguard Link) is being developed as a lightweight go binary to streamline connecting machines over LAN via Wireguard and the process of managing them.

## Status

wglink is very early work in progress and is not ready for production use.

## Planned Features
- Point-to-point Wireguard links
- Full mesh Wireguard networks
- Automatic tunnel address selection
- Manual control over hosts, addresses, and subnets
- Declarative inventory configuration
- Detect configuration drift after changes
- Report tunnel health, handshakes, traffic, and peer reachability
- Generate private keys on the machines which use them
- Support SSH and local execution
- Optional Proxmox Integration

## Requirements
- Linux
- SSH access or local access
- wireguard-tools
- Go 1.26 or newer (if building wglink from source)

## License
wglink is licensed under the MIT License.
