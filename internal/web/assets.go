package web

import "embed"

// embedded is the frontend, compiled into the binary.
//
// Reading it from disk would break the one thing this has to be: a folder
// holding the binary and ffmpeg, and nothing else (REQUIREMENTS.md sections
// 3.3 and 4).
//
//go:embed assets
var embedded embed.FS
