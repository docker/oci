package gzipr

import (
	"encoding/json"
	"io"
)

// Encode serializes the [Index] as JSON to w.
func (idx *Index) Encode(w io.Writer) error {
	return json.NewEncoder(w).Encode(idx)
}

// DecodeIndex deserializes a JSON-encoded [Index] from r.
func DecodeIndex(r io.Reader) (*Index, error) {
	var idx Index
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}
