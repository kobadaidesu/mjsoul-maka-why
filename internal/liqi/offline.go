package liqi

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// OpenCachedMetadata loads only local, exact-version schema bytes. Capture must
// not silently fetch a possibly different schema while observing a browser.
func OpenCachedMetadata(path string) (*Resource, error) {
	raw, err := readPrivate(path, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("read liqi metadata: %w", err)
	}
	if err := validateJSON(raw); err != nil {
		return nil, err
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse liqi metadata: %w", err)
	}
	if err := validateMetadata(m); err != nil {
		return nil, err
	}
	if filepath.Base(path) != bindingName(m) {
		return nil, fmt.Errorf("liqi metadata filename does not match version binding")
	}
	return loadCache(filepath.Dir(path), m, m.SHA256)
}
