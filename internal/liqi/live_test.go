//go:build live

package liqi

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveStaticSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r, err := NewResolver().Fetch(ctx, filepath.Join(t.TempDir(), "liqi"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Registry.Message(".lq.Wrapper"); err != nil {
		t.Fatal(err)
	}
	t.Logf("game=%s resource=%s sha=%s counts=%+v", r.Metadata.GameVersion, r.Metadata.ResourceVersion, r.Metadata.SHA256, r.Registry.Counts)
}
