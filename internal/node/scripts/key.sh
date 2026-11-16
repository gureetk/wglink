# shellcheck shell=sh
# Creates the interface's private key if missing, or a new one with
# WG_ROTATE=1, and prints its public key. The private key never leaves this
# node.

main() {
	exec </dev/null
	have wg || die "wg is not installed; install wireguard-tools"
	key="$WG_DIR/$WG_IFACE.key"
	mkdir -p "$WG_DIR"
	if [ ! -s "$key" ] || [ "$WG_ROTATE" = 1 ]; then
		wg genkey >"$key.new"
		mv -f "$key.new" "$key"
		out "@created"
	fi
	chmod 600 "$key"
	out "@pub $(wg pubkey <"$key")"
}
