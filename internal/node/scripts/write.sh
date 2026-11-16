# shellcheck shell=sh
# Writes WG_CONFIG with this node's secrets filled in, keeps the old file as
# .prev, then loads it: live with wg syncconf, or by (re)starting.

main() {
	exec </dev/null
	conf="$WG_DIR/$WG_IFACE.conf"
	key="$WG_DIR/$WG_IFACE.key"
	strip="$WG_DIR/.$WG_IFACE.strip"
	trap 'rm -f "$conf.new" "$strip"' EXIT
	have wg-quick || die "wg-quick is not installed; install wireguard-tools"
	[ -s "$key" ] || die "no private key at $key"
	if [ -f "$conf" ] && ! is_managed "$conf" && [ "$WG_ADOPT" != 1 ]; then
		die "$conf was not written by wglink; use --adopt to replace it"
	fi
	fill >"$conf.new"
	if [ -f "$conf" ]; then cp -p "$conf" "$conf.prev"; fi
	mv -f "$conf.new" "$conf"
	load
	out "@section config"
	public "$conf" | raw
}

# fill prints WG_CONFIG with the private key after [Interface] and each
# "PresharedKey = @peer" replaced by that peer's key file.
fill() {
	printf '%s' "$WG_CONFIG" | while IFS= read -r line || [ -n "$line" ]; do
		case $line in
		'[Interface]')
			out "$line"
			out "PrivateKey = $(cat "$key")"
			;;
		'PresharedKey = @'*)
			peer=${line#'PresharedKey = @'}
			psk="$WG_DIR/$WG_IFACE.psk.$peer"
			[ -s "$psk" ] || die "no preshared key for $peer at $psk"
			out "PresharedKey = $(cat "$psk")"
			;;
		*) out "$line" ;;
		esac
	done
}

load() {
	unit="wg-quick@$WG_IFACE"
	if use_systemd; then systemctl enable --quiet "$unit"; fi
	if is_up "$WG_IFACE"; then
		if [ "$WG_MODE" = live ]; then
			wg-quick strip "$WG_IFACE" >"$strip"
			wg syncconf "$WG_IFACE" "$strip"
			out "@loaded live"
			return 0
		fi
		if use_systemd && systemctl is-active --quiet "$unit"; then
			systemctl restart "$unit" || unit_failed "$unit"
			out "@loaded restart"
			return 0
		fi
		wg-quick down "$WG_IFACE"
	fi
	if use_systemd; then
		systemctl restart "$unit" || unit_failed "$unit"
	else
		wg-quick up "$WG_IFACE"
	fi
	out "@loaded start"
}

unit_failed() {
	journalctl -u "$1" -n 15 --no-pager >&2 2>/dev/null || true
	die "$1 failed to start"
}
