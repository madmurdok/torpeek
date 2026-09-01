// Package output writes what a run produces: full-resolution frames as they
// become ready, the contact sheet assembled at the end, the JSON manifest, the
// result cache keyed by infohash and run parameters, and the run state that
// makes cancel and resume work.
//
// Requirements: sections 2.8, 2.9, 2.10 and 2.11.
package output
