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
	for c in wg wg-quick ip systemctl ping; do
		if have "$c"; then out "@have $c"; fi
	done
	if use_systemd; then out "@service systemd"; else out "@service none"; fi
	if [ -d /sys/module/wireguard ]; then out "@module"; fi
	out "@forward4 $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)"
	out "@forward6 $(cat /proc/sys/net/ipv6/conf/all/forwarding 2>/dev/null || echo 0)"
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
	fi
	if have ip && is_up "$1"; then out "@up"; fi
}
