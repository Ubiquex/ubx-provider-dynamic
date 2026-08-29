package dynserver

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestEnsurePathParamsPresent_SecondRenameSurvivesPascalCase is UBI-206's
// own real regression: calling ensurePathParamsPresent twice against the
// same attrs slice (Build's own real shape -- once for PathParams, once
// for CreatePathParams) for a resource whose Create and Read share the
// identical path, where the colliding path-param name ("owner") needs
// renaming both times, must produce two names that stay genuinely
// distinct after ubiquex's own real Go codegen (sdk/codegen/templates/
// go/go.go's splitWireName, which tolerates and drops a trailing
// underscore) -- not "owner_path" and "owner_path_", which both
// PascalCase to the identical "OwnerPath" and silently redeclare a Go
// struct field.
func TestEnsurePathParamsPresent_SecondRenameSurvivesPascalCase(t *testing.T) {
	objectType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}
	attrs := []*tfprotov6.SchemaAttribute{
		{Name: "owner", NestedType: &tfprotov6.SchemaObject{}}, // real, live shape: NestedType set, Type nil
	}
	_ = objectType

	renames1 := ensurePathParamsPresent(&attrs, []string{"owner"})
	renames2 := ensurePathParamsPresent(&attrs, []string{"owner"})

	first := renames1["owner"]
	second := renames2["owner"]
	if first == "" || second == "" {
		t.Fatalf("expected both calls to rename \"owner\", got %q and %q", first, second)
	}
	if first == second {
		t.Fatalf("expected two distinct renamed names, got the same %q twice", first)
	}

	// The real, load-bearing check: after ubiquex's own real splitWireName
	// (trailing/doubled underscores tolerated and dropped), these must
	// still differ.
	if stripTrailingUnderscores(first) == stripTrailingUnderscores(second) {
		t.Fatalf("names %q and %q collapse to the identical Go identifier after trailing-underscore stripping", first, second)
	}
}

// stripTrailingUnderscores mirrors sdk/codegen's own real splitWireName
// tolerance (ubiquex, sdk/codegen/templates/go/go.go) closely enough to
// catch the real regression this test guards -- not a full reimplementation,
// just the one behavior that mattered here.
func stripTrailingUnderscores(s string) string {
	for len(s) > 0 && s[len(s)-1] == '_' {
		s = s[:len(s)-1]
	}
	return s
}
