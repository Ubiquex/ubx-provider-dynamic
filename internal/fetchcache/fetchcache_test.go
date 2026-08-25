package fetchcache

import (
	"path/filepath"
	"testing"
)

func TestGet_DisabledByDefault_AlwaysCallsDo(t *testing.T) {
	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", "")

	calls := 0
	do := func(url string) ([]byte, error) {
		calls++
		return []byte{byte(calls)}, nil
	}

	first, err := Get("https://example.com/doc", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, err := Get("https://example.com/doc", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected do to be called on every Get with the cache disabled, got %d calls", calls)
	}
	if string(first) == string(second) {
		t.Fatalf("expected two live fetches to see the do func's own changing return value, both returned %v", first)
	}
}

// TestGet_Enabled_PinsFirstFetch is the real, direct reproduction of
// UBI-186's own found bug: do here stands in for a live third-party
// endpoint whose content genuinely changes between calls (Google's real
// discovery documents, confirmed live -- see fetchcache's own doc
// comment). With the cache enabled, every caller asking for the same
// url within this directory's lifetime must see the SAME bytes the
// first caller saw, never do's later, different return value -- this is
// what makes a multi-pass (go/ts/py) or repeated generation run
// byte-identical.
func TestGet_Enabled_PinsFirstFetch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", dir)

	calls := 0
	do := func(url string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"revision":"20260811"}`), nil
		}
		return []byte(`{"revision":"20260818"}`), nil
	}

	first, err := Get("https://container.googleapis.com/$discovery/rest?version=v1", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, err := Get("https://container.googleapis.com/$discovery/rest?version=v1", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	third, err := Get("https://container.googleapis.com/$discovery/rest?version=v1", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 1 {
		t.Fatalf("expected exactly one live fetch with the cache enabled, do was called %d times", calls)
	}
	if string(first) != `{"revision":"20260811"}` || string(second) != string(first) || string(third) != string(first) {
		t.Fatalf("expected every call to return the first fetch's own pinned bytes, got %q, %q, %q", first, second, third)
	}
}

func TestGet_Enabled_DistinctURLsCacheSeparately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", dir)

	do := func(url string) ([]byte, error) { return []byte(url), nil }

	a, err := Get("https://a.example.com", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	b, err := Get("https://b.example.com", do)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(a) != "https://a.example.com" || string(b) != "https://b.example.com" {
		t.Fatalf("expected distinct URLs to cache independently, got %q and %q", a, b)
	}
}

func TestGet_Enabled_SurvivesAcrossSeparateCacheDirLookups(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", dir)

	first, err := Get("https://example.com/doc", func(string) ([]byte, error) { return []byte("first"), nil })
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(first) != "first" {
		t.Fatalf("unexpected first fetch: %q", first)
	}

	// A fresh process (this test's own second Get call, standing in for
	// the next of the three real, separate ubx-provider-dynamic
	// subprocess invocations cli/sdk.go makes per member -- schema,
	// signals, namespaces -- or the next of the three real go/ts/py
	// passes) must see the same cached bytes without ever calling do.
	second, err := Get("https://example.com/doc", func(string) ([]byte, error) {
		t.Fatal("do must not be called once the cache already holds this URL")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(second) != "first" {
		t.Fatalf("expected the cached value from the first process's fetch, got %q", second)
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*.body"))
	if err != nil {
		t.Fatalf("glob cache dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one cache entry on disk, found %d: %v", len(entries), entries)
	}
}
