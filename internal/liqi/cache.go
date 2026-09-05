package liqi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Metadata is indexed by the complete observed version/source binding. A
// single schema SHA may serve several game versions without losing provenance.
func bindingName(m Metadata) string {
	return digest([]byte(m.GameVersion+"\n"+m.ResourceVersion+"\n"+m.SourceURL)) + ".meta.json"
}

func validateMetadata(m Metadata) error {
	if !versionToken.MatchString(m.GameVersion) || !versionToken.MatchString(m.ResourceVersion) ||
		!validSHA(m.SHA256) || !validSHA(m.ManifestSHA256) || m.FetchedAt.IsZero() ||
		m.SourceURL != staticOrigin+m.ResourceVersion+"/"+schemaResource ||
		m.VersionURL != staticOrigin+"version.json" ||
		m.ManifestURL != staticOrigin+"resversion"+m.GameVersion+".json" {
		return fmt.Errorf("invalid cache metadata")
	}
	return nil
}

func saveCache(dir string, r *Resource) error {
	if err := validateMetadata(r.Metadata); err != nil {
		return err
	}
	if digest(r.Data) != r.Metadata.SHA256 {
		return fmt.Errorf("cache payload SHA mismatch")
	}
	if dir == "" {
		return fmt.Errorf("cache directory is empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(r.Metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}
	if err := atomicWrite(dir, r.Metadata.SHA256+".json", r.Data); err != nil {
		return err
	}
	return atomicWrite(dir, bindingName(r.Metadata), append(meta, '\n'))
}

func loadCache(dir string, want Metadata, expectedSHA string) (*Resource, error) {
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	meta, err := readPrivate(filepath.Join(dir, bindingName(want)), 64<<10)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	if err := validateJSON(meta); err != nil {
		return nil, err
	}
	var m Metadata
	d := json.NewDecoder(bytes.NewReader(meta))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode cache metadata: %w", err)
	}
	if err := validateMetadata(m); err != nil {
		return nil, err
	}
	if m.GameVersion != want.GameVersion || m.ResourceVersion != want.ResourceVersion || m.SourceURL != want.SourceURL ||
		(expectedSHA != "" && m.SHA256 != expectedSHA) {
		return nil, fmt.Errorf("cache does not match resolved version/source or requested SHA")
	}
	data, err := readPrivate(filepath.Join(dir, m.SHA256+".json"), maxSchemaBytes)
	if err != nil {
		return nil, fmt.Errorf("read cached schema: %w", err)
	}
	if digest(data) != m.SHA256 {
		return nil, fmt.Errorf("cached schema SHA mismatch")
	}
	registry, err := Build(data)
	if err != nil {
		return nil, fmt.Errorf("cached schema descriptors: %w", err)
	}
	return &Resource{Metadata: m, Data: data, Registry: registry, Cached: true}, nil
}

func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat cache directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("cache directory must be a real directory with permissions 0700")
	}
	return nil
}

func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("cache file must be regular and private (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("cache file exceeds size limit")
	}
	return data, nil
}

func atomicWrite(dir, name string, data []byte) (err error) {
	f, err := os.CreateTemp(dir, ".liqi-*") // 0600, same filesystem as destination.
	if err != nil {
		return fmt.Errorf("create cache temporary file: %w", err)
	}
	path := f.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
			err = fmt.Errorf("remove cache temporary file: %w", removeErr)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write cache temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync cache temporary file: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cache temporary file: %w", err)
	}
	if err := os.Rename(path, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("rename cache temporary file: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open cache directory for sync: %w", err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return fmt.Errorf("sync cache directory: %w", syncErr)
	}
	return closeErr
}
