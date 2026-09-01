// Package swarm wraps the BitTorrent session: adding a magnet or .torrent,
// fetching arbitrary piece ranges by priority, reading swarm availability off
// peer bitfields, and accounting the run's time and traffic budget.
//
// Requirements: sections 2.1, 2.4, 2.6 and 4.
package swarm

import (
	"fmt"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
)

// Config holds the session settings torpeek cares about. Defaults follow
// REQUIREMENTS.md section 7.
type Config struct {
	// DataDir stages fetched pieces. Raw pieces are dropped after a run;
	// only results are cached.
	DataDir string
	// Upload serves already-held pieces back to the swarm while a run is
	// active. On by default: peers reciprocate, which helps min-time.
	Upload bool
	// DHT enables the distributed hash table. Torrents carrying the private
	// flag stay off it regardless of this setting.
	DHT bool
}

// DefaultConfig returns the session defaults for a run staging data in dataDir.
func DefaultConfig(dataDir string) Config {
	return Config{DataDir: dataDir, Upload: true, DHT: true}
}

// NewClient starts a BitTorrent session.
func NewClient(cfg Config) (*torrent.Client, error) {
	tc := torrent.NewDefaultClientConfig()
	tc.DefaultStorage = storage.NewFileByInfoHash(cfg.DataDir)
	tc.NoUpload = !cfg.Upload
	tc.NoDHT = !cfg.DHT
	// We only ever hold slivers of a file, so there is nothing to seed once
	// the run is over.
	tc.Seed = false

	cl, err := torrent.NewClient(tc)
	if err != nil {
		return nil, fmt.Errorf("start torrent session: %w", err)
	}
	return cl, nil
}
