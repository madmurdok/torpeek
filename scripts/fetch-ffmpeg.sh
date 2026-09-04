#!/bin/sh
#
# Fetch the bundled ffmpeg and ffprobe for one or more release platforms into
# third_party/ffmpeg/<platform>/, verifying every checksum in
# third_party/ffmpeg.lock before anything is written where the packager can
# see it.
#
# usage: scripts/fetch-ffmpeg.sh [platform ...]
#        (no arguments: every platform in the lock whose status is ok)
#
# Nothing here ends up in git - .gitignore carries /third_party/ffmpeg/. The
# machinery is the deliverable; the binaries are a download.
#
# Three things this refuses to do, each because getting it wrong is how a
# release ships something nobody chose:
#
#   - trust a download. The archive's sha256 is checked against the lock, and
#     so is the sha256 of each binary taken out of it. A mismatch deletes the
#     download and exits non-zero rather than warning.
#   - trust that an -lgpl asset is still LGPL. The licence text inside the
#     upstream archive is checksummed too. If upstream repointed the asset at a
#     GPL build, that text changes and this stops.
#   - guess at a platform the lock says is blocked. It exits non-zero and names
#     the reason, so the caller decides.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lock="$root/third_party/ffmpeg.lock"
dest_root="$root/third_party/ffmpeg"
cache="$dest_root/.cache"

die() {
	echo "fetch-ffmpeg: $*" >&2
	exit 1
}

[ -f "$lock" ] || die "no lock file at $lock"

# sha256 of one file, bare hex. macOS ships shasum, Linux sha256sum, and a
# release can be cut on either.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "need sha256sum or shasum"
	fi
}

download() {
	# $1 url, $2 destination
	if command -v curl >/dev/null 2>&1; then
		curl -fL --retry 3 --proto '=https' -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

# field <platform> <key> - one value out of the lock, empty if absent.
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

platforms=$*
if [ -z "$platforms" ]; then
	platforms=$(awk '
		/^\[/ { name = substr($0, 2, length($0) - 2) }
		/^status[ \t]*=[ \t]*ok$/ { print name }
	' "$lock")
fi
[ -n "$platforms" ] || die "no platforms to fetch"

fetch_one() {
	platform=$1

	status=$(field "$platform" status)
	case $status in
	ok) ;;
	blocked)
		reason=$(field "$platform" reason)
		echo "fetch-ffmpeg: $platform is blocked: $reason" >&2
		return 1
		;;
	"") die "$platform is not in $lock" ;;
	*) die "$platform has unknown status '$status'" ;;
	esac

	url=$(field "$platform" url)
	archive_sha=$(field "$platform" archive_sha256)
	archive_root=$(field "$platform" archive_root)
	ffmpeg_member=$(field "$platform" ffmpeg_member)
	ffprobe_member=$(field "$platform" ffprobe_member)
	license_member=$(field "$platform" license_member)
	ffmpeg_sha=$(field "$platform" ffmpeg_sha256)
	ffprobe_sha=$(field "$platform" ffprobe_sha256)
	license_sha=$(field "$platform" license_sha256)
	version=$(field "$platform" ffmpeg_version)

	for required in url archive_sha archive_root ffmpeg_member ffprobe_member \
		license_member ffmpeg_sha ffprobe_sha license_sha version; do
		eval "value=\$$required"
		[ -n "$value" ] || die "$platform: lock is missing $required"
	done

	dest="$dest_root/$platform"
	ffmpeg_out="$dest/$(basename "$ffmpeg_member")"
	ffprobe_out="$dest/$(basename "$ffprobe_member")"

	# Already correct: say so and touch nothing. A release is cut more than
	# once, and re-downloading 113 MB to prove it is the same 113 MB is a
	# waste of somebody's evening.
	if [ -f "$ffmpeg_out" ] && [ -f "$ffprobe_out" ] &&
		[ "$(sha256 "$ffmpeg_out")" = "$ffmpeg_sha" ] &&
		[ "$(sha256 "$ffprobe_out")" = "$ffprobe_sha" ]; then
		echo "$platform: ffmpeg $version already present and verified"
		return 0
	fi

	mkdir -p "$cache" "$dest"
	archive="$cache/$(basename "$url")"

	if [ -f "$archive" ] && [ "$(sha256 "$archive")" = "$archive_sha" ]; then
		echo "$platform: using cached $(basename "$archive")"
	else
		echo "$platform: downloading $(basename "$archive")"
		download "$url" "$archive.part" || die "$platform: download failed: $url"
		mv "$archive.part" "$archive"
		got=$(sha256 "$archive")
		if [ "$got" != "$archive_sha" ]; then
			rm -f "$archive"
			die "$platform: archive sha256 mismatch
  expected $archive_sha
  got      $got
The download was deleted. Either the network mangled it or upstream replaced
the asset; in the second case the lock has to be reviewed, not overwritten."
		fi
	fi

	tmp="$dest/.unpack"
	rm -rf "$tmp"
	mkdir -p "$tmp"

	case $archive in
	*.tar.xz)
		tar -xJf "$archive" -C "$tmp" \
			"$archive_root/$ffmpeg_member" \
			"$archive_root/$ffprobe_member" \
			"$archive_root/$license_member" ||
			die "$platform: could not unpack $(basename "$archive")"
		;;
	*.zip)
		unzip -oq "$archive" \
			"$archive_root/$ffmpeg_member" \
			"$archive_root/$ffprobe_member" \
			"$archive_root/$license_member" -d "$tmp" ||
			die "$platform: could not unpack $(basename "$archive")"
		;;
	*) die "$platform: do not know how to unpack $(basename "$archive")" ;;
	esac

	# The licence text is checked first: if the build is no longer the LGPL one
	# we chose, nothing else about it is worth verifying.
	got=$(sha256 "$tmp/$archive_root/$license_member")
	if [ "$got" != "$license_sha" ]; then
		rm -rf "$tmp"
		die "$platform: the licence text inside the archive changed
  expected $license_sha
  got      $got
torpeek only ships an LGPL ffmpeg (docs/licensing.md). Read the new text
before touching the lock - this is the guard against an -lgpl asset quietly
becoming a GPL build."
	fi

	for pair in "ffmpeg $ffmpeg_member $ffmpeg_sha" "ffprobe $ffprobe_member $ffprobe_sha"; do
		# shellcheck disable=SC2086
		set -- $pair
		tool=$1
		member=$2
		want=$3
		got=$(sha256 "$tmp/$archive_root/$member")
		if [ "$got" != "$want" ]; then
			rm -rf "$tmp"
			die "$platform: $tool sha256 mismatch
  expected $want
  got      $got"
		fi
	done

	mv -f "$tmp/$archive_root/$ffmpeg_member" "$ffmpeg_out"
	mv -f "$tmp/$archive_root/$ffprobe_member" "$ffprobe_out"
	mv -f "$tmp/$archive_root/$license_member" "$dest/LICENSE.txt"
	chmod +x "$ffmpeg_out" "$ffprobe_out"
	rm -rf "$tmp"

	# A copy of the provenance travels with the binaries, so a
	# third_party/ffmpeg tree found on a machine months later can still say
	# what it holds. The lock stays the source of truth.
	{
		echo "# written by scripts/fetch-ffmpeg.sh - do not edit"
		printf '%-14s = %s\n' "platform" "$platform"
		for key in source release_tag build_scripts variant license \
			ffmpeg_version ffmpeg_commit ffmpeg_source url archive_sha256; do
			printf '%-14s = %s\n' "$key" "$(field "$platform" "$key")"
		done
		printf '%-14s = %s\n' "ffmpeg_sha256" "$ffmpeg_sha"
		printf '%-14s = %s\n' "ffprobe_sha256" "$ffprobe_sha"
		printf '%-14s = %s\n' "license_sha256" "$license_sha"
	} >"$dest/PROVENANCE.txt"

	echo "$platform: ffmpeg $version verified into third_party/ffmpeg/$platform"
}

failed=
for platform in $platforms; do
	if ! fetch_one "$platform"; then
		failed="$failed $platform"
	fi
done

if [ -n "$failed" ]; then
	echo "fetch-ffmpeg: no ffmpeg for:$failed" >&2
	exit 1
fi
