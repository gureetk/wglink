# shellcheck shell=sh
# Writes WG_CONFIG with this node's secrets filled in, keeps the old file as
# .prev, then loads it: live with wg syncconf, or by (re)starting. With
# WG_REVERT seconds, a timer undoes the change unless confirm.sh runs first.

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
	if [ "$WG_REVERT" -gt 0 ]; then arm_revert; fi
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

# arm_revert sets a systemd timer that puts the current config back, or
# takes a new interface down, if wglink loses contact and can't confirm.
arm_revert() {
	if ! use_systemd || ! have systemd-run; then
		die "undoing a change needs systemd; apply with --no-revert to go ahead without it"
	fi
	timer="wglink-revert-$WG_IFACE"
	systemctl stop "$timer.timer" 2>/dev/null || true
	systemctl reset-failed "$timer.timer" "$timer.service" 2>/dev/null || true
	if [ -f "$conf" ]; then cp -p "$conf" "$conf.revert"; else rm -f "$conf.revert"; fi
	revert_script >"$WG_DIR/.$WG_IFACE.revert"
	systemd-run --quiet --collect --unit="$timer" --on-active="${WG_REVERT}s" \
		--timer-property=AccuracySec=1s /bin/sh "$WG_DIR/.$WG_IFACE.revert" ||
		die "couldn't set the timer that undoes this change; apply with --no-revert to go ahead without it"
	out "@revert ${WG_REVERT}s"
}

# revert_script prints what the timer runs. Interface names need no escaping.
revert_script() {
	cat <<EOF
#!/bin/sh
# Written by wglink: undoes a change to $WG_IFACE that wasn't confirmed.
cd "\$(dirname "\$0")" || exit 1
if [ -f '$WG_IFACE.conf.revert' ]; then
	mv -f '$WG_IFACE.conf.revert' '$WG_IFACE.conf'
	systemctl restart 'wg-quick@$WG_IFACE'
else
	systemctl disable --now 'wg-quick@$WG_IFACE'
	rm -f '$WG_IFACE.conf'
fi
rm -f "\$0"
EOF
}

unit_failed() {
	journalctl -u "$1" -n 15 --no-pager >&2 2>/dev/null || true
	die "$1 failed to start"
}
