package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
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
	// CodeNoPortAvailable: every BitTorrent port the operator allocated is
	// held by another client, so this torrent cannot have one of its own.
	// Distinct from CodeInternal because it is not a fault: it is the
	// hosting environment's limit showing through (REQUIREMENTS.md 4.1), and
	// what a person does about it - allocate more ports, or wait for a
	// private torrent to finish - is nothing like what they do about a bug.
	CodeNoPortAvailable ErrorCode = "no_port_available"
	// CodeTorrentBusy: this torrent is already attached to another run.
	// Distinct from CodeInternal for the same reason CodeNoPortAvailable is:
	// it is not a fault but a refusal, and what a person does about it -
	// wait for the other run, or cancel it - is nothing like what they do
	// about a bug.
	CodeTorrentBusy ErrorCode = "torrent_busy"
	// CodeNoVideo: the torrent holds nothing worth taking frames from.
	CodeNoVideo ErrorCode = "no_video_files"
	// CodeNoFileMatch: the file selection named something the torrent does
	// not hold.
	CodeNoFileMatch ErrorCode = "no_file_match"
	// CodeUnprobeable: ffprobe could not make sense of the container, so
	// there is no index to seek with.
	CodeUnprobeable ErrorCode = "unprobeable"
	// CodeReadStalled: a read through the bridge ran out of time, so the tool
	// on the other end was answering about a file it never finished reading.
	// Deliberately distinct from CodeUnprobeable: what ffprobe reports in that
	// situation - no streams, no packets, no keyframe with a position - is
	// indistinguishable from a container defect, and blaming the container
	// both accuses a healthy file and gates the sequential fallback on a
	// verdict nothing supports (TOR-45).
	CodeReadStalled ErrorCode = "read_stalled"
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
	// Before the context cases on purpose: a stall is a deadline that expired
	// inside the bridge, not the caller stopping the run, and the two must not
	// be reported as the same thing.
	case errors.Is(err, bridge.ErrStalled):
		return CodeReadStalled
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeCancelled
	case errors.Is(err, swarm.ErrNoMetadata):
		return CodeNoMetadata
	case errors.Is(err, swarm.ErrPrivacyUnresolvable):
		return CodePrivacyUnresolvable
	case errors.Is(err, swarm.ErrNoPortAvailable):
		return CodeNoPortAvailable
	case errors.Is(err, swarm.ErrTorrentBusy):
		return CodeTorrentBusy
	case errors.Is(err, swarm.ErrNoFileMatch):
		return CodeNoFileMatch
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
