// Package enrich learns what a torrent IS, from the only thing torpeek has
// before it fetches anything: the torrent's own name.
//
// NOTHING IS IMPLEMENTED HERE YET. This file is the package's boundary and
// nothing else - deliberately no types, no stubs and no placeholder
// functions, because this project has spent several tickets removing dead
// code and an orphan package of empty declarations would be exactly that.
// The idea, the order of the work and the decisions it already obliges are
// recorded in docs/enrichment.md; read that before adding the first line of
// code here, because the order is the design rather than a preference.
//
// # What belongs in this package
//
// Parsing a release name into what it says - title, year, season, episode,
// source, resolution, codec, group - and, when a person has opted in,
// enriching that with a metadata service's answer: cast, genres, keywords.
// Both cached beside the run record, because the results tree is already
// keyed by infohash and params and already survives being copied from a
// seedbox to a laptop (TOR-60), so a sidecar there is durable without any new
// machinery, and the library a person searches falls out of it for free.
//
// # What does NOT belong here
//
// FACE RECOGNITION. Not because it is out of scope as an idea - it is the
// third step of the same design - but because it cannot live in this binary.
// torpeek ships CGO_ENABLED=0, and that is load-bearing rather than
// convenient: with cgo off the piece-completion store is bbolt rather than
// sqlite, and make check tests that build specifically because it is the one
// that goes out. Every ONNX runtime is cgo. So a model rides BESIDE the
// binary the way ffmpeg already does, in its own process with its own build,
// and putting it here would drag a cgo dependency into the one place the
// project has decided it cannot go.
//
// # The two rules this package inherits rather than invents
//
// A LOOKUP IS A DISCLOSURE. Sending a name to a service tells that service
// what this person is looking at, and a private torrent's name can itself
// identify them. Acceptance criterion 5 exists because a private torrent must
// not announce itself, and TOR-150 turned webseeds off partly because they are
// HTTP to somebody else's server. So enrichment is opt-in, what leaves the
// machine is stated plainly, and the offline parse stays useful on its own -
// declining should cost the tags, not the feature.
//
// A GUESS IS NOT A FACT. Release names are folklore, not a format, and a
// mis-parse names the wrong film. A confidently wrong cast list is worse than
// an empty one, because it sits somewhere a person has no reason to doubt.
// The rule this project has applied seven times running - absent is not zero -
// applies unchanged: a weak match reads as unknown, or as a candidate marked
// as one, and never as an answer.
package enrich
