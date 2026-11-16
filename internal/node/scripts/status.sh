# shellcheck shell=sh
# Reports interfaces, their peers and configs, and pings peers through the
# tunnel. WG_IFACES="*" means every WireGuard interface and wglink config.

main() {
	exec </dev/null
	have wg || die "wg is not installed; install wireguard-tools"
	# Handshake times are by this clock, so report it too.
	out "@now $(date +%s)"
	if [ "$WG_IFACES" = "*" ]; then
		WG_IFACES=$(all_ifaces)
	fi
	# shellcheck disable=SC2086 # validated names, split on purpose
	for iface in $WG_IFACES; do
		if is_up "$iface"; then
			out "@iface $iface up"
			# The first line holds the private key; peer lines hold preshared keys.
			wg show "$iface" dump |
				awk 'BEGIN { FS = OFS = "\t" } NR == 1 { $1 = "(hidden)" } NR > 1 && $2 != "(none)" { $2 = "(set)" } { print }' |
				raw
		else
			out "@iface $iface down"
		fi
		if [ -f "$WG_DIR/$iface.conf" ]; then
			out "@config $iface"
			public "$WG_DIR/$iface.conf" | raw
		fi
	done
	# shellcheck disable=SC2086 # "iface,addr" pairs, split on purpose
	for target in $WG_PING; do
		ping_one "${target%%,*}" "${target#*,}" &
	done
	wait
	out "@end"
}

# all_ifaces lists live WireGuard interfaces and the configs wglink wrote.
all_ifaces() {
	{
		wg show interfaces | tr ' ' '\n'
		for f in "$WG_DIR"/*.conf; do
			if [ -f "$f" ] && is_managed "$f"; then
				f=${f##*/}
				out "${f%.conf}"
			fi
		done
	} | grep . | sort -u
}

ping_one() {
	if res=$(ping -c 1 -W 1 -I "$1" "$2" 2>/dev/null); then
		rtt=$(printf '%s\n' "$res" | sed -n 's/.*time[=<]\([0-9.]*\).*/\1/p' | head -n 1)
		out "@ping $1 $2 ok ${rtt:-0}"
	else
		out "@ping $1 $2 fail"
	fi
}
