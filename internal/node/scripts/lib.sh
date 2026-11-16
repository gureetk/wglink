# shellcheck shell=sh
# Helpers shared by every node script. wglink prepends "set -eu", a umask
# of 077 and the WG_* variables, then calls main.

out() { printf '%s\n' "$*"; }

die() {
	printf 'wglink: %s\n' "$*" >&2
	exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

# raw marks lines of command output so they can't pass for directives. awk
# also ends a last line that has no newline, which would swallow the next
# directive.
raw() { awk '{ print "|" $0 }'; }

# is_managed FILE: the file starts with wglink's marker.
is_managed() {
	first=
	IFS= read -r first <"$1" || true
	case $first in "# Managed by wglink"*) return 0 ;; esac
	return 1
}

# public FILE: the config without its private key and with preshared keys
# masked. wgconf.Public does the same, so hashes match.
public() {
	sed -e '/^[[:blank:]]*[Pp][Rr][Ii][Vv][Aa][Tt][Ee][Kk][Ee][Yy][[:blank:]]*=/d' \
		-e 's/^\([[:blank:]]*[Pp][Rr][Ee][Ss][Hh][Aa][Rr][Ee][Dd][Kk][Ee][Yy][[:blank:]]*=\).*$/\1 (set)/' "$1"
}

use_systemd() {
	case $WG_SERVICE in
	systemd) return 0 ;;
	none) return 1 ;;
	esac
	[ -d /run/systemd/system ] && have systemctl
}

is_up() { ip link show dev "$1" >/dev/null 2>&1; }
