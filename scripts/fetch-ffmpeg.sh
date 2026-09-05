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
# Four things this refuses to do, each because getting it wrong is how a
# release ships something nobody chose:
#
#   - trust a download. The archive's sha256 is checked against the lock, and
#     so is the sha256 of each binary taken out of it. A mismatch deletes the
#     download and exits non-zero rather than warning.
#   - trust that an asset still carries the licence the lock claims. Two
#     guards, both per-platform, because the platforms disagree: Linux and
#     Windows are LGPL builds and macOS is a GPL one (TOR-93). Where the
#     upstream archive carries its own licence text, that text is checksummed
#     too. Always, and for every platform, the FFmpeg configure line embedded
#     in each binary is checked for the flags the lock requires and forbids -
#     which reads the artefact being shipped rather than a file beside it, and
#     works for a target this machine cannot execute.
#   - guess at a platform the lock says is blocked. It exits non-zero and names
#     the reason, so the caller decides.
#   - let the written source offer name a library it cannot hand over. The
#     macOS GPL build links x264, and x264 is the one dependency upstream's
#     build definition takes from a moving `master` URL rather than a pinned
#     version, so the revision it compiled is recorded in our lock and nowhere
#     else (TOR-98). Where a stanza names one, the source archive for it is
#     downloaded and verified here, and the binaries are checked to carry the
#     version string that revision's own source compiles into them - so a stale
#     revision cannot outlive an upstream rebuild quietly.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lock="$root/third_party/ffmpeg.lock"
dest_root="$root/third_party/ffmpeg"

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

# tree_sha256 <source archive> <scratch dir> - sha256 over what a source
# archive *contains*, rather than over its bytes: one `<sha256> <x|-> <path>`
# line per file, sorted, hashed. Two reasons it is not just sha256 of the
# tarball. The archive's own top directory is named after the ref it was asked
# for, so the same commit arrives under `x264-master/` from one URL and
# `x264-<sha>/` from another; and code.videolan.org builds these archives on
# demand, so a GitLab upgrade can re-compress a tree into different bytes
# without anything upstream having changed. The execute bit is part of the
# hash because a `configure` that arrives without it does not build, which
# would make the source we offer incomplete in the one way copying it around
# tends to break. The x264 tree has no symlinks and no empty directories, so
# files alone describe it.
tree_sha256() {
	rm -rf "$2"
	mkdir -p "$2"
	tar -xzf "$1" -C "$2" || die "could not unpack $(basename "$1")"

	top=$(find "$2" -mindepth 1 -maxdepth 1)
	[ -d "$top" ] || die "$(basename "$1") does not hold exactly one directory"

	(
		cd "$top" || exit 1
		find . -type f | LC_ALL=C sort | while IFS= read -r member; do
			if [ -x "$member" ]; then bit=x; else bit=-; fi
			printf '%s %s  %s\n' "$(sha256 "$member")" "$bit" "$member"
		done
	) >"$2/manifest.txt"

	sha256 "$2/manifest.txt"
	rm -rf "$2"
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

# member_path <member> - where a member sits inside the archive. archive_root
# is `.` when the archive has no top-level directory, and both tar and unzip
# want the bare name in that case rather than a "./" prefix.
member_path() {
	case $archive_root in
	.) printf '%s\n' "$1" ;;
	*) printf '%s/%s\n' "$archive_root" "$1" ;;
	esac
}

# get_archive <url> <sha256> - download, or reuse a cached copy, and verify.
# Sets $archive to the verified path.
get_archive() {
	archive="$cache/$(basename "$1")"

	if [ -f "$archive" ] && [ "$(sha256 "$archive")" = "$2" ]; then
		echo "$platform: using cached $(basename "$archive")"
		return 0
	fi

	echo "$platform: downloading $(basename "$archive")"
	download "$1" "$archive.part" || die "$platform: download failed: $1"
	mv "$archive.part" "$archive"
	got=$(sha256 "$archive")
	if [ "$got" != "$2" ]; then
		rm -f "$archive"
		die "$platform: archive sha256 mismatch
  expected $2
  got      $got
The download was deleted. Either the network mangled it or upstream replaced
the asset; in the second case the lock has to be reviewed, not overwritten."
	fi
}

# unpack <archive> <into> <member...> - extract only the named members.
unpack() {
	src=$1
	into=$2
	shift 2
	case $src in
	*.tar.xz)
		tar -xJf "$src" -C "$into" "$@" ||
			die "$platform: could not unpack $(basename "$src")"
		;;
	*.zip)
		unzip -oq "$src" "$@" -d "$into" ||
			die "$platform: could not unpack $(basename "$src")"
		;;
	*) die "$platform: do not know how to unpack $(basename "$src")" ;;
	esac
}

# check_configuration <file> - the licence guard that reads the artefact.
# FFmpeg bakes its whole configure line into every executable and prints it for
# `-version`, so grepping the bytes answers "is this the build the lock says it
# is" without running it. That matters twice over: darwin-arm64 cannot be
# executed on an Intel machine, and a release can be cut on Linux where none of
# the macOS binaries can.
check_configuration() {
	for flag in $configure_requires; do
		LC_ALL=C grep -q -a -F -- "$flag" "$1" || die "$platform: $(basename "$1") was not configured with $flag
The lock says this is a $license build and requires: $configure_requires
Read docs/licensing.md before touching the lock - the licence of what we
redistribute is decided by these flags, not by the filename of the asset."
	done

	for flag in $configure_forbids; do
		if LC_ALL=C grep -q -a -F -- "$flag" "$1"; then
			die "$platform: $(basename "$1") was configured with $flag
The lock says this is a $license build and forbids: $configure_forbids
A --enable-nonfree build may not be redistributed by anybody at all, and an
--enable-gpl build changes the licence of the whole archive. Read
docs/licensing.md; do not bump the hashes."
		fi
	done
}

# check_x264 <ffmpeg> <ffprobe> - the guard on the one library the upstream
# build definition does not pin, and therefore the one whose source the written
# offer cannot name from upstream's own record (TOR-98). Nothing at all for a
# stanza with no x264 keys, which is every LGPL platform: those builds contain
# no x264 to account for.
#
# Two halves, because the claim has two halves. The marker ties the lock's
# x264_commit to the binaries in front of us: x264 stamps no revision into what
# it builds - its version.sh derives one from git and the build definition
# unpacks a tarball that has none, which is why upstream's own versions.txt
# says `0.165.x` - but encoder/set.c does compile in a version string with the
# copyright year, and that string moves when x264's source does. So an engineer
# who bumps ffmpeg_sha256 to a rebuilt upstream asset hits this rather than
# leaving a revision behind that describes the previous binary. The second half
# proves the source that revision names is still there and still that tree.
check_x264() {
	[ -n "$x264_commit" ] || return 0

	for binary in "$1" "$2"; do
		LC_ALL=C grep -q -a -F -- "$x264_marker" "$binary" ||
			die "$platform: $(basename "$binary") does not carry the x264 version string of $x264_commit
  looked for $x264_marker
The lock says the x264 linked into this binary is revision $x264_commit
(x264 build $x264_build). Either upstream rebuilt against a different x264 -
in which case the revision has to be identified again, and docs/licensing.md
says how it was done the first time - or this binary has no x264 in it at all,
and the x264_* keys should go. Do not bump them to make this pass."
	done

	verify_x264_source
}

# verify_x264_source - download, or reuse, the x264 source the offer points at,
# and prove it is the tree the lock claims. Both hashes are checked because
# they fail differently: x264_source_sha256 is the archive's own bytes and
# code.videolan.org builds those on demand, so the same commit can arrive
# re-compressed; x264_tree_sha256 is over the contents and is what actually
# has to hold. Same tree under new bytes is a note - the offer is about source,
# not about a gzip - and a different tree is a hard failure, because then the
# offer names something the binary was not built from.
verify_x264_source() {
	mkdir -p "$cache"
	x264_file="$cache/$(basename "$x264_source")"

	if [ ! -f "$x264_file" ] || [ "$(sha256 "$x264_file")" != "$x264_source_sha" ]; then
		echo "$platform: downloading x264 source $(basename "$x264_file")"
		download "$x264_source" "$x264_file.part" ||
			die "$platform: x264 source download failed: $x264_source
The written offer in the macOS archive points at this URL. If it is gone, the
offer needs a mirror before the next release, not a note."
		mv "$x264_file.part" "$x264_file"
	fi

	x264_got=$(sha256 "$x264_file")
	x264_tree_got=$(tree_sha256 "$x264_file" "$cache/.x264-tree")

	if [ "$x264_tree_got" != "$x264_tree_sha" ]; then
		rm -f "$x264_file"
		die "$platform: the x264 source at $x264_commit is not the tree the lock pins
  expected tree $x264_tree_sha
  got           $x264_tree_got
A by-commit archive's contents cannot change, so either the download was
mangled or that URL no longer serves that revision. The download was deleted;
run this again, and if it fails the same way read docs/licensing.md before
touching the lock - this is the source the macOS written offer hands over for
the x264 inside the shipped binary."
	fi

	if [ "$x264_got" != "$x264_source_sha" ]; then
		echo "$platform: NOTE the x264 archive was re-compressed upstream" >&2
		echo "  x264_source_sha256 says $x264_source_sha" >&2
		echo "  the file now hashes to $x264_got" >&2
		echo "  its contents are unchanged ($x264_tree_sha), so this is a" >&2
		echo "  different gzip of the same revision, not different source." >&2
	fi

	echo "$platform: x264 $x264_build source $x264_commit verified"
}

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
	ffprobe_url=$(field "$platform" ffprobe_url)
	ffprobe_archive_sha=$(field "$platform" ffprobe_archive_sha256)
	archive_root=$(field "$platform" archive_root)
	ffmpeg_member=$(field "$platform" ffmpeg_member)
	ffprobe_member=$(field "$platform" ffprobe_member)
	license_member=$(field "$platform" license_member)
	ffmpeg_sha=$(field "$platform" ffmpeg_sha256)
	ffprobe_sha=$(field "$platform" ffprobe_sha256)
	license_sha=$(field "$platform" license_sha256)
	license=$(field "$platform" license)
	# Read only so the required-keys loop below can insist on it: a stanza
	# with no license_file would leave package.sh naming no licence text.
	# shellcheck disable=SC2034
	license_file=$(field "$platform" license_file)
	configure_requires=$(field "$platform" configure_requires)
	configure_forbids=$(field "$platform" configure_forbids)
	version=$(field "$platform" ffmpeg_version)
	x264_commit=$(field "$platform" x264_commit)
	x264_build=$(field "$platform" x264_build)
	x264_source=$(field "$platform" x264_source)
	x264_source_sha=$(field "$platform" x264_source_sha256)
	x264_tree_sha=$(field "$platform" x264_tree_sha256)
	x264_marker=$(field "$platform" x264_marker)

	for required in url archive_sha archive_root ffmpeg_member ffprobe_member \
		license_member ffmpeg_sha ffprobe_sha license_sha version \
		license license_file configure_requires configure_forbids; do
		eval "value=\$$required"
		[ -n "$value" ] || die "$platform: lock is missing $required"
	done

	# A second archive is optional, but half of one is a typo: martin-riedl
	# publishes one zip per executable, BtbN one archive holding both.
	if [ -n "$ffprobe_url" ] && [ -z "$ffprobe_archive_sha" ]; then
		die "$platform: lock has ffprobe_url but no ffprobe_archive_sha256"
	fi
	if [ -z "$ffprobe_url" ] && [ -n "$ffprobe_archive_sha" ]; then
		die "$platform: lock has ffprobe_archive_sha256 but no ffprobe_url"
	fi

	# Same reasoning for the licence text, and it matters more: a stanza that
	# said `license_member = LICENSE.txt` with `license_sha256 = none` would
	# skip no check but would fail with a hash mismatch, which reads like a
	# substitution upstream rather than like the typo it is.
	if [ "$license_member" = none ] && [ "$license_sha" != none ]; then
		die "$platform: license_member is none, so license_sha256 must be too"
	fi
	if [ "$license_member" != none ] && [ "$license_sha" = none ]; then
		die "$platform: license_sha256 is none but license_member names $license_member"
	fi

	# The x264 keys are all-or-nothing for the same reason: only the macOS
	# stanzas carry them, and a set with one key missing would skip half of
	# check_x264 while looking like it ran - a marker with no source to
	# verify, or a source with no marker tying it to the binary.
	x264_keys=0
	for value in "$x264_commit" "$x264_build" "$x264_source" \
		"$x264_source_sha" "$x264_tree_sha" "$x264_marker"; do
		[ -z "$value" ] || x264_keys=$((x264_keys + 1))
	done
	if [ "$x264_keys" -ne 0 ] && [ "$x264_keys" -ne 6 ]; then
		die "$platform: the x264_* keys are all-or-nothing, and this stanza has $x264_keys of 6
Needed together: x264_commit x264_build x264_source x264_source_sha256
x264_tree_sha256 x264_marker (docs/licensing.md, TOR-98)."
	fi

	# Per-platform, because two platforms' archives are both called
	# ffmpeg.zip: one shared cache directory would have darwin-arm64 reuse
	# the darwin-amd64 download and fail its hash for the wrong reason.
	cache="$dest_root/.cache/$platform"

	dest="$dest_root/$platform"
	ffmpeg_out="$dest/$(basename "$ffmpeg_member")"
	ffprobe_out="$dest/$(basename "$ffprobe_member")"

	# Already correct: say so and touch nothing else. A release is cut more
	# than once, and re-downloading 113 MB to prove it is the same 113 MB is
	# a waste of somebody's evening. The configure guard still runs - it is
	# a licence check, not a download step, and it costs a grep. So does the
	# x264 guard: its 1 MB is not what the fast path exists to avoid, and a
	# release cut from a warm tree should still have checked that the source
	# its own written offer points at is reachable.
	if [ -f "$ffmpeg_out" ] && [ -f "$ffprobe_out" ] &&
		[ "$(sha256 "$ffmpeg_out")" = "$ffmpeg_sha" ] &&
		[ "$(sha256 "$ffprobe_out")" = "$ffprobe_sha" ]; then
		check_configuration "$ffmpeg_out"
		check_configuration "$ffprobe_out"
		check_x264 "$ffmpeg_out" "$ffprobe_out"
		echo "$platform: ffmpeg $version already present and verified"
		return 0
	fi

	mkdir -p "$cache" "$dest"

	tmp="$dest/.unpack"
	rm -rf "$tmp"
	mkdir -p "$tmp"

	# What comes out of the first archive: ffmpeg always, ffprobe unless it
	# has an archive of its own, and the licence text unless upstream ships
	# none. Members never contain spaces, so one string is a list.
	members=$(member_path "$ffmpeg_member")
	[ -n "$ffprobe_url" ] || members="$members $(member_path "$ffprobe_member")"
	[ "$license_member" = none ] || members="$members $(member_path "$license_member")"

	get_archive "$url" "$archive_sha"
	# shellcheck disable=SC2086
	unpack "$archive" "$tmp" $members

	if [ -n "$ffprobe_url" ]; then
		get_archive "$ffprobe_url" "$ffprobe_archive_sha"
		unpack "$archive" "$tmp" "$(member_path "$ffprobe_member")"
	fi

	# The licence text is checked first: if the build is no longer the one we
	# chose, nothing else about it is worth verifying. `none` is not a way to
	# opt out of that - it says the upstream archive carries no licence text
	# at all (martin-riedl's zips hold the executable and nothing else), and
	# check_configuration below is then the whole guard.
	if [ "$license_member" = none ]; then
		echo "$platform: upstream archive carries no licence text; $license is checked from the configure line"
	else
		got=$(sha256 "$tmp/$(member_path "$license_member")")
		if [ "$got" != "$license_sha" ]; then
			rm -rf "$tmp"
			die "$platform: the licence text inside the archive changed
  expected $license_sha
  got      $got
The lock says this platform ships a $license build
(docs/licensing.md). Read the new text before touching the lock - this is the
guard against an asset quietly becoming a differently licensed build."
		fi
	fi

	for pair in "ffmpeg $ffmpeg_member $ffmpeg_sha" "ffprobe $ffprobe_member $ffprobe_sha"; do
		# shellcheck disable=SC2086
		set -- $pair
		tool=$1
		member=$2
		want=$3
		got=$(sha256 "$tmp/$(member_path "$member")")
		if [ "$got" != "$want" ]; then
			rm -rf "$tmp"
			die "$platform: $tool sha256 mismatch
  expected $want
  got      $got"
		fi
		check_configuration "$tmp/$(member_path "$member")"
	done

	check_x264 "$tmp/$(member_path "$ffmpeg_member")" \
		"$tmp/$(member_path "$ffprobe_member")"

	mv -f "$tmp/$(member_path "$ffmpeg_member")" "$ffmpeg_out"
	mv -f "$tmp/$(member_path "$ffprobe_member")" "$ffprobe_out"
	if [ "$license_member" != none ]; then
		mv -f "$tmp/$(member_path "$license_member")" "$dest/LICENSE.txt"
	fi
	chmod +x "$ffmpeg_out" "$ffprobe_out"

	# The downloaded archives have no reader left. Measured (TOR-103): the
	# cache held 380 MiB beside 748 MiB of extracted binaries, and moving it
	# away changed nothing - every platform still answered "already present
	# and verified", because the fast path above hashes the EXTRACTED files
	# and never looks here. Keeping a third of a gigabyte for a case that
	# cannot arise is not a cache, it is a leak.
	#
	# The x264 source tarball is deliberately not touched: it is ~1 MiB, it
	# is never extracted into anything lasting, and without it every fetch
	# re-downloads it.
	rm -f "$cache/$(basename "$url")"
	[ -z "$ffprobe_url" ] || rm -f "$cache/$(basename "$ffprobe_url")"
	rm -rf "$tmp"

	# A copy of the provenance travels with the binaries, so a
	# third_party/ffmpeg tree found on a machine months later can still say
	# what it holds - including which licence, which is no longer the same
	# answer on every platform. The lock stays the source of truth.
	{
		echo "# written by scripts/fetch-ffmpeg.sh - do not edit"
		printf '%-22s = %s\n' "platform" "$platform"
		for key in source release_tag build_scripts variant license \
			license_file ffmpeg_version ffmpeg_commit ffmpeg_source \
			url archive_sha256 ffprobe_url ffprobe_archive_sha256 \
			configure_requires configure_forbids x264_build \
			x264_commit x264_source x264_source_sha256 \
			x264_tree_sha256; do
			value=$(field "$platform" "$key")
			[ -n "$value" ] || continue
			printf '%-22s = %s\n' "$key" "$value"
		done
		printf '%-22s = %s\n' "ffmpeg_sha256" "$ffmpeg_sha"
		printf '%-22s = %s\n' "ffprobe_sha256" "$ffprobe_sha"
		printf '%-22s = %s\n' "license_sha256" "$license_sha"
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
