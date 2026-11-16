# shellcheck shell=sh
# Confirms a change to WG_IFACE: stops the timer write.sh set to undo it.

main() {
	exec </dev/null
	timer="wglink-revert-$WG_IFACE"
	script="$WG_DIR/.$WG_IFACE.revert"
	# Once the timer is stopped it can't fire; then see whether it already did.
	systemctl stop "$timer.timer" 2>/dev/null || true
	if [ ! -f "$script" ] || systemctl is-active --quiet "$timer.service"; then
		die "its timer already undid the change to $WG_IFACE"
	fi
	systemctl reset-failed "$timer.timer" "$timer.service" 2>/dev/null || true
	rm -f "$script" "$WG_DIR/$WG_IFACE.conf.revert"
	out "@confirmed"
}
