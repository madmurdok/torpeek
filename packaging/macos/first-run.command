#!/bin/sh
#
# First run on macOS: clear the download flag from this folder, then start
# torpeek.
#
# Why this file exists (TOR-97). torpeek's own binary carries no Apple
# Developer ID signature, and a browser marks everything it downloads with
# com.apple.quarantine - an attribute `tar -xzf` copies onto every file it
# unpacks, so unpacking in Terminal does not shed it. The first execution of a
# marked, unsigned binary is not refused with a message. Measured on macOS
# 26.6.2 Intel: AppleSystemPolicy holds the process before one instruction of
# torpeek's own code runs and then SIGKILLs it - a binary whose very first
# statement writes to stderr printed nothing, sat at 0% CPU with an 8 KB
# resident set, and died of signal 9 after a few seconds. The same bytes with
# the flag cleared printed immediately.
#
# So torpeek cannot explain this itself: it never reaches main(). Only
# something that runs *before* it can, which is what this script is for. A
# startup check inside the binary would be visible to exactly the people whose
# run was not stopped.
#
# Order is the whole point, and is why this is a script rather than a line in
# README.txt. Clearing the flag before the first attempt works. Clearing it
# afterwards may not - macOS can remember a refused launch per file, and then
# only a freshly unpacked copy starts (TOR-93 measured that; this machine's
# later runs did start after a late clear, so it is "may", not "does"). A
# person with one thing to run cannot get the order wrong.
#
# Run it as `sh first-run.command` from this folder. Double-clicking it in
# Finder is the same idea, but this script is itself a downloaded file, so
# Finder launching it runs into the very check the script exists to clear;
# `sh` reads it as data instead, which quarantine does not gate.

set -u

# Double-clicking in Finder starts Terminal in the user's home directory, not
# here, so the folder is derived from the script's own path rather than assumed.
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
cd "$here" || exit 1

die() {
	printf 'first-run: %s\n' "$1" >&2
	exit 1
}

exes="torpeek ffmpeg ffprobe"

for exe in $exes; do
	[ -f "$exe" ] || die "no $exe in $here.
Run this from the folder the archive unpacked into, with all three executables
still in it."
done

command -v xattr >/dev/null 2>&1 || die "this macOS has no xattr command, so the download flag cannot be cleared with it.
Copying each executable through a pipe sheds the flag too, because the copy is
a new file:

  for f in torpeek ffmpeg ffprobe; do cat \"\$f\" > \"\$f.new\" && mv \"\$f.new\" \"\$f\"; done
  chmod +x torpeek ffmpeg ffprobe"

# -r covers the licence texts as well, which do not need it - one command over
# the folder is easier to reason about than a list, and clearing an attribute
# from a text file costs nothing. Errors are discarded because xattr complains
# once per file that never carried the attribute, and on a folder built locally
# that is every file. What matters is the state afterwards, checked next: a
# command that reported success is not the same as a flag that is gone.
xattr -d -r com.apple.quarantine . 2>/dev/null

still=
for exe in $exes; do
	if xattr -p com.apple.quarantine "$exe" >/dev/null 2>&1; then
		still="$still $exe"
	fi
done
[ -z "$still" ] || die "the download flag is still set on:$still
Clearing it needs write access to those files. Copy the whole folder somewhere
you own - your home directory, not a read-only disk image - and run this again."

# Some unpacking tools drop the execute bit. That is the other way a first run
# fails on macOS, it has the same one-command fix, and a person who was handed
# one script to run should not have to meet it separately.
for exe in $exes; do
	[ -x "$exe" ] || chmod +x "$exe" 2>/dev/null ||
		die "cannot make $exe executable. Copy the folder somewhere you own and run this again."
done

# A double-click passes no arguments, and -web is the mode that needs none: it
# serves the UI and opens a browser at it, so the flag is cleared and there is
# something to look at in one action. Anything typed after the script name is
# passed through instead, so `sh first-run.command -n 6 -out ./frames x.torrent`
# is a cleared first run of the command line.
if [ "$#" -eq 0 ]; then
	set -- -web
fi

printf 'first-run: download flag cleared; starting ./torpeek %s\n' "$*"
./torpeek "$@"
status=$?

# 128 + SIGKILL. Reaching this means the flag was cleared and macOS stopped
# torpeek anyway, which is the remembered-refusal case: the flag is gone, but
# the decision about this particular file is not. The same bytes in a freshly
# unpacked folder do start.
if [ "$status" -eq 137 ]; then
	cat >&2 <<'EOF'
first-run: macOS killed torpeek even though the download flag is now clear.
That happens when this copy was already launched once while the flag was set:
the refusal is remembered per file, and clearing it afterwards does not undo
it. Unpack the archive again into a new folder and run this script there
before running torpeek.
EOF
fi

exit "$status"
