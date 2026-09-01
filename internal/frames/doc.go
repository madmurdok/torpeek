// Package frames plans capture points and turns them into images: even spacing
// inside a configurable window of the duration, blank frame rejection, shifting
// away from pieces the swarm cannot serve, and the min-time / min-traffic
// profiles. Decoding happens by invoking the bundled ffmpeg against a bridge
// URL, never by linking a decoder.
//
// Requirements: sections 2.3, 2.5 and 4.
package frames
