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
# Two platforms carry one extra thing each, and both are in the archive because
# the platform cannot do without them: Linux a systemd unit for a seedbox slot
# (REQUIREMENTS 4.1), macOS a first-run launcher, because there a downloaded
# torpeek is stopped by Gatekeeper before it can say so itself (TOR-97).
#
# A platform with no bundled ffmpeg is skipped, loudly, and the script exits
# non-zero at the end. Three archives out of four must not be able to pass for
# a complete release (docs/licensing.md).
#
# The licence the archives travel under is not the same on every platform:
# Linux and Windows bundle an LGPL ffmpeg, macOS a GPL one (TOR-93). Every
# sentence the generated README.txt and THIRD-PARTY-NOTICES.md say about it is
# therefore branched on the lock's own `license` field, and an unrecognised
# value stops the release. A notices file that misnames the licence of what it
# ships is worse than no notices file at all.

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
	# One extra line in the file list on the platform that has a seedbox
	# directory. Computed here rather than as a $( case ) inside the heredoc
	# below: that does not parse, and it fails by writing the case statement
	# itself into README.txt.
	seedbox_line=""
	case $2 in
	linux-*)
		seedbox_line=$(printf '\n  %-22s %s' "seedbox/" "systemd unit and guide for a managed slot with no root")
		;;
	esac

	# The macOS archive carries a launcher, and it is the first thing a person
	# there has to run rather than an extra (TOR-97), so it is listed directly
	# under torpeek instead of at the end of the folder list.
	firstrun_line=""
	case $2 in
	darwin-*)
		firstrun_line=$(printf '\n  %-22s %s' "first-run.command" "clears the download flag and starts torpeek")
		;;
	esac

	# A section of its own, above RUNNING IT rather than below it, because on
	# macOS it has to be read before the first command is typed and a note
	# printed after the commands is a note read after them (TOR-97). The
	# leading and trailing newline in the value are load-bearing: with them
	# the section gets its blank line on either side, and empty - which is
	# every other platform - the same heredoc line renders as exactly the one
	# blank line that already separated the two sections.
	first_note=""

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
	darwin-*)
		invoke="./torpeek"
		unpack_note="Some unpacking tools drop the execute bit. If the shell says
\"permission denied\": chmod +x torpeek ffmpeg ffprobe. first-run.command does
that too, so if you started there you will not meet this."
		first_note="
FIRST RUN ON macOS

  sh first-run.command

Run that once, in this folder, before anything else. macOS marks every file a
browser downloads, unpacking carries the mark onto these files, and torpeek's
own binary carries no Apple Developer ID signature and is not notarised - so a
marked torpeek is stopped by the system on its first run before it prints
anything at all. The terminal shows no error: the process sits there for a few
seconds and dies, and macOS may put a dialog on screen about a program it
cannot check. Nothing torpeek could print would help, because it is stopped
before any of its own code runs. The script clears the mark from this folder,
checks that it is gone, and only then starts torpeek.

The order is why this is a script and not a line to remember. Clearing the mark
before the first attempt works; clearing it afterwards may not, because macOS
can remember a refused launch per file. If you have already run ./torpeek and
nothing happened, unpack the archive again into a new folder and run the line
above there first.

By hand it is \`xattr -c torpeek ffmpeg ffprobe\`, before the first ./torpeek.
Double-clicking first-run.command in Finder does what the line above does, but
the script is a downloaded file too, so macOS may stop it in turn - typing the
line always works, because the shell reads the script instead of launching it.
The bundled ffmpeg and ffprobe are signed by their builder and start either
way.
"
		;;
	*)
		invoke="./torpeek"
		unpack_note="Some unpacking tools drop the execute bit. If the shell says
\"permission denied\": chmod +x torpeek ffmpeg ffprobe."
		;;
	esac

	# The LICENCE section below is the one part of this file that is not the
	# same on every platform, because what the archive travels under is not:
	# LGPL on Linux and Windows, GPL on macOS (TOR-93). Computed here for the
	# same reason as seedbox_line - a $( case ) inside the heredoc writes the
	# case statement into README.txt instead of running it.
	case $(field "$2" license) in
	LGPL-3.0-or-later)
		licence_note="The ffmpeg and ffprobe here are LGPL builds, redistributed unmodified.
THIRD-PARTY-NOTICES.md names the exact upstream build and commit, carries the
written offer for the corresponding source, and points at the licence texts in
licenses/. torpeek itself is a separate program that runs them as child
processes; it does not link against them, which is why the two licences sit
side by side here rather than one covering the other."
		;;
	GPL-3.0-or-later)
		licence_note="The ffmpeg and ffprobe here are GPL builds, redistributed unmodified, and this
whole folder is passed on to you under the GNU GPL version 3 or later. Run it
for any purpose, study it, copy it, change it and pass it on - under that same
licence, and with THIRD-PARTY-NOTICES.md alongside, because the written offer
for the complete corresponding source lives in it. That file also names the
exact upstream build and commit and points at the licence texts in licenses/.

torpeek's own source code is MIT and stays MIT: the text is in LICENSE, and MIT
source may travel inside a GPL-licensed whole. What the GPL covers is this
folder, not the project.

The Linux and Windows archives of this release bundle an LGPL ffmpeg instead
and are not under the GPL. macOS is the only platform with no LGPL build worth
shipping, and there was no reason to take rights away from everyone else to
make one sentence cover all three."
		;;
	*)
		die "$2: no README wording for licence '$(field "$2" license)'"
		;;
	esac
	cat >"$1/README.txt" <<EOF
torpeek $version - $2

Preview a video torrent without downloading it: frames spread across each
video file, plus a technical summary.

WHAT IS IN THIS FOLDER

$(printf '  %-22s %s\n' \
		"torpeek$exe" "the program - this is the only thing you run")$firstrun_line
$(printf '  %-22s %s\n' \
		"ffmpeg$exe" "frame decoding, used by torpeek" \
		"ffprobe$exe" "container inspection, used by torpeek" \
		"README.txt" "this file" \
		"LICENSE" "torpeek's own licence (MIT)" \
		"THIRD-PARTY-NOTICES.md" "what ffmpeg is, where it came from, its licence" \
		"licenses/" "the full licence texts those notices refer to")$seedbox_line

Nothing has to be installed. Keep the three executables in the same folder:
torpeek looks for ffmpeg and ffprobe next to itself before it falls back to
whatever the machine has on PATH, and the copies here are the ones this build
was tested with.
$first_note
RUNNING IT

  $invoke -n 6 -out ./frames some.torrent
  $invoke -web

The first writes six frames from each video file in the torrent into ./frames.
The second serves the web UI and opens a browser at it. A magnet URI works
anywhere a .torrent path does. \`$invoke -help\` lists every flag.

$unpack_note

LICENCE

torpeek is MIT; the text is in LICENSE.

$licence_note
EOF
}

write_notices() {
	# $1 archive directory, $2 platform
	platform=$2
	licence=$(field "$platform" license)
	licence_file=$(field "$platform" license_file)

	# Four paragraphs differ per platform, and each of them would be a lie on
	# the other one: which text is the licence, what the build has configured
	# in, what that obliges the person holding the archive to do, and whether
	# torpeek's own aggregation argument is the end of the story. Branched on
	# the lock's `license` rather than assumed, and an unrecognised value
	# stops the release instead of defaulting to the older LGPL wording,
	# because that default is exactly the defect this file cannot afford
	# (TOR-93).
	case $licence in
	LGPL-3.0-or-later)
		licence_para="**Licence: $licence.** Full text in
\`licenses/ffmpeg/$licence_file\`; the GNU GPL v3, which the LGPL v3
incorporates by reference, is in \`licenses/ffmpeg/COPYING.GPLv3\`. FFmpeg's own
summary of which of its files are under which licence is in
\`licenses/ffmpeg/LICENSE.md\`."
		build_para="These are **LGPL** builds: FFmpeg's GPL-only components (libx264, libx265, the
GPL filters) are not configured in. They are redistributed **unmodified**, byte
for byte as published by the upstream builder."
		offer_extra=""
		aggregation_para="torpeek is a separate program. It never links FFmpeg: it runs \`ffmpeg\` and
\`ffprobe\` as child processes and talks to them over their command line, stdout
and stderr (see \`internal/ffmpeg\`). Bundling the two executables in one
folder is aggregation, not combination, so nothing here constrains torpeek's
own licence."
		;;
	GPL-3.0-or-later)
		licence_para="**Licence: $licence.** Full text in
\`licenses/ffmpeg/$licence_file\`. The GNU LGPL v3 is in
\`licenses/ffmpeg/COPYING.LGPLv3\` and still applies to the parts of FFmpeg
that are not GPL-only, so those keep their LGPL rights inside this GPL whole.
FFmpeg's own summary of which of its files are under which licence is in
\`licenses/ffmpeg/LICENSE.md\`."
		build_para="These are **GPL** builds: FFmpeg's GPL-only components - libx264, libx265 and
the GPL filters - **are** configured in. They are redistributed **unmodified**,
byte for byte as published by the upstream builder.

**What that means for you.** This archive is passed on to you as a whole under
the **GNU GPL version 3 or later**. You may run it for any purpose, study it,
copy it, change it and pass it on, provided you pass it on under that same
licence and hand on the written offer below with it.

torpeek's own source code is **MIT** and stays MIT - the text is in
\`LICENSE\`. MIT is GPL-compatible, so MIT source may travel inside a
GPL-licensed whole; what the GPL covers here is this archive, not the project's
repository.

**The other platforms' archives are not under the GPL.** The Linux and Windows
archives of torpeek $version bundle an LGPL ffmpeg. macOS is the only platform
with no LGPL build worth shipping (\`docs/licensing.md\`), and downgrading
everybody else's rights to make one sentence cover all three would have bought
nothing."
		offer_extra="- any patches the builder applies on top of that commit before configuring and
  compiling, together with the scripts that control compilation and
  installation. Both are part of the build definition named above, and this
  offer covers them as much as it covers FFmpeg itself.
"
		aggregation_para="torpeek is a separate program. It never links FFmpeg: it runs \`ffmpeg\` and
\`ffprobe\` as child processes and talks to them over their command line, stdout
and stderr (see \`internal/ffmpeg\`). Bundling the two executables in one
folder is aggregation, not combination, which is why torpeek's own source stays
MIT and its repository is unaffected by what travels beside it here.

This *archive*, though, is offered to you under the GPL as a whole. MIT permits
that, and it means nobody holding the folder has to work out for themselves
where the aggregate ends: redistribute this folder under the GPL, and take
torpeek's source from its repository under MIT."
		;;
	*)
		die "$platform: no notices wording for licence '$licence'"
		;;
	esac

	# The offer's second bullet - "how every library configured into it is
	# obtained and built" - reaches every library in these binaries but one.
	# x264 is the dependency upstream's build definition takes from a moving
	# `master` URL instead of from a pinned version, so the revision that
	# actually got compiled is recorded in our own lock and nowhere upstream,
	# and the offer has to name it from there or not at all (TOR-98). A stanza
	# with no x264 keys emits nothing: those are the LGPL builds, and they
	# contain no x264 to account for.
	if [ -n "$(field "$platform" x264_commit)" ]; then
		offer_extra="$offer_extra- x264, at revision \`$(field "$platform" x264_commit)\`
  (x264 build $(field "$platform" x264_build)) - the one library the build definition above takes
  from a moving \`master\` URL rather than from a pinned version, and therefore
  the one this project pins itself:
  $(field "$platform" x264_source)
  sha256 \`$(field "$platform" x264_source_sha256)\`.
  How that revision was identified, and what the evidence does and does not
  establish, is recorded in the project's \`docs/licensing.md\`.
"
	fi

	# ffprobe usually rides in the same archive as ffmpeg and needs no row of
	# its own; martin-riedl publishes one zip per executable, and a provenance
	# table that named only the first would leave half of what is in the
	# folder unaccounted for.
	ffprobe_archive_rows=""
	if [ -n "$(field "$platform" ffprobe_url)" ]; then
		# The backticks are markdown, not command substitution.
		# shellcheck disable=SC2016
		ffprobe_archive_rows=$(printf '\n| upstream archive (ffprobe) | %s |\n| archive sha256 (ffprobe) | `%s` |' \
			"$(field "$platform" ffprobe_url)" \
			"$(field "$platform" ffprobe_archive_sha256)")
	fi

	cat >"$1/THIRD-PARTY-NOTICES.md" <<EOF
# Third-party notices - torpeek $version ($platform)

This archive contains software other than torpeek. This file says what, under
which licence, and where its source is.

## FFmpeg (ffmpeg, ffprobe)

$licence_para

$build_para

| | |
|---|---|
| version | \`$(field "$platform" ffmpeg_version)\` |
| FFmpeg commit | \`$(field "$platform" ffmpeg_commit)\` |
| prebuilt by | $(field "$platform" source), release \`$(field "$platform" release_tag)\` |
| build variant | $(field "$platform" variant) |
| upstream archive | $(field "$platform" url) |
| archive sha256 | \`$(field "$platform" archive_sha256)\` |$ffprobe_archive_rows
| ffmpeg sha256 | \`$(field "$platform" ffmpeg_sha256)\` |
| ffprobe sha256 | \`$(field "$platform" ffprobe_sha256)\` |

### Written offer for the corresponding source

The complete corresponding source code for the FFmpeg build in this archive is:

- FFmpeg itself, at commit \`$(field "$platform" ffmpeg_commit)\`:
  $(field "$platform" ffmpeg_source)
- the build definition that produced this binary, including how every library
  configured into it is obtained and built: $(field "$platform" build_scripts)
$offer_extra
For a period of three years from the date this archive was distributed, and on
request, the torpeek project will provide a complete machine-readable copy of
that source on a physical medium or by equivalent means, for no more than the
cost of performing the distribution. Open an issue at
https://github.com/madmurdok/torpeek to ask.

## torpeek

$aggregation_para

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
	# All three texts on every platform, whichever of them is *the* licence
	# there. An LGPL v3 archive needs the GPL v3 too, because LGPL v3
	# incorporates it by reference; a GPL v3 archive needs the LGPL v3 too,
	# because the parts of FFmpeg that are not GPL-only stay LGPL and their
	# recipients keep LGPL rights in them. Which one governs the archive as a
	# whole is what the lock's `license_file` names and the notices state -
	# not which files were copied (TOR-93).
	cp "$root/packaging/licenses/ffmpeg/COPYING.LGPLv3" \
		"$root/packaging/licenses/ffmpeg/COPYING.GPLv3" \
		"$root/packaging/licenses/ffmpeg/LICENSE.md" \
		"$stage/licenses/ffmpeg/"

	# torpeek's own licence, at the archive root rather than under licenses/,
	# which is for what the archive carries on somebody else's behalf. A
	# recipient of an unlicensed program has no rights at all, so this file
	# is not optional decoration (TOR-92).
	cp "$root/LICENSE" "$stage/LICENSE"

	# Linux additionally carries what a seedbox deployment needs, which
	# REQUIREMENTS 4.1 asks for by name: "the same files as the desktop
	# archive, plus a systemd unit and instructions" (TOR-31). Linux only -
	# a systemd unit on Windows is noise, and docs/seedbox.md's steps are
	# systemd's and nginx's.
	case "$platform" in
	linux-*)
		mkdir -p "$stage/seedbox"
		cp "$root/packaging/systemd/torpeek.service" \
			"$root/packaging/systemd/torpeek.env.example" \
			"$root/docs/seedbox.md" \
			"$stage/seedbox/"
		;;
	# macOS only, and not decoration: a quarantined torpeek is stopped before
	# any of its own code runs, so it cannot report the one thing that would
	# fix it. Something else has to run first, and this is that something -
	# it clears the flag from the folder, proves it is gone and then launches
	# torpeek, which is an order nobody can get wrong (TOR-97). The generated
	# README.txt above leads with it.
	darwin-*)
		cp "$root/packaging/macos/first-run.command" "$stage/first-run.command"
		chmod +x "$stage/first-run.command"
		;;
	esac

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

	# The staging directory has done its job once the archive exists, and it
	# is not small: four platforms left 869 MiB of it behind after a release
	# build, a byte-for-byte second copy of what the archives already hold
	# (TOR-103). The archive is the artefact to inspect - `tar tzf` and
	# `unzip -l` read it without unpacking, and archivecheck/ unpacks it the
	# way a person would rather than reaching in here.
	#
	# KEEP_STAGE=1 keeps it, for the one case the deletion would cost
	# something: comparing two builds file by file, where re-running
	# packaging to get the tree back is slower than having kept it.
	if [ "${KEEP_STAGE:-0}" = 1 ]; then
		kept=" (staging kept: $stage)"
	else
		rm -rf "$stage"
		kept=""
	fi

	printf '%s: %s unpacked, %s as %s%s\n' \
		"$platform" "$(mib "$unpacked")" "$(mib "$(bytes "$archive")")" \
		"$(basename "$archive")" "$kept"
done

# Every archive of this version that is on disk, not only the ones this run
# rebuilt. Packaging one platform at a time is normal - a platform whose
# upstream build has moved gets retried alone - and a checksum file that forgot
# the other platforms each time would be worse than none.
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
