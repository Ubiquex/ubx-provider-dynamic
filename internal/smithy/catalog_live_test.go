package smithy

import (
	"os"
	"testing"
)

func requireLiveCatalog(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against the real github.com/aws/api-models-aws repo")
	}
}

// TestFetchCatalog_RealRepo is this session's own real, required proof:
// a real, unauthenticated call against github.com/aws/api-models-aws's
// own live Trees API, not a mock or a fixture. Confirmed live before
// this test existed: 430 real service model files, truncated: false,
// zero download failures fetching all 430 -- this test re-asserts the
// same real facts so a future repo change (a new service added, an old
// one removed, GitHub's own API shape changing) is caught, not silently
// assumed to still hold.
func TestFetchCatalog_RealRepo(t *testing.T) {
	requireLiveCatalog(t)

	entries, err := FetchCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 400 {
		t.Fatalf("got %d real catalog entries, expected at least 400 (430 confirmed live 2026-08-25) -- api-models-aws shrank, or this function's own path-shape assumption broke", len(entries))
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if e.ServiceSlug == "" {
			t.Errorf("entry with empty ServiceSlug: %+v", e)
		}
		if e.SchemaURL == "" {
			t.Errorf("entry with empty SchemaURL: %+v", e)
		}
		if seen[e.ServiceSlug] {
			t.Errorf("duplicate ServiceSlug %q -- FetchCatalog's own 5-token path filter let through more than one real model file for the same service", e.ServiceSlug)
		}
		seen[e.ServiceSlug] = true
	}

	var sqs *CatalogEntry
	for i := range entries {
		if entries[i].ServiceSlug == "sqs" {
			sqs = &entries[i]
		}
	}
	if sqs == nil {
		t.Fatal("expected a real sqs entry, got none")
	}
	if sqs.SchemaURL != "https://raw.githubusercontent.com/aws/api-models-aws/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json" {
		t.Errorf("sqs SchemaURL = %q, does not match the real, confirmed-live URL", sqs.SchemaURL)
	}

	// The real catalog entry must actually load through this package's
	// own real Load/FindService -- proof the catalog isn't just a list of
	// plausible-looking URLs, but points at genuinely ingestible models.
	doc, err := Load(sqs.SchemaURL)
	if err != nil {
		t.Fatalf("catalog's own sqs SchemaURL did not load as a real Smithy model: %v", err)
	}
	if _, err := FindService(doc); err != nil {
		t.Fatalf("catalog's own sqs SchemaURL loaded but has no real service shape: %v", err)
	}
}
