package wireexec

import (
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// InputMemberNames returns opShapeID's own real input shape's member names,
// converted to the snake_case attribute names the translated tfplugin
// schema uses -- a Smithy-sourced resource's real equivalent of
// resourcemap.Resource's own PathParams (OpenAPI's own "{param}" URL
// segments): the set of fields needed to identify/select one specific
// resource instance for a Read/Update/Delete call. Unlike OpenAPI, Smithy's
// REST/RPC/Query protocols don't share one uniform "path parameter" concept
// -- for restJson1/restXml this is exactly the httpLabel-bound members;
// for awsJson1_x/awsQuery it's simply every real input member (there is no
// separate path at all) -- but in every real case, this IS the set of
// fields Do's own per-protocol binder reads out of the caller's input map,
// so returning "every real input member name" here is correct for all six
// real protocols without needing per-protocol special-casing.
func InputMemberNames(model *smithy.Model, opShapeID string) []string {
	op, ok := model.Shapes[opShapeID]
	if !ok || op.Input == nil {
		return nil
	}
	inputShape, ok := model.Shapes[op.Input.Target]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(inputShape.Members))
	for memberName := range inputShape.Members {
		out = append(out, uschema.ToSnakeCase(memberName))
	}
	return out
}
