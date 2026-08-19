// Live validation against two real, genuinely different, currently-published
// OpenAPI specs (GitHub's github/rest-api-description and Datadog's own
// official spec) -- UBI-158 Phase 1's own required proof, kept separate from
// the hermetic default test suite (gated behind UBX_LIVE_VALIDATION, the
// same discipline the ubiquex monorepo itself applies to any test that
// touches a real network/service: `go test ./...` stays hermetic by
// default).
package dynserver

import (
	"os"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
)

func TestLive_GitHub(t *testing.T) {
	requireLive(t)
	validateSpec(t, "github", "https://raw.githubusercontent.com/github/rest-api-description/main/descriptions/api.github.com/api.github.com.json",
		[]string{"github_full-repository", "github_issue", "github_gist"})
}

func TestLive_Datadog(t *testing.T) {
	requireLive(t)
	validateSpec(t, "datadog", "https://raw.githubusercontent.com/DataDog/datadog-api-client-go/master/.generator/schemas/v1/openapi.yaml",
		nil)
}

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real, published OpenAPI specs")
	}
}

func validateSpec(t *testing.T, providerName, url string, wantAtLeastOneOf []string) {
	t.Helper()

	doc, err := openapi.Load(url)
	if err != nil {
		t.Fatalf("load %s spec: %v", providerName, err)
	}

	resources, notes, err := Build(doc, providerName, config.Provider{})
	if err != nil {
		t.Fatalf("build %s resources: %v", providerName, err)
	}
	if len(resources) == 0 {
		t.Fatalf("discovered zero resources from a real %s spec -- the engine found nothing usable", providerName)
	}

	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	sort.Strings(names)

	t.Logf("%s: %d resources discovered, %d notes", providerName, len(resources), len(notes))
	max := 40
	if len(names) < max {
		max = len(names)
	}
	t.Logf("%s: sample resource types: %v", providerName, names[:max])

	if len(wantAtLeastOneOf) > 0 {
		found := false
		for _, w := range wantAtLeastOneOf {
			if _, ok := resources[w]; ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: expected at least one of %v among discovered resources, found none (got %v)", providerName, wantAtLeastOneOf, names)
		}
	}

	// Exercise every discovered resource's own schema end to end: a real
	// null value of its ObjectType must marshal through the exact same
	// cty-msgpack wire encoding a launched provider process uses to talk
	// to ubx's own real client (provider/ctyvalue.go) -- proves the
	// translated schema isn't just structurally built but genuinely
	// wire-valid, not merely "compiles."
	for _, name := range names {
		rt := resources[name]
		if len(rt.ObjectType.AttributeTypes) == 0 {
			t.Fatalf("%s: resource %s has zero attributes", providerName, name)
		}
		nullVal := tftypes.NewValue(rt.ObjectType, nil)
		if _, err := nullVal.MarshalMsgPack(rt.ObjectType); err != nil {
			t.Fatalf("%s: resource %s: schema does not round-trip through msgpack: %v", providerName, name, err)
		}
	}

	dumpNotesSample(t, providerName, notes, 25)

	// UBI-158 Phase 5 (the conformance gate) real regression check: every
	// resource's own PathParams/CreatePathParams must RESOLVE (via
	// PathParamAttr/CreatePathParamAttr -- identity for the common case,
	// a real rename when ensurePathParamsPresent found a genuine name
	// collision) to a real STRING or NUMBER attribute -- confirmed live
	// this session that github_full_repository's own "owner" path segment
	// silently collided with its own, differently-typed (Object) response
	// attribute of the same name before that fix, breaking every real
	// ReadResource/ApplyResourceChange call for the type outright
	// (extractStringAttrs' own real "cannot be used as a path parameter"
	// error). This exercises every discovered resource from this spec,
	// not just github_full_repository, since the same name-collision
	// shape can happen to any real API this engine points at.
	for _, name := range names {
		rt := resources[name]
		byName := map[string]bool{}
		for _, a := range rt.Schema.Block.Attributes {
			if a.Type != nil && (a.Type.Is(tftypes.String) || a.Type.Is(tftypes.Number)) {
				byName[a.Name] = true
			}
		}
		check := func(templateParams []string, attrFor map[string]string) {
			for _, p := range templateParams {
				attrName := p
				if renamed, ok := attrFor[p]; ok {
					attrName = renamed
				}
				if !byName[attrName] {
					t.Errorf("%s: resource %s: path parameter %q (resolved to attribute %q) is not a real string/number attribute in the translated schema -- a real ReadResource/ApplyResourceChange call would fail to build its own request", providerName, name, p, attrName)
				}
			}
		}
		check(rt.PathParams, rt.PathParamAttr)
		check(rt.CreatePathParams, rt.CreatePathParamAttr)
	}
}

func dumpNotesSample(t *testing.T, providerName string, notes []string, max int) {
	t.Helper()
	if len(notes) < max {
		max = len(notes)
	}
	for _, n := range notes[:max] {
		t.Logf("%s note: %s", providerName, n)
	}
}
