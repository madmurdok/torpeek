package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/torpeek/torpeek/internal/ffmpeg"
	"github.com/torpeek/torpeek/internal/probe"
	"github.com/torpeek/torpeek/internal/swarm"
)

// ErrorCode is a stable, machine-readable name for a failure.
//
// These values are part of the interface: the CLI turns them into exit codes,
// the UI into messages, and a future API would put them in a JSON body. They
// are written down here once so a rewording of an error message cannot change
// what a client matches on.
type ErrorCode string

const (
	// CodeNoMetadata: the swarm never sent the torrent's info.
	CodeNoMetadata ErrorCode = "no_metadata"
	// CodeNoPeers: nobody to ask for data.
	CodeNoPeers ErrorCode = "no_peers"
	// CodePrivacyUnresolvable: a magnet with no trackers, where fetching
	// metadata would mean using DHT before knowing whether that is allowed.
	CodePrivacyUnresolvable ErrorCode = "privacy_unresolvable"
	// CodeNoVideo: the torrent holds nothing worth taking frames from.
	CodeNoVideo ErrorCode = "no_video_files"
	// CodeUnprobeable: ffprobe could not make sense of the container, so
	// there is no index to seek with.
	CodeUnprobeable ErrorCode = "unprobeable"
	// CodeUnavailable: the pieces needed are not held by any connected peer.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeDecodeFailed: ffmpeg read the data but produced no usable frame.
	CodeDecodeFailed ErrorCode = "decode_failed"
	// CodeSeekFailed: a frame was decoded, but nowhere near the moment that
	// was asked for, so it answers a question nobody put.
	CodeSeekFailed ErrorCode = "seek_failed"
	// CodeToolMissing: ffmpeg or ffprobe could not be found.
	CodeToolMissing ErrorCode = "tool_missing"
	// CodeBudgetExhausted: a time or traffic limit stopped the run.
	CodeBudgetExhausted ErrorCode = "budget_exhausted"
	// CodeCancelled: the caller stopped the run.
	CodeCancelled ErrorCode = "cancelled"
	// CodeStorage: writing results failed.
	CodeStorage ErrorCode = "storage"
	// CodeInternal: anything unclassified. A client should show the message.
	CodeInternal ErrorCode = "internal"
)

// Error pairs a code with the underlying failure.
type Error struct {
	Code ErrorCode
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %v", e.Code, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Fail wraps an error with a code.
func Fail(code ErrorCode, err error) *Error {
	return &Error{Code: code, Err: err}
}

// CodeOf classifies an error from anywhere in the pipeline.
//
// Classification lives here rather than at each call site so that a new error
// from a lower layer surfaces as CodeInternal - visible and unclassified -
// instead of being quietly mislabelled as something a client would act on.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}

	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}

	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeCancelled
	case errors.Is(err, swarm.ErrNoMetadata):
		return CodeNoMetadata
	case errors.Is(err, swarm.ErrPrivacyUnresolvable):
		return CodePrivacyUnresolvable
	case errors.Is(err, probe.ErrNoIndex):
		return CodeUnprobeable
	case errors.Is(err, ffmpeg.ErrNotFound):
		return CodeToolMissing
	}

	var exitErr *ffmpeg.ExitError
	if errors.As(err, &exitErr) {
		return CodeDecodeFailed
	}

	return CodeInternal
}
