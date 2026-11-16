# shellcheck shell=sh
# Stores preshared keys for WG_IFACE from WG_PSKS, "peer=key" words, one file
# per peer that only root can read. wglink makes each key and gives it to
# both peers of a pair; write.sh puts it in the config.

main() {
	exec </dev/null
	mkdir -p "$WG_DIR"
	# shellcheck disable=SC2086 # validated "peer=key" words, split on purpose
	for pk in $WG_PSKS; do
		f="$WG_DIR/$WG_IFACE.psk.${pk%%=*}"
		printf '%s\n' "${pk#*=}" >"$f.new"
		chmod 600 "$f.new"
		mv -f "$f.new" "$f"
	done
	out "@stored"
}
