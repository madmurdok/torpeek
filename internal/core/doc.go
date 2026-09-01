// Package core is the silent library core: it owns the run, orchestrates the
// other packages and emits a typed event stream. It never prints and knows
// nothing about how results are displayed, so the TUI, the web UI and any
// future service can all sit on top of it as equal clients.
//
// Requirements: section 3.1.
package core
