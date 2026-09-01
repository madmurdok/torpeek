package swarm

import (
	"fmt"
	"os"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// Source is where a torrent comes from: a magnet link or a .torrent file.
//
// It exists mainly so the private flag can be answered *before* a client is
// started. anacrolix carries metainfo.Info.Private but never acts on it - DHT
// and PEX are client-wide switches - so respecting BEP 27 is entirely our job,
// and the only way to respect it is to know the answer before going online.
type Source struct {
	raw    string
	magnet *metainfo.Magnet
	mi     *metainfo.MetaInfo
	info   *metainfo.Info
}

// ParseSource reads a magnet URI or a path to a .torrent file. Nothing touches
// the network here.
func ParseSource(s string) (Source, error) {
	if strings.HasPrefix(s, "magnet:") {
		m, err := metainfo.ParseMagnetUri(s)
		if err != nil {
			return Source{}, fmt.Errorf("parse magnet: %w", err)
		}
		return Source{raw: s, magnet: &m}, nil
	}

	if _, err := os.Stat(s); err != nil {
		return Source{}, fmt.Errorf("source is neither a magnet URI nor a readable file: %s", s)
	}

	mi, err := metainfo.LoadFromFile(s)
	if err != nil {
		return Source{}, fmt.Errorf("load torrent file: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return Source{}, fmt.Errorf("parse torrent info: %w", err)
	}
	return Source{raw: s, mi: mi, info: &info}, nil
}

// IsMagnet reports whether metadata still has to be fetched from the swarm.
func (s Source) IsMagnet() bool { return s.magnet != nil }

// Trackers lists announce URLs known before any metadata is fetched. For a
// magnet these are its tr= parameters; for a file, its announce list.
func (s Source) Trackers() []string {
	if s.magnet != nil {
		return s.magnet.Trackers
	}
	if s.mi == nil {
		return nil
	}
	var out []string
	for _, tier := range s.mi.UpvertedAnnounceList() {
		out = append(out, tier...)
	}
	return out
}

// Privacy reports the BEP 27 private flag. known is false for a magnet, where
// the answer only arrives with the metadata - which is exactly why the session
// fetches metadata from trackers first and only then decides about DHT.
func (s Source) Privacy() (private, known bool) {
	if s.info == nil {
		return false, false
	}
	return s.info.Private != nil && *s.info.Private, true
}

// InfoHash identifies the torrent. Available for both source kinds.
func (s Source) InfoHash() (metainfo.Hash, error) {
	if s.magnet != nil {
		return s.magnet.InfoHash, nil
	}
	if s.mi == nil {
		return metainfo.Hash{}, fmt.Errorf("empty source")
	}
	return s.mi.HashInfoBytes(), nil
}

func (s Source) String() string { return s.raw }
