package liqi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type staticFixture struct {
	game, resource string
	schema         []byte
	failPath       string
	failStatus     int
	paths          []string
}

func newStaticFixture(t *testing.T) (*Resolver, *staticFixture) {
	t.Helper()
	s := &staticFixture{game: "test.12.w", resource: "vtest.9.w", schema: fixture(t, "features.json")}
	r := NewResolver()
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		if req.Method != http.MethodGet || req.Body != nil || req.URL.Host != "game.mahjongsoul.com" || req.URL.RawQuery != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		path := req.URL.Path
		s.paths = append(s.paths, path)
		if path == s.failPath {
			if s.failStatus == 0 {
				return nil, errors.New("synthetic transport failure")
			}
			return &http.Response{StatusCode: s.failStatus, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("synthetic error")), Request: req}, nil
		}
		var body []byte
		switch path {
		case "/version.json":
			body = []byte(fmt.Sprintf(`{"version":%q,"code":"not-fetched.js"}`, s.game))
		case "/resversion" + s.game + ".json":
			body = []byte(fmt.Sprintf(`{"res":{"res/proto/liqi.json":{"prefix":%q},"unrelated/asset":{"prefix":"never-fetch"}}}`, s.resource))
		case "/" + s.resource + "/res/proto/liqi.json":
			body = s.schema
		default:
			t.Fatalf("unexpected static path %s", path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	return r, s
}

func TestFetchAndExactCache(t *testing.T) {
	r, s := newStaticFixture(t)
	dir := filepath.Join(t.TempDir(), "liqi")
	resource, err := r.Fetch(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Metadata.GameVersion != s.game || resource.Metadata.ResourceVersion != s.resource || !bytes.Equal(resource.Data, s.schema) || resource.Cached {
		t.Fatalf("incorrect resource: %+v", resource.Metadata)
	}
	if len(s.paths) != 3 {
		t.Fatalf("expected exactly 3 static GETs, got %v", s.paths)
	}
	for _, name := range []string{resource.Metadata.SHA256 + ".json", bindingName(resource.Metadata)} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("cache not private: %s %v", name, err)
		}
	}
	s.failPath = "/" + s.resource + "/res/proto/liqi.json"
	s.failStatus = http.StatusServiceUnavailable
	fallback, err := r.Fetch(context.Background(), dir, resource.Metadata.SHA256)
	if err != nil || !fallback.Cached || fallback.FallbackReason == nil || !bytes.Equal(fallback.Data, s.schema) {
		t.Fatalf("exact cache fallback failed: %v", err)
	}
	if _, err := r.Fetch(context.Background(), dir, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong requested SHA accepted from cache")
	}
	s.failStatus = http.StatusNotFound
	if _, err := r.Fetch(context.Background(), dir, ""); err == nil {
		t.Fatal("404 must not be concealed by a cached schema")
	}
	s.failStatus = 0
	if cached, err := r.Fetch(context.Background(), dir, ""); err != nil || !cached.Cached {
		t.Fatalf("transport failure did not use exact cache: %v", err)
	}
	// Same bytes shared by different game versions retain distinct provenance.
	s.failPath = ""
	s.game = "test.13.w"
	second, err := r.Fetch(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Metadata.SHA256 != resource.Metadata.SHA256 {
		t.Fatal("same bytes changed hash")
	}
	if _, err := loadCache(dir, resource.Metadata, ""); err != nil {
		t.Fatalf("older binding was overwritten: %v", err)
	}
	// No fallback across resource versions, even if a cache exists.
	s.resource = "vtest.10.w"
	s.failPath = "/" + s.resource + "/res/proto/liqi.json"
	if _, err := r.Fetch(context.Background(), dir, ""); err == nil {
		t.Fatal("cross-version fallback accepted")
	}
	// No fallback when the current version/manifest cannot be established.
	s.failPath = "/version.json"
	if _, err := r.Fetch(context.Background(), dir, resource.Metadata.SHA256); err == nil {
		t.Fatal("unresolved current version accepted")
	}
}

func TestCorruptAndUnsafeCacheRejected(t *testing.T) {
	for _, corruption := range []string{"bytes", "metadata", "permissions", "symlink"} {
		t.Run(corruption, func(t *testing.T) {
			r, s := newStaticFixture(t)
			dir := filepath.Join(t.TempDir(), "liqi")
			resource, err := r.Fetch(context.Background(), dir, "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, resource.Metadata.SHA256+".json")
			switch corruption {
			case "bytes":
				err = os.WriteFile(path, []byte("corrupt synthetic bytes"), 0600)
			case "metadata":
				err = os.WriteFile(filepath.Join(dir, bindingName(resource.Metadata)), []byte(`{"sha256":"../../outside"}`), 0600)
			case "permissions":
				err = os.Chmod(path, 0644)
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(path+".original", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.failPath = "/" + s.resource + "/res/proto/liqi.json"
			if _, err := r.Fetch(context.Background(), dir, ""); err == nil {
				t.Fatal("unsafe/corrupt cache accepted")
			}
		})
	}
}

func TestFetchFailsClosed(t *testing.T) {
	for _, kind := range []string{"version", "prefix", "schema", "sha", "cancel", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			r, s := newStaticFixture(t)
			dir := filepath.Join(t.TempDir(), "liqi")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := ""
			switch kind {
			case "version":
				s.game = "../api"
			case "prefix":
				s.resource = "https://example.invalid/api"
			case "schema":
				s.schema = []byte(`{"nested":{"M":{"fields":{"f":{"type":"Missing","id":1}}}}}`)
			case "sha":
				expected = strings.Repeat("0", 64)
			case "cancel":
				cancel()
			case "manifest":
				s.failPath = "/resversion" + s.game + ".json"
			}
			if _, err := r.Fetch(ctx, dir, expected); err == nil {
				t.Fatal("invalid input accepted")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed fetch created cache: %v", err)
			}
		})
	}
}

func TestRedirectAndSizeLimits(t *testing.T) {
	r := NewResolver()
	calls := 0
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://example.invalid/api"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	if _, err := r.Resolve(context.Background()); err == nil || calls != 1 {
		t.Fatalf("redirect was followed: calls=%d error=%v", calls, err)
	}
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("12345")), Request: req}, nil
	})
	if _, err := r.get(context.Background(), staticOrigin+"version.json", 4, "test"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("size limit ignored: %v", err)
	}
}
