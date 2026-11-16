# shellcheck shell=sh
# Reports each interface's peers and pings quiet peers through the tunnel.

main() {
	exec </dev/null
	have wg || die "wg is not installed; install wireguard-tools"
	# shellcheck disable=SC2086 # validated names, split on purpose
	for iface in $WG_IFACES; do
		if ! is_up "$iface"; then
			out "@iface $iface down"
			continue
		fi
		out "@iface $iface up"
		# The first line holds the private key; peer lines hold preshared keys.
		wg show "$iface" dump |
			awk 'BEGIN { FS = OFS = "\t" } NR == 1 { $1 = "(hidden)" } NR > 1 && $2 != "(none)" { $2 = "(set)" } { print }' |
			raw
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

ping_one() {
	if res=$(ping -c 1 -W 1 -I "$1" "$2" 2>/dev/null); then
		rtt=$(printf '%s\n' "$res" | sed -n 's/.*time[=<]\([0-9.]*\).*/\1/p' | head -n 1)
		out "@ping $2 ok ${rtt:-0}"
	else
		out "@ping $2 fail"
	fi
}
