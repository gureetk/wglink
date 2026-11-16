# shellcheck shell=sh
# Reports what wglink needs to know about this node. Prints no secrets.

main() {
	exec </dev/null
	out "@probe 1"
	out "@hostname $(cat /proc/sys/kernel/hostname 2>/dev/null || hostname)"
	out "@uid $(id -u)"
	if [ -r /etc/os-release ]; then
		out "@os $(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')"
	fi
	for c in wg wg-quick ip systemctl systemd-run ping; do
		if have "$c"; then out "@have $c"; fi
	done
	if use_systemd; then out "@service systemd"; else out "@service none"; fi
	if [ -d /sys/module/wireguard ]; then out "@module"; fi
	out "@forward4 $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)"
	out "@forward6 $(cat /proc/sys/net/ipv6/conf/all/forwarding 2>/dev/null || echo 0)"
	# Newer kernels can forward IPv6 on one interface alone.
	if [ -e /proc/sys/net/ipv6/conf/lo/force_forwarding ]; then out "@forward6iface"; fi
	if have ip; then
		out "@section addr"
		ip -o addr show 2>/dev/null | raw || true
		out "@section route"
		{ ip -o -4 route show table all && ip -o -6 route show table all; } 2>/dev/null | raw || true
	fi
	out "@section udp"
	cat /proc/net/udp /proc/net/udp6 2>/dev/null | raw || true
	if have wg; then
		out "@section wgports"
		wg show all listen-port 2>/dev/null | raw || true
	fi
	for f in "$WG_DIR"/*.conf; do
		if [ -f "$f" ] && is_managed "$f"; then
			name=${f##*/}
			out "@managed ${name%.conf} $(sed -n '1s/^# Managed by wglink (network \([a-z0-9-]*\)).*/\1/p' "$f")"
		fi
	done
	# shellcheck disable=SC2086 # validated names, split on purpose
	for iface in $WG_IFACES; do
		probe_iface "$iface"
	done
	out "@end"
}

probe_iface() {
	conf="$WG_DIR/$1.conf"
	out "@iface $1"
	if [ -f "$conf" ]; then
		if is_managed "$conf"; then out "@conf managed"; else out "@conf unmanaged"; fi
		out "@section config"
		public "$conf" | raw
	fi
	if [ -s "$WG_DIR/$1.key" ] && have wg; then
		out "@pub $(wg pubkey <"$WG_DIR/$1.key")"
		# Which key the config file and the running interface use, so a key
		# that changed but wasn't loaded shows up.
		if [ -f "$conf" ] && pub=$(conf_key "$conf" | wg pubkey 2>/dev/null); then
			out "@confpub $pub"
		fi
	fi
	if have ip && is_up "$1"; then
		out "@up"
		if have wg && pub=$(wg show "$1" public-key 2>/dev/null); then out "@livepub $pub"; fi
	fi
	# Preshared keys, by hash: in their files, and as the config has them.
	if have sha256sum; then
		psk_peers "$1" | while read -r peer; do
			out "@pskfile $peer $(key_hash "$(cat "$WG_DIR/$1.psk.$peer")")"
		done
		if [ -f "$conf" ]; then
			conf_psks "$conf" | while read -r peer psk; do
				out "@pskconf $peer $(key_hash "$psk")"
			done
		fi
	fi
}

# key_hash prints a key's SHA-256, which reveals nothing about the key.
key_hash() { printf '%s\n' "$1" | sha256sum | cut -d ' ' -f 1; }

# conf_psks prints "peer key" for each preshared key in a config, naming
# peers by the comment wglink writes under [Peer]. The keys stay on this node.
conf_psks() {
	awk '
		/^\[Peer\]/ { name = ""; next }
		/^#/ && name == "" { name = $2; next }
		tolower($0) ~ /^[ \t]*presharedkey[ \t]*=/ {
			v = $0
			sub(/^[^=]*=[ \t]*/, "", v)
			sub(/[ \t\r]+$/, "", v)
			if (name != "") print name, v
		}
	' "$1"
}

# conf_key prints the private key in a config, which stays on this node.
conf_key() {
	sed -n 's/^[[:blank:]]*[Pp][Rr][Ii][Vv][Aa][Tt][Ee][Kk][Ee][Yy][[:blank:]]*=[[:blank:]]*//p' "$1" | head -n 1
}
