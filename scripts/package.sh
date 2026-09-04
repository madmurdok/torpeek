#!/bin/sh
#
# Assemble one release archive per platform: the torpeek binary, the bundled
# ffmpeg and ffprobe, and the licence material that has to travel with them.
#
# usage: scripts/package.sh [platform ...]
#        (no arguments: every platform in third_party/ffmpeg.lock)
#
# Run `make cross` first - this packages binaries, it does not build them. That
# separation is deliberate: `make cross` exports CGO_ENABLED=0, and with cgo off
# the piece-completion store is bbolt rather than sqlite. That is the
# configuration every acceptance run and every `make check` has measured
# (TOR-59), so an archive must contain exactly the binary `make cross` made and
# not one this script rebuilt with different flags.
#
# A platform with no LGPL ffmpeg is skipped, loudly, and the script exits
# non-zero at the end. Three archives out of four must not be able to pass for
# a complete release (docs/licensing.md).

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lock="$root/third_party/ffmpeg.lock"
dist="$root/dist"

die() {
	echo "package: $*" >&2
	exit 1
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "need sha256sum or shasum"
	fi
}

# bytes of a file, portably: stat's flags differ between BSD and GNU.
bytes() {
	wc -c <"$1" | tr -d ' '
}

mib() {
	awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'
}

field() {
	awk -v want="[$1]" -v key="$2" '
		/^\[/ { in_stanza = ($0 == want); next }
		!in_stanza { next }
		{
			eq = index($0, "=")
			if (eq == 0) next
			k = substr($0, 1, eq - 1)
			v = substr($0, eq + 1)
			gsub(/^[ \t]+|[ \t]+$/, "", k)
			gsub(/^[ \t]+|[ \t]+$/, "", v)
			if (k == key) { print v; exit }
		}
	' "$lock"
}

# The version is read out of the code, never passed in, for the same reason
# `make tag` derives it: a version handed in on the command line is how an
# archive comes to claim a release nobody shipped.
version=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' "$root/internal/version/version.go")
[ -n "$version" ] || die "cannot read Version from internal/version/version.go"

platforms=$*
if [ -z "$platforms" ]; then
	platforms=$(awk '/^\[/ { print substr($0, 2, length($0) - 2) }' "$lock")
fi

write_readme() {
	# $1 archive directory, $2 platform, $3 exe suffix
	exe=$3
	# How a person on that platform actually types it, and the one
	# unpacking note that only applies off Windows.
	case $2 in
	windows-*)
		invoke="torpeek.exe"
		unpack_note="Unzip the whole folder before running anything. Explorer will
happily run torpeek.exe straight out of the .zip preview, and from there it
cannot see the ffmpeg.exe that is supposed to be next to it."
		;;
	linux-*)
		invoke="./torpeek"
		unpack_note="Some unpacking tools drop the execute bit. If the shell says
\"permission denied\": chmod +x torpeek ffmpeg ffprobe.

This needs a glibc-based distribution - Debian, Ubuntu, Fedora, RHEL, Arch and
anything like them. torpeek itself has no libc at all, but the bundled ffmpeg
and ffprobe are linked against glibc, so on Alpine or another musl system they
will not start. Install ffmpeg from the distribution's own packages there and
delete the two copies here; torpeek falls back to PATH."
		;;
	*)
		invoke="./torpeek"
		unpack_note="Some unpacking tools drop the execute bit. If the shell says
\"permission denied\": chmod +x torpeek ffmpeg ffprobe."
		;;
	esac
	cat >"$1/README.txt" <<EOF
torpeek $version - $2

Preview a video torrent without downloading it: frames spread across each
video file, plus a technical summary.

WHAT IS IN THIS FOLDER

$(printf '  %-22s %s\n' \
		"torpeek$exe" "the program - this is the only thing you run" \
		"ffmpeg$exe" "frame decoding, used by torpeek" \
		"ffprobe$exe" "container inspection, used by torpeek" \
		"README.txt" "this file" \
		"THIRD-PARTY-NOTICES.md" "what ffmpeg is, where it came from, its licence" \
		"licenses/" "the full licence texts those notices refer to")

Nothing has to be installed. Keep the three executables in the same folder:
torpeek looks for ffmpeg and ffprobe next to itself before it falls back to
whatever the machine has on PATH, and the copies here are the ones this build
was tested with.

RUNNING IT

  $invoke -n 6 -out ./frames some.torrent
  $invoke -web

The first writes six frames from each video file in the torrent into ./frames.
The second serves the web UI and opens a browser at it. A magnet URI works
anywhere a .torrent path does. \`$invoke -help\` lists every flag.

$unpack_note

LICENCE

The ffmpeg and ffprobe here are LGPL builds, redistributed unmodified.
THIRD-PARTY-NOTICES.md names the exact upstream build and commit, carries the
written offer for the corresponding source, and points at the licence texts in
licenses/. torpeek itself is a separate program that runs them as child
processes; it does not link against them.
EOF
}

write_notices() {
	# $1 archive directory, $2 platform
	platform=$2
	cat >"$1/THIRD-PARTY-NOTICES.md" <<EOF
# Third-party notices - torpeek $version ($platform)

This archive contains software other than torpeek. This file says what, under
which licence, and where its source is.

## FFmpeg (ffmpeg, ffprobe)

**Licence: $(field "$platform" license).** Full text in
\`licenses/ffmpeg/COPYING.LGPLv3\`; the GNU GPL v3, which the LGPL v3
incorporates by reference, is in \`licenses/ffmpeg/COPYING.GPLv3\`. FFmpeg's own
summary of which of its files are under which licence is in
\`licenses/ffmpeg/LICENSE.md\`.

These are **LGPL** builds: FFmpeg's GPL-only components (libx264, libx265, the
GPL filters) are not configured in. They are redistributed **unmodified**, byte
for byte as published by the upstream builder.

| | |
|---|---|
| version | \`$(field "$platform" ffmpeg_version)\` |
| FFmpeg commit | \`$(field "$platform" ffmpeg_commit)\` |
| prebuilt by | $(field "$platform" source), release \`$(field "$platform" release_tag)\` |
| build variant | $(field "$platform" variant) |
| upstream archive | $(field "$platform" url) |
| archive sha256 | \`$(field "$platform" archive_sha256)\` |
| ffmpeg sha256 | \`$(field "$platform" ffmpeg_sha256)\` |
| ffprobe sha256 | \`$(field "$platform" ffprobe_sha256)\` |

### Written offer for the corresponding source

The complete corresponding source code for the FFmpeg build in this archive is:

- FFmpeg itself, at commit \`$(field "$platform" ffmpeg_commit)\`:
  $(field "$platform" ffmpeg_source)
- the build definition that produced this binary, including the versions of
  every library configured into it: $(field "$platform" build_scripts)

For a period of three years from the date this archive was distributed, and on
request, the torpeek project will provide a complete machine-readable copy of
that source on a physical medium or by equivalent means, for no more than the
cost of performing the distribution. Open an issue at
https://github.com/madmurdok/torpeek to ask.

## torpeek

torpeek is a separate program. It never links FFmpeg: it runs \`ffmpeg\` and
\`ffprobe\` as child processes and talks to them over their command line, stdout
and stderr (see \`internal/ffmpeg\`). Bundling the two executables in one
folder is aggregation, not combination, so nothing here constrains torpeek's
own licence.

torpeek is compiled from Go source. The 73 Go modules linked into this
executable (the CGO-free build's set, listed by
\`CGO_ENABLED=0 go list -deps ./cmd/torpeek\`) are under MIT, BSD, ISC,
Apache-2.0 and MPL-2.0. None is GPL or LGPL, and none is copyleft in a way
that reaches the whole program.

Eight of them are under the **Mozilla Public License 2.0**, including
\`github.com/anacrolix/torrent\`, the BitTorrent engine. MPL-2.0 is file-level
copyleft: it permits distribution inside a larger work under other terms
(MPL-2.0 section 3.3) but requires that recipients be told how to obtain the
source of the covered files. That source is unmodified upstream, at the exact
versions recorded in torpeek's \`go.mod\`, and is available from the Go module
proxy at https://proxy.golang.org and from each project's repository. The same
three-year written offer above covers it.
EOF
}

skipped=

for platform in $platforms; do
	status=$(field "$platform" status)
	case $status in
	ok) ;;
	blocked)
		reason=$(field "$platform" reason)
		echo "package: SKIP $platform - $reason" >&2
		skipped="$skipped $platform"
		continue
		;;
	"")
		die "$platform is not in $lock"
		;;
	*)
		die "$platform has unknown status '$status'"
		;;
	esac

	exe=""
	case $platform in
	windows-*) exe=".exe" ;;
	esac

	binary="$dist/$platform/torpeek$exe"
	[ -f "$binary" ] || die "no $binary - run 'make cross' first"

	# Fetching is part of packaging, not a step somebody can forget: an archive
	# assembled from a third_party tree nobody verified is exactly the failure
	# the lock exists to prevent.
	"$root/scripts/fetch-ffmpeg.sh" "$platform" ||
		die "$platform: could not get a verified ffmpeg"

	ffmpeg_src="$root/third_party/ffmpeg/$platform/ffmpeg$exe"
	ffprobe_src="$root/third_party/ffmpeg/$platform/ffprobe$exe"
	for f in "$ffmpeg_src" "$ffprobe_src"; do
		[ -f "$f" ] || die "$platform: expected $f after fetching"
	done

	name="torpeek-$version-$platform"
	stage="$dist/$name"
	rm -rf "$stage"
	mkdir -p "$stage/licenses/ffmpeg"

	cp "$binary" "$stage/torpeek$exe"
	cp "$ffmpeg_src" "$stage/ffmpeg$exe"
	cp "$ffprobe_src" "$stage/ffprobe$exe"
	cp "$root/packaging/licenses/ffmpeg/COPYING.LGPLv3" \
		"$root/packaging/licenses/ffmpeg/COPYING.GPLv3" \
		"$root/packaging/licenses/ffmpeg/LICENSE.md" \
		"$stage/licenses/ffmpeg/"
	chmod +x "$stage/torpeek$exe" "$stage/ffmpeg$exe" "$stage/ffprobe$exe"

	write_readme "$stage" "$platform" "$exe"
	write_notices "$stage" "$platform"

	unpacked=$(find "$stage" -type f -exec wc -c {} \; | awk '{ t += $1 } END { print t }')

	# Windows gets a zip because that is what Explorer can open with nothing
	# installed; everything else gets a tar.gz, which preserves the execute
	# bit that a zip does not.
	case $platform in
	windows-*)
		archive="$dist/$name.zip"
		rm -f "$archive"
		(cd "$dist" && zip -qr "$name.zip" "$name")
		;;
	*)
		archive="$dist/$name.tar.gz"
		rm -f "$archive"
		(cd "$dist" && tar -czf "$name.tar.gz" "$name")
		;;
	esac

	printf '%s: %s unpacked, %s as %s\n' \
		"$platform" "$(mib "$unpacked")" "$(mib "$(bytes "$archive")")" \
		"$(basename "$archive")"
done

# Every archive of this version that is on disk, not only the ones this run
# rebuilt. Packaging one platform at a time is normal - the macOS one is
# blocked, and a fix gets retried alone - and a checksum file that forgot the
# other platforms each time would be worse than none.
mkdir -p "$dist"
sums="$dist/torpeek-$version-SHA256SUMS.txt"
: >"$sums"
for candidate in "$dist/torpeek-$version-"*.tar.gz "$dist/torpeek-$version-"*.zip; do
	[ -f "$candidate" ] || continue
	printf '%s  %s\n' "$(sha256 "$candidate")" "$(basename "$candidate")" >>"$sums"
done
echo "checksums: $sums"
cat "$sums"

if [ -n "$skipped" ]; then
	echo "package: no archive for:$skipped" >&2
	echo "package: this release is incomplete - see docs/licensing.md" >&2
	exit 1
fi
