#!/bin/sh
# Builds release archives for each Linux target into dist/, with checksums,
# third-party licenses and release notes from CHANGELOG.md.
# Usage: scripts/release.sh vX.Y.Z (run from a clean checkout of that tag)
set -eu

tag=${1:-}
case $tag in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "usage: $0 vX.Y.Z" >&2
	exit 2
	;;
esac
version=${tag#v}
cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist

# Licenses of the modules linked into the binary.
go mod download
for mod in $(GOOS=linux go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}{{end}}{{end}}' ./cmd/wglink | sort -u); do
	dir=$(go list -m -f '{{.Dir}}' "${mod%@*}")
	printf '==== %s ====\n\n' "$mod"
	for f in "$dir"/LICENSE* "$dir"/NOTICE* "$dir"/COPYING*; do
		if [ -f "$f" ]; then cat "$f" && echo; fi
	done
done >dist/THIRD_PARTY_LICENSES

for target in amd64 arm64 armv7; do
	goarch=$target goarm=
	if [ "$target" = armv7 ]; then goarch=arm goarm=7; fi
	name="wglink_${version}_linux_$target"
	mkdir "dist/$name"
	CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
		go build -trimpath -ldflags='-s -w' -o "dist/$name/wglink" ./cmd/wglink
	cp LICENSE README.md CHANGELOG.md dist/THIRD_PARTY_LICENSES "dist/$name/"
	tar -C dist -czf "dist/$name.tar.gz" "$name"
done

# The go command stamps the version from the tag; make sure it did.
if [ "$(uname -m)" = x86_64 ]; then
	got=$("dist/wglink_${version}_linux_amd64/wglink" version)
	if [ "$got" != "wglink version $tag" ]; then
		echo "built binary says \"$got\", want $tag; is the checkout clean and at the tag?" >&2
		exit 1
	fi
fi

(cd dist && sha256sum ./*.tar.gz | sed 's| \./| |' >SHA256SUMS)

# Release notes: this version's section of the changelog.
awk -v v="$version" '
	/^## / { inside = index($0, "[" v "]") > 0; next }
	/^\[[^]]*\]: / { inside = 0 }
	inside { print }
' CHANGELOG.md >dist/NOTES.md
if [ ! -s dist/NOTES.md ]; then
	echo "CHANGELOG.md has no section for $version" >&2
	exit 1
fi
ls -l dist
