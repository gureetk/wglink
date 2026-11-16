# shellcheck shell=sh
# Stops the interface and deletes the files wglink wrote for it.

main() {
	exec </dev/null
	conf="$WG_DIR/$WG_IFACE.conf"
	if [ -f "$conf" ] && ! is_managed "$conf"; then
		die "$conf was not written by wglink; leaving it alone"
	fi
	unit="wg-quick@$WG_IFACE"
	if use_systemd; then
		systemctl stop "wglink-revert-$WG_IFACE.timer" 2>/dev/null || true
		systemctl disable --now --quiet "$unit" 2>/dev/null || true
	fi
	if is_up "$WG_IFACE" && [ -f "$conf" ]; then wg-quick down "$WG_IFACE" 2>/dev/null || true; fi
	if is_up "$WG_IFACE"; then ip link del dev "$WG_IFACE"; fi
	rm -f "$conf" "$conf.prev" "$conf.new" "$conf.revert" "$WG_DIR/.$WG_IFACE.revert" \
		"$WG_DIR/$WG_IFACE.key" "$WG_DIR/$WG_IFACE".psk.*
	out "@removed"
}
