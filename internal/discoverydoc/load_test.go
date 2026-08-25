package discoverydoc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoad_FetchCacheEnabled_PinsAcrossRepeatedLoads is Load's own real
// reproduction of UBI-186's found bug (see fetchcache's own doc comment
// for the live-confirmed finding): a server standing in for Google's own
// real discovery endpoint, which genuinely returns different content on
// successive requests for the identical URL, must not be allowed to
// change what a generation run sees mid-run. Every Load call against
// the same URL, once UBX_PROVIDER_DYNAMIC_FETCH_CACHE names a directory,
// must return the resource set the FIRST call saw -- exactly the
// property a multi-pass (go/ts/py) or repeated verification run needs.
func TestLoad_FetchCacheEnabled_PinsAcrossRepeatedLoads(t *testing.T) {
	revision := "20260811"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"name": "container",
			"revision": %q,
			"resources": {
				"projects": {
					"resources": {
						"clusters": {
							"methods": {
								"get":    {"httpMethod": "GET", "flatPath": "v1/projects/{p}/clusters/{c}"},
								"create": {"httpMethod": "POST", "flatPath": "v1/projects/{p}/clusters"}
							}
						}
					}
				}
			}
		}`, revision)
		revision = "20260818"
	}))
	defer srv.Close()

	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", t.TempDir())

	first, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	second, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if first.Revision != second.Revision {
		t.Fatalf("expected the second Load to see the first's pinned revision, got %q then %q", first.Revision, second.Revision)
	}
}

// TestLoad_FetchCacheDisabled_SeesLiveChanges confirms the default,
// unset-env-var path is genuinely unchanged from before fetchcache
// existed: every Load call re-fetches, so a server whose content
// changes between requests is visible to the caller, matching every
// existing, unpinned generation run's own established behavior.
func TestLoad_FetchCacheDisabled_SeesLiveChanges(t *testing.T) {
	revision := "20260811"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"name": "container", "revision": %q, "resources": {}}`, revision)
		revision = "20260818"
	}))
	defer srv.Close()

	t.Setenv("UBX_PROVIDER_DYNAMIC_FETCH_CACHE", "")

	first, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	second, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if first.Revision == second.Revision {
		t.Fatalf("expected the cache-disabled default path to see the server's live change, both calls returned revision %q", first.Revision)
	}
}
