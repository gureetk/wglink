# shellcheck shell=sh
# Lists a Proxmox VE host's guests through pvesh. Without WG_GUESTS it
# prints the cluster's status and guests; with WG_GUESTS ("lxc/101
# qemu/200") it prints each guest's network interfaces on node WG_PVE_NODE.

main() {
	exec </dev/null
	have pvesh || die "pvesh not found; is this a Proxmox VE host?"
	if [ -z "$WG_GUESTS" ]; then
		section status /cluster/status
		section guests /cluster/resources --type vm
		out "@end"
		return
	fi
	errf=$(mktemp)
	trap 'rm -f "$errf"' EXIT
	# shellcheck disable=SC2086 # validated "type/id" words, split on purpose
	for g in $WG_GUESTS; do
		case ${g%/*} in
		lxc) path="/nodes/$WG_PVE_NODE/lxc/${g#*/}/interfaces" ;;
		*) path="/nodes/$WG_PVE_NODE/qemu/${g#*/}/agent/network-get-interfaces" ;;
		esac
		if res=$(pvesh get "$path" --output-format json 2>"$errf"); then
			out "@guest $g"
			printf '%s\n' "$res" | raw
		else
			out "@failed $g $(head -n 1 "$errf")"
		fi
	done
	out "@end"
}

# section NAME PATH [ARG...] prints one pvesh answer as a raw section. The
# answer may lack a final newline, so it goes through printf.
section() {
	name=$1
	shift
	res=$(pvesh get "$@" --output-format json) || die "pvesh get $1 failed"
	out "@section $name"
	printf '%s\n' "$res" | raw
}
