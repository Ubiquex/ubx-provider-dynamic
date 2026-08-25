package smithy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// BuiltDataSource is one fully-processed Smithy-sourced data source:
// real discovery (datasource.go's own DiscoverDataSources), real schema
// translation reusing the identical, unchanged toschema.go/
// internal/schema.Translator machinery build.go's own BuiltResource
// already uses for resources -- no second translator, no parallel
// schema-shape logic. Deliberately carries no execution seam at all
// (unlike BuiltResource's own wireexec-facing fields): GetProviderSchema
// is the only real RPC a data source's own schema needs to answer for
// `ubx sdk gen` to work, and a real, live data-source READ (when one is
// genuinely needed, e.g. resolver-time in ubx core) goes through the
// identical ReadResource RPC a resource already uses -- confirmed
// directly against ubx core's own core/scan.go, not assumed -- never a
// separate ReadDataSource call.
type BuiltDataSource struct {
	DataSourceCandidate
	WireType string
	// RealNamespace is the SAME sanitized (ToSnakeCase'd,
	// override-applied) namespace string already folded into WireType's
	// own service segment -- distinct from DataSourceCandidate.Namespace
	// (the raw, un-sanitized discovered value, e.g. a real Smithy
	// EndpointPrefix like "a2i-runtime.sagemaker") because main.go's own
	// --dump-namespaces branch needs to report exactly what WireType
	// itself was built from, not the raw source string: reporting the
	// raw value here was a real, live-found bug (this session's own
	// full 429-service AWS sweep) -- a2i-runtime.sagemaker's own real
	// hyphen+dot flowed straight into ir.ResourceType.RealNamespace
	// unsanitized, then into a generated Go package/directory name
	// ("sdk/go/aws/data/a2i-runtime.sagemaker/..."), which is not a
	// syntactically valid Go package name at all -- confirmed live via
	// the real generated repo's own CheckNoDuplicateDeclarations parse
	// failure. One shared computation, not two that could drift apart.
	RealNamespace string
	Schema        *tfprotov6.Schema
	ObjectType    tftypes.Object
}

// BuildDataSources runs UBI-186's own real "later step" build.go's own
// doc comment on DataSourceCandidate flagged as deferred: turn each real,
// discovered candidate into a real, servable tfprotov6.Schema.
//
// Mirrors Build's own real create/read merge shape exactly, just with
// one operation's own Input/Output standing in for a resource's separate
// create/read pair: a candidate's Input shape becomes
// Required/Optional attributes (the real lookup/filter arguments a
// caller sets), its Output shape becomes Computed attributes (the real
// values the operation returns) -- uschema.MergeResourceAttributes
// (unchanged, the identical function Build already calls) is what
// reconciles a member appearing on both sides (Optional+Computed) from
// one appearing on only one.
// namespaceOverride, when non-empty, replaces every discovered
// candidate's own Namespace for wire-type derivation only (never the
// DataSourceCandidate.Namespace field itself, which stays the real,
// discovered value) -- config.Provider.DataSourceNamespace's own real,
// live-confirmed reason to exist: a handful of real, permanently
// distinct AWS services share the identical real Smithy
// EndpointPrefix/ArnNamespace by design (that field's own doc comment
// has the full account), which would otherwise collide at the
// dynamic-provider-group merge layer.
func BuildDataSources(doc *Model, providerName string, svc *Service, namespaceOverride string) (map[string]*BuiltDataSource, []string, error) {
	candidates, err := DiscoverDataSources(doc, svc)
	if err != nil {
		return nil, nil, fmt.Errorf("discover data sources: %w", err)
	}

	out := make(map[string]*BuiltDataSource, len(candidates))
	var notes []string
	for _, cand := range candidates {
		conv := NewConverter(doc)
		op := doc.Shapes[cand.OperationID]

		var inputAttrs []*tfprotov6.SchemaAttribute
		if op.Input != nil {
			s, err := conv.Convert(op.Input.Target)
			if err != nil {
				return nil, notes, fmt.Errorf("data source %s: input: %w", cand.Noun, err)
			}
			tr := uschema.NewTranslator()
			inputAttrs = tr.BuildTopLevel(s, providerName+".data."+cand.Noun+".input")
			notes = append(notes, dataSourceSchemaNotes(providerName, cand.Noun, tr.Notes)...)
		}

		var outputAttrs []*tfprotov6.SchemaAttribute
		if op.Output != nil {
			s, err := conv.Convert(op.Output.Target)
			if err != nil {
				return nil, notes, fmt.Errorf("data source %s: output: %w", cand.Noun, err)
			}
			tr := uschema.NewTranslator()
			outputAttrs = tr.BuildTopLevel(s, providerName+".data."+cand.Noun+".output")
			notes = append(notes, dataSourceSchemaNotes(providerName, cand.Noun, tr.Notes)...)
		}

		merged := uschema.MergeResourceAttributes(inputAttrs, outputAttrs)
		if len(merged) == 0 {
			notes = append(notes, fmt.Sprintf("[schema] data %s: neither input nor output yielded any attributes -- skipped", cand.Noun))
			continue
		}

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		objType, ok := block.ValueType().(tftypes.Object)
		if !ok {
			return nil, notes, fmt.Errorf("data source %s: internal error: root schema did not translate to an Object type", cand.Noun)
		}

		wireCand := cand
		if namespaceOverride != "" {
			wireCand.Namespace = namespaceOverride
		}
		wireType := dataSourceWireType(providerName, wireCand)
		if _, exists := out[wireType]; exists {
			notes = append(notes, fmt.Sprintf("[naming] data %s: wire type %q collides with an earlier candidate in this same service -- skipped", cand.Noun, wireType))
			continue
		}

		out[wireType] = &BuiltDataSource{
			DataSourceCandidate: cand,
			WireType:            wireType,
			RealNamespace:       uschema.ToSnakeCase(wireCand.Namespace),
			Schema:              &tfprotov6.Schema{Version: 1, Block: block},
			ObjectType:          objType,
		}
	}

	return out, notes, nil
}

// dataSourceWireType derives a real, service-namespaced wire type
// string -- deliberately never resolved against a curated "confirmed
// real HashiCorp name" table the way naming.go's own Resolve does for
// resources: hashicorp/aws's own real, published data source catalog is
// a few hundred entries, nowhere near enough to cover this package's
// own real, live-confirmed 4,924 discovered candidates across all 430
// services (this session's own live count), so nearly every one of
// these has no real name to match against at all -- Resolve's own
// "honestly report unresolved rather than guess" posture would leave
// almost the entire corpus unnamed.
//
// Folding the candidate's own real namespace (cand.Namespace --
// datasource.go's own EndpointPrefix/ArnNamespace derivation) directly
// into the wire type, mirroring CloudFormation's own real
// "aws_ec2_instance"-shaped resource names rather than HashiCorp's
// older, pre-namespaced "aws_instance" convention, guarantees two
// different services whose own noun happens to collide (EC2's own
// "Instance" and SSO's own "Instance" -- a real, confirmed collision
// class, see naming.go's own doc comment) get distinct wire types by
// construction, with zero curated-alias table to maintain. This is a
// real, deliberate divergence from Build's own resource naming, not an
// oversight: resources are ledger-addressed and drift-sensitive against
// real, already-existing infra, so matching HashiCorp's own real string
// is load-bearing there; a data source here is new, ubiquex-only
// territory with no existing infra depending on a specific string.
func dataSourceWireType(providerName string, cand DataSourceCandidate) string {
	noun := uschema.ToSnakeCase(cand.Noun)
	ns := uschema.ToSnakeCase(cand.Namespace)
	if ns == "" {
		return strings.ToLower(providerName) + "_" + noun
	}
	return strings.ToLower(providerName) + "_" + ns + "_" + noun
}

func dataSourceSchemaNotes(providerName, noun string, notes []uschema.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, fmt.Sprintf("[schema] %s.data.%s.%s: %s", providerName, noun, n.Path, n.Detail))
	}
	return out
}
