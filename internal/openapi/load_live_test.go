package openapi

import (
	"os"
	"testing"
)

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real, published specs")
	}
}

// TestLive_AzureSwaggerWithExternalRefs proves the real fix this checkpoint
// needed on top of Kubernetes' own real, single-file Swagger 2.0 case:
// Azure's own real, published Swagger 2.0 specs are NOT single-file --
// confirmed live against a real Azure Compute resource-provider spec,
// which references a real, external, sibling "common-types" definitions
// file by real relative path
// ("../../common-types/v1/common.json#/definitions/SubResource"). The
// plain openapi2conv.ToV3 wrapper's own internal loader refuses this
// outright (a real, safe-by-default "disallowed external reference"
// error) -- Load now passes its own loader (IsExternalRefsAllowed) and
// the real document location through via ToV3WithLoader instead, letting
// it resolve exactly like a real OpenAPI 3.x spec's own external refs
// already did.
//
// Deliberately does NOT assert anything about resourcemap.Discover
// finding real CRUD resources in this spec -- confirmed live, separately,
// that Azure's own real ARM API convention uses PUT (not POST) to
// create/upsert a resource, which resourcemap.Discover does not
// recognize yet (a real, separate, NOT-yet-fixed gap, named honestly in
// STATE.md and the central provider config's own comments, not
// conflated with this test's own real, narrower scope: proving the
// loader itself correctly parses a real, multi-file Swagger 2.0 spec).
func TestLive_AzureSwaggerWithExternalRefs(t *testing.T) {
	requireLive(t)
	const url = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/compute/resource-manager/Microsoft.Compute/Compute/stable/2026-04-01/ComputeRP.json"

	doc, err := Load(url)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatalf("expected a real OpenAPI 3.x version string after conversion, got %q", doc.OpenAPI)
	}
	if doc.Paths == nil || doc.Paths.Len() == 0 {
		t.Fatalf("expected real paths to survive conversion, got none")
	}
	// A real, external-ref-including proxy for "external ref resolution
	// genuinely worked, not just top-level parsing": this ComputeRP.json
	// file's own real, LOCAL-only $ref graph is far smaller than 457 --
	// confirmed live this session that Azure's own real common-types
	// files (referenced by real relative path, e.g.
	// "../../common-types/v1/common.json") pull in real, additional
	// schemas beyond what the local file alone defines. A specific
	// external schema NAME is deliberately not pinned here -- confirmed
	// live that kin-openapi's own real v2->v3 conversion doesn't always
	// preserve an externally-defined schema's own bare name verbatim
	// (e.g. real "SubResource" usages resolve into a real, differently-
	// named schema, "SubResourceWithColocationStatus," not the bare name
	// itself) -- asserting a specific name would be pinning an
	// implementation detail of kin-openapi's own conversion, not this
	// package's own real contract.
	if got := len(doc.Components.Schemas); got < 200 {
		t.Fatalf("expected a real, large schema count (external refs genuinely resolved, not just this file's own local definitions), got only %d", got)
	}
	t.Logf("real Azure spec: %d paths, %d schemas (including externally-referenced ones)", doc.Paths.Len(), len(doc.Components.Schemas))
}
