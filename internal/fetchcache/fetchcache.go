// Package fetchcache is an explicit, opt-in disk cache in front of a
// live, third-party HTTP fetch -- built for the real, live-confirmed
// finding behind UBI-186's GCP reproducibility gap: two fetches of the
// identical https://container.googleapis.com/$discovery/rest?version=v1
// URL, taken minutes apart, returned a genuinely different "revision"
// field and different schema content (a full 262-URL sweep of every
// configured GCP discovery doc, two rounds a few minutes apart, found 8
// with real semantic drift -- new schema properties, changed field
// descriptions, a newly added method -- not merely reordered JSON keys,
// confirmed by parsing and comparing, not just diffing raw bytes).
// That is Google's own live backend serving whichever revision happens
// to be current on the instance that answers a given request, not
// anything this codebase's own map iteration or collision resolution
// controls -- both were already audited and confirmed fully sorted
// before this package was written. Sorting a candidate walk cannot fix
// an input that changes between fetches; pinning the fetched bytes for
// the duration of a generation run is the only real fix for the
// byte-identical reproducibility UBI-182's snapshot hashing depends on.
//
// Disabled by default (Get behaves exactly like a plain fetch, one live
// round trip every call) so routine, single-pass generation keeps
// seeing the freshest schema -- this package changes nothing about that
// path. Set UBX_PROVIDER_DYNAMIC_FETCH_CACHE to a directory to opt in
// for a reproducible run: every fetch of the same URL within that
// directory's lifetime returns the identical bytes the first fetch in
// that directory saw, regardless of how many separate process
// invocations ask for it -- each dynamic-provider-group member's own
// three separate real subprocess calls (schema/signals/namespaces,
// cli/sdk.go's own real shape), each of go/ts/py, and repeat runs for
// verification all draw from the same pinned snapshot.
package fetchcache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

const dirEnvVar = "UBX_PROVIDER_DYNAMIC_FETCH_CACHE"

// Get returns url's body: from the on-disk cache in
// UBX_PROVIDER_DYNAMIC_FETCH_CACHE if that env var is set and already
// holds a cached entry for url, otherwise via do (which also populates
// the cache for next time when the env var is set). do is the caller's
// own real fetch -- this package owns none of the HTTP client, timeout,
// or status-code handling, only the pin-it-to-disk decision in front of
// it.
func Get(url string, do func(url string) ([]byte, error)) ([]byte, error) {
	dir := os.Getenv(dirEnvVar)
	if dir == "" {
		return do(url)
	}

	path := cachePath(dir, url)
	if body, err := os.ReadFile(path); err == nil {
		return body, nil
	}

	body, err := do(url)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o755); err == nil {
		tmp := path + ".tmp"
		if writeErr := os.WriteFile(tmp, body, 0o644); writeErr == nil {
			os.Rename(tmp, path)
		}
	}
	return body, nil
}

func cachePath(dir, url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".body")
}
