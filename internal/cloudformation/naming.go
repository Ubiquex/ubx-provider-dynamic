package cloudformation

import (
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// Resolve computes the real HashiCorp-compatible resource type name for a
// CFN-sourced resource, given its real namespace ("SQS") and type
// ("Queue") segments -- mirrors internal/smithy/naming.go's own Resolve
// exactly (same real "try prefixed, try bare, else honestly unresolved"
// discipline, same shared smithy.KnownNames/smithy.Strategy types, not
// duplicated ones), since CFN overlaps the same real hashicorp/aws
// resource surface Smithy does and the identical naming-compatibility
// risk applies. namespace is snake_cased directly (not run through any
// real endpointPrefix lookup the way Smithy's own Resolve does) -- CFN's
// own real namespace segment is already a real, human-readable service
// name ("AmazonMQ", "SQS"), unlike Smithy's own terse endpointPrefix.
func Resolve(namespace, resourceType string, known smithy.KnownNames) (name string, strategy smithy.Strategy) {
	prefixed := "aws_" + uschema.ToSnakeCase(namespace) + "_" + uschema.ToSnakeCase(resourceType)
	bare := "aws_" + uschema.ToSnakeCase(resourceType)

	switch {
	case known[prefixed]:
		return prefixed, smithy.StrategyPrefixed
	case known[bare]:
		return bare, smithy.StrategyBare
	default:
		return prefixed, smithy.StrategyUnresolved
	}
}
