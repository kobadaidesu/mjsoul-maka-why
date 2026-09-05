package liqi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// CONFIRMED by static GET on 2026-09-05; see docs/protocol-findings.md.
// TODO(verify): Match this static resource chain to the user's active Chrome
// client in Phase 1; the public index currently advertises a different build.
const staticOrigin = "https://game.mahjongsoul.com/"
const schemaResource = "res/proto/liqi.json"

var versionToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Metadata struct {
	GameVersion     string    `json:"game_version"`
	ResourceVersion string    `json:"resource_version"`
	SourceURL       string    `json:"source_url"`
	SHA256          string    `json:"sha256"`
	FetchedAt       time.Time `json:"fetched_at"`
	VersionURL      string    `json:"version_url"`
	ManifestURL     string    `json:"manifest_url"`
	ManifestSHA256  string    `json:"manifest_sha256"`
}

type Resource struct {
	Metadata Metadata
	Data     []byte // Exact response bytes, never remarshal the schema.
	Registry *Registry
	Cached   bool
	// The caller must report why the exact-version cache was used.
	FallbackReason error
}

type Resolver struct {
	client *http.Client
}

type transientFetchError struct{ err error }

func (e *transientFetchError) Error() string { return e.err.Error() }
func (e *transientFetchError) Unwrap() error { return e.err }

func NewResolver() *Resolver {
	return &Resolver{client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("static resource redirects are not permitted")
		},
	}}
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func validSHA(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

// Resolve performs only the two observed static GETs. It never reads data_url,
// executes client code, or accepts arbitrary game API endpoints.
func (r *Resolver) Resolve(ctx context.Context) (Metadata, error) {
	m := Metadata{VersionURL: staticOrigin + "version.json"}
	raw, err := r.get(ctx, m.VersionURL, 64<<10, "get version")
	if err != nil {
		return Metadata{}, err
	}
	if err := validateJSON(raw); err != nil {
		return Metadata{}, fmt.Errorf("version JSON: %w", err)
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &version); err != nil || !versionToken.MatchString(version.Version) {
		return Metadata{}, fmt.Errorf("invalid or missing static game version")
	}
	m.GameVersion = version.Version
	m.ManifestURL = staticOrigin + "resversion" + m.GameVersion + ".json"
	raw, err = r.get(ctx, m.ManifestURL, 32<<20, "get resource manifest")
	if err != nil {
		return Metadata{}, err
	}
	if err := validateJSON(raw); err != nil {
		return Metadata{}, fmt.Errorf("resource manifest JSON: %w", err)
	}
	var manifest struct {
		Res map[string]json.RawMessage `json:"res"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Metadata{}, fmt.Errorf("parse resource manifest: %w", err)
	}
	var entry struct {
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(manifest.Res[schemaResource], &entry); err != nil || !versionToken.MatchString(entry.Prefix) {
		return Metadata{}, fmt.Errorf("manifest has no valid %s prefix; resource layout is unverified", schemaResource)
	}
	m.ManifestSHA256 = digest(raw)
	m.ResourceVersion = entry.Prefix
	m.SourceURL = staticOrigin + entry.Prefix + "/" + schemaResource
	return m, nil
}

// Fetch always checks version and manifest. Cache fallback is allowed only if
// that chain resolved successfully and the schema GET then failed. An expected
// SHA further restricts both network and cache results, when supplied.
func (r *Resolver) Fetch(ctx context.Context, cacheDir, expectedSHA string) (*Resource, error) {
	if expectedSHA != "" && !validSHA(expectedSHA) {
		return nil, fmt.Errorf("expected SHA-256 must be 64 lowercase hex characters")
	}
	m, err := r.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	data, fetchErr := r.get(ctx, m.SourceURL, maxSchemaBytes, "get liqi schema")
	if fetchErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fetch liqi: %w", ctx.Err())
		}
		var transient *transientFetchError
		if !errors.As(fetchErr, &transient) {
			return nil, fetchErr
		}
		cached, err := loadCache(cacheDir, m, expectedSHA)
		if err != nil {
			return nil, fmt.Errorf("%w; exact-version cache unavailable: %v", fetchErr, err)
		}
		cached.FallbackReason = fetchErr
		return cached, nil
	}
	m.SHA256 = digest(data)
	m.FetchedAt = time.Now().UTC()
	if expectedSHA != "" && m.SHA256 != expectedSHA {
		return nil, fmt.Errorf("liqi SHA mismatch: game=%s resource=%s expected=%s actual=%s", m.GameVersion, m.ResourceVersion, expectedSHA, m.SHA256)
	}
	registry, err := Build(data)
	if err != nil {
		return nil, fmt.Errorf("build liqi descriptors game=%s resource=%s sha256=%s: %w", m.GameVersion, m.ResourceVersion, m.SHA256, err)
	}
	resource := &Resource{Metadata: m, Data: data, Registry: registry}
	if err := saveCache(cacheDir, resource); err != nil {
		return nil, fmt.Errorf("save liqi cache: %w", err)
	}
	return resource, nil
}

func (r *Resolver) get(ctx context.Context, url string, limit int64, operation string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%s url=%s attempts=0 status=unavailable: %w", operation, url, err)
	}
	// No cookies, credentials, request body, automatic retries, or redirects.
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := r.client.Do(req)
	if err != nil {
		failure := fmt.Errorf("%s url=%s attempts=1 status=unavailable: %w", operation, url, err)
		if resp == nil {
			return nil, &transientFetchError{err: failure}
		}
		return nil, failure // Redirect policy rejection is not a cache fallback.
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		failure := fmt.Errorf("%s url=%s attempts=1 status=%d", operation, url, resp.StatusCode)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return nil, &transientFetchError{err: failure}
		}
		return nil, failure
	}
	if readErr != nil {
		return nil, &transientFetchError{err: fmt.Errorf("%s url=%s attempts=1 status=%d: %w", operation, url, resp.StatusCode, readErr)}
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%s close body url=%s attempts=1 status=%d: %w", operation, url, resp.StatusCode, closeErr)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s url=%s attempts=1 status=%d exceeds %d bytes", operation, url, resp.StatusCode, limit)
	}
	return data, nil
}
