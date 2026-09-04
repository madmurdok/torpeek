# Running torpeek on a seedbox

The target is a managed slot — Ultra.cc and the hosts shaped like it — where
there is no root, Docker is forbidden, files live only inside `$HOME`, and
every listening port must come from a range allocated to the account.
REQUIREMENTS.md §4.1 treats that as a first-class environment rather than a
degraded desktop, for a plain reason: the box has a gigabit line, and
checking a torrent is best done where it will be downloaded.

Nothing below needs privileges. What you end up with is one static binary in
`~/bin`, a `systemd --user` unit, and an nginx snippet the host already reads.

## What you need first

- **A Linux amd64 release archive.** `make archives` builds it; it holds
  `torpeek`, `ffmpeg`, `ffprobe` and the licence material. The bundled ffmpeg
  is a glibc build, so a musl distro (Alpine) will run `torpeek` but not it.
- **Three ports from your allocated range.** The web UI, BitTorrent, and the
  internal HTTP bridge. The bridge listens on loopback only, but it is a
  listening port all the same — take it from the range like the others.
- **Your nginx `proxy.d` directory**, usually `~/.apps/nginx/proxy.d/`. The UI
  is served under a subdirectory, e.g. `https://user.host.usbx.me/torpeek`.

## 1. Put the folder in `~/bin`

```sh
mkdir -p ~/bin
tar xzf torpeek-0.9.0-linux-amd64.tar.gz
cp torpeek-0.9.0-linux-amd64/{torpeek,ffmpeg,ffprobe} ~/bin/
```

All three together, deliberately: torpeek looks for `ffmpeg` and `ffprobe`
next to its own executable before it falls back to `PATH`, and the copies in
the archive are the ones that build was tested with. A `PATH` ffmpeg on a
managed host is whatever the provider installed, if anything.

## 2. Fill in the environment file

```sh
mkdir -p ~/.config/torpeek
cp torpeek-0.9.0-linux-amd64/torpeek.env.example ~/.config/torpeek/env
chmod 600 ~/.config/torpeek/env
$EDITOR ~/.config/torpeek/env
```

Generate the token rather than inventing one:

```sh
head -c 24 /dev/urandom | base64 | tr -d '=+/'
```

It goes in `TORPEEK_WEB_TOKEN`. Pinning it is the point of putting it here at
all: left unset, torpeek generates a fresh token every start — correct for an
interactive run, useless for a service that restarts on its own while nobody
is reading its log for the new URL.

`chmod 600` because this file is the credential. The unit reads it rather
than carrying the token in its own text, which `systemctl cat` prints.

## 3. Install the unit

```sh
mkdir -p ~/.config/systemd/user
cp packaging/systemd/torpeek.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now torpeek
systemctl --user status torpeek
```

Then, and this is the step that catches people:

```sh
loginctl enable-linger "$USER"
```

Without lingering, your `--user` units are killed when your last SSH session
ends, which looks exactly like a crash you cannot reproduce. Some hosts
enable it for every account already and some require asking support.

## 4. Point nginx at it

Write `~/.apps/nginx/proxy.d/torpeek.conf`:

```nginx
location /torpeek/ {
    proxy_pass http://127.0.0.1:9123/torpeek/;   # TORPEEK_WEB_PORT

    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # The event stream is a WebSocket. Without these two headers the page
    # loads, shows nothing, and never says why.
    proxy_set_header Upgrade    $http_upgrade;
    proxy_set_header Connection "upgrade";

    # A run can sit quiet for minutes while pieces arrive. nginx's default
    # read timeout is 60s, which would drop the socket mid-run and leave the
    # page reconnecting in a loop.
    proxy_read_timeout 3600s;

    # Frames should appear as they land, not in a batch when the response
    # ends.
    proxy_buffering off;
}
```

Reload nginx the way your host documents it (on Ultra.cc,
`app-nginx restart`). Then open `https://user.host.usbx.me/torpeek/?token=…`.

The trailing slashes in both `location` and `proxy_pass` matter. Everything
the page requests is relative to its base path, so the mount point and
`TORPEEK_BASE_PATH` have to name the same prefix.

## 5. Check it from the outside

```sh
systemctl --user status torpeek
journalctl --user -u torpeek -n 50
curl -sS -o /dev/null -w '%{http_code}\n' "https://user.host.usbx.me/torpeek/runs?token=$TOKEN"
```

`200` with the token and `401` without it is the pair worth seeing: the
static page is served to anyone deliberately (a login form nobody can load
protects nothing), and every route that reads or changes anything is behind
the token.

## Why the unit looks the way it does

- **`-parallel 2`, not the desktop default of 4.** A shared slot's IO is a
  neighbour's IO, and these hosts recommend one to three active downloads.
  §4.1 makes this a setting rather than a constant for exactly this reason.
- **`-torrent-port` is set explicitly.** The torrent library would otherwise
  take an OS-assigned port, which is the allocation rule broken most easily
  and least visibly — nothing fails, you are simply using a port that is not
  yours.
- **`-web-host 127.0.0.1`.** nginx is on this same host; the UI never needs
  to be reachable directly.
- **`-cache-max-size`.** The output tree grows only from runs, and the disk it
  grows on is someone else's quota. Unset means no eviction at all, which is
  the wrong default here.
- **Every variable is `${BRACED}`.** An unset `$BARE` expands to no argument
  at all and silently shifts the following flag onto the wrong value;
  `${BRACED}` expands to one empty argument, and each of these flags treats
  empty as "not set".

## What has not been verified

The unit's exact command line was run and checked locally — it starts, serves
under a base path, answers `200` on `/runs` with the token and `401` without
it, and tolerates an empty `TORPEEK_CACHE_MAX_SIZE` and `TORPEEK_WATCH_DIR`.
The rest of this guide has **not** been walked on a real slot: there is no
seedbox account behind this repository, so the nginx snippet, the lingering
step and the port-range rules are written from the hosts' own documentation
and from how systemd behaves, not from having watched it work end to end
(TOR-31). If you follow it on a real slot, the parts most likely to need a
correction are the nginx reload command and the `proxy.d` path, both of which
differ between providers.
