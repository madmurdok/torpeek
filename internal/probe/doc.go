// Package probe wraps ffprobe. It answers two questions about a file published
// by the bridge: what is in it (duration, audio and subtitle tracks, quality
// figures) and where a keyframe for a given timestamp sits in bytes.
//
// This is deliberately not a container parser. ffprobe reports the byte
// position of a keyframe for any container ffmpeg understands, which is why
// torpeek carries no MP4/MKV/AVI index code of its own - see ARCHITECTURE.md.
//
// Requirements: sections 2.4, 2.7 and 2.8.
package probe
