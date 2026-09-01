// Package bridge publishes a torrent's file as an ordinary HTTP resource on
// loopback, so ffmpeg and ffprobe can read it with Range requests while the
// data is still being fetched from the swarm.
//
// It is the single point where an external process can stall the run, so every
// request carries its own timeout and a context cancelled with the run.
//
// Requirements: sections 2.4 and 4.1.
package bridge
