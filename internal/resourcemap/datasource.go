package resourcemap

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/dsfilter"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/typename"
)

// DataSourceCandidate is one real, unclaimed GET -- this package's own
// real analog of smithy.DataSourceCandidate/discoverydoc.DataSourceCandidate
// (those packages' own doc comments): a real GET operation Discover's
// own Phase 1 already finds and, until this file existed, only ever
// recorded as a discarded Note ("no matching create... read-only,
// modeled as a data source concern, not a resource (out of Phase 1
// scope)"). Namespace mirrors the other two sources' own identical
// field -- the service segment deriveNoun already extracts, carried
// separately for ir.ServiceAndLocalNameForType (ubiquex) to read as
// RealNamespace.
type DataSourceCandidate struct {
	TypeName  string
	Namespace string
	Operation *openapi3.Operation
}

// DiscoverDataSources widens the real candidate surface beyond
// Discover's own Phase 1 read-candidate set (filterReadCandidates' own
// "GET on a path ending in {param}" filter) -- deliberately, mirroring
// smithy's own UBI-186 finding that widening the unclaimed-operation
// surface (adding List alongside Get/Describe) roughly doubled AWS's
// own real data-source candidate pool: a real GitHub/Datadog/Kubernetes
// document's own real read-only surface includes plenty of genuinely
// useful collection-shaped GETs (list-all-X, no trailing {param}) a
// resource-pairing heuristic has no reason to consider, but a data
// source catalog benefits from including. Calls Discover once to learn
// which (path, method) pairs are already claimed as some resource's own
// ReadPath, then walks every real GET in the document, keeping whichever
// ones aren't. seenTypeNames is scoped to data sources only -- see the
// other two sources' own identical DataSourceCandidate doc comments for
// why a data source MAY legitimately share a TypeName with a resource.
func DiscoverDataSources(doc *openapi3.T, providerName string) ([]DataSourceCandidate, []Note, error) {
	resources, _, err := Discover(doc, providerName)
	if err != nil {
		return nil, nil, err
	}
	claimedReadPaths := make(map[string]bool, len(resources))
	for _, r := range resources {
		claimedReadPaths[r.ReadPath] = true
	}

	var ops []op
	if doc.Paths != nil {
		for _, path := range doc.Paths.InMatchingOrder() {
			item := doc.Paths.Find(path)
			if item == nil || item.Get == nil {
				continue
			}
			ops = append(ops, op{method: "GET", path: path, sub: item.Get})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path < ops[j].path })

	var candidates []DataSourceCandidate
	var notes []Note
	seenTypeNames := map[string]bool{}

	for _, o := range ops {
		if claimedReadPaths[o.path] {
			continue
		}
		refName, respSchema := ResponseSchema(o.sub)
		if respSchema == nil {
			notes = append(notes, Note{Path: o.path, Detail: "GET has no JSON response schema -- skipped, cannot derive a data source shape"})
			continue
		}

		// A real collection-listing GET's own response is usually not
		// the item type itself but a wrapper around it (Azure's own
		// real "TargetTypeListResult", Datadog's own real
		// "MetricsListResponse", Kubernetes' own real "PodList") --
		// deriveNoun, reused unchanged from resource discovery, has no
		// reason to know that and takes the wrapper's own name
		// verbatim, so a data source ends up named
		// "chaos_target_type_list_result" instead of the real,
		// meaningful "chaos_target_type". itemRefName, when found,
		// substitutes the wrapper's own name with the real item type's
		// -- SHAPE-based (a single array property whose items $ref a
		// distinct named schema), not a name-suffix guess, so a real,
		// unrelated domain type that merely ends in "List" (a security
		// allow-list, say -- a flat array of strings, not a wrapper
		// around another named type) is never mistakenly unwrapped.
		nounSourceRefName := refName
		if item, ok := collectionItemRefName(respSchema); ok {
			nounSourceRefName = item
		}

		service, _, noun, nounNote := deriveNoun(nounSourceRefName, o.path)
		if nounNote != "" {
			notes = append(notes, Note{Path: o.path, Detail: nounNote})
		}

		operationName := ""
		if o.sub != nil {
			operationName = o.sub.OperationID
		}
		if reason, excluded := dsfilter.Excluded(dsfilter.Candidate{
			Noun:             noun,
			Path:             o.path,
			OperationName:    operationName,
			ResponseTypeName: refName,
		}); excluded {
			notes = append(notes, Note{Path: o.path, Detail: "excluded from data-source candidates: " + string(reason)})
			continue
		}

		typeName := typename.Combine(providerName, service, noun)
		if seenTypeNames[typeName] {
			notes = append(notes, Note{Path: o.path, Detail: "data source type name \"" + typeName + "\" already claimed by another read-only path -- skipped rather than disambiguated"})
			continue
		}
		seenTypeNames[typeName] = true
		candidates = append(candidates, DataSourceCandidate{TypeName: typeName, Namespace: service, Operation: o.sub})
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].TypeName < candidates[j].TypeName })
	return candidates, notes, nil
}

// knownEnvelopeMetadataProperties are real, common pagination/metadata
// property names a collection-envelope object carries alongside its own
// real array-of-items property -- Azure ARM's own "nextLink", OData's
// own "@odata.nextLink"/"@odata.count", Kubernetes' own
// "metadata"/"kind"/"apiVersion"/"continue"/"resourceVersion", generic
// "totalCount"/"count" pagination fields. Ignored when looking for the
// ONE real item-array property, so their presence never prevents a
// genuine collection wrapper from being recognized.
var knownEnvelopeMetadataProperties = map[string]bool{
	"nextLink":        true,
	"@odata.nextLink": true,
	"@odata.count":    true,
	"metadata":        true,
	"kind":            true,
	"apiVersion":      true,
	"continue":        true,
	"resourceVersion": true,
	"totalCount":      true,
	"count":           true,
	"self":            true,
	"links":           true,
}

// collectionItemRefName reports the real item type a collection-envelope
// response wraps, when schema is confidently shaped like one: exactly
// one property (beyond knownEnvelopeMetadataProperties) that is an array
// whose own items reference a distinct, named component schema. This is
// a SHAPE test, not a name-suffix guess -- see DiscoverDataSources' own
// call site for why that distinction matters (a real domain type that
// merely ends in "List" -- a flat array of strings, no $ref -- never
// matches here, so it's never mistakenly unwrapped).
func collectionItemRefName(schema *openapi3.Schema) (string, bool) {
	if schema == nil || len(schema.Properties) == 0 {
		return "", false
	}
	var itemRef string
	found := 0
	for name, propRef := range schema.Properties {
		if knownEnvelopeMetadataProperties[strings.ToLower(name)] {
			continue
		}
		if propRef == nil || propRef.Value == nil {
			continue
		}
		prop := propRef.Value
		if !prop.Type.Is("array") || prop.Items == nil {
			continue
		}
		ref := refString(prop.Items.Ref)
		if ref == "" {
			continue
		}
		found++
		itemRef = ref
	}
	if found != 1 {
		return "", false
	}
	return itemRef, true
}

// BuiltDataSource mirrors smithy.BuiltDataSource/discoverydoc.BuiltDataSource
// in shape -- no execution seam, for the identical real reason (a live
// data-source read goes through ReadResource, keyed by TypeName, never
// a separate RPC).
type BuiltDataSource struct {
	DataSourceCandidate
	Schema *tfprotov6.Schema
}

// BuildDataSources translates every DiscoverDataSources candidate into a
// real, servable *tfprotov6.Schema -- mirrors Build's own real create/
// read merge shape (uschema.MergeResourceAttributes, unchanged): a
// candidate's own real query/path Parameters become Required/Optional
// attributes, its own response schema becomes Computed attributes.
func BuildDataSources(doc *openapi3.T, providerName string) (map[string]*BuiltDataSource, []Note, error) {
	candidates, notes, err := DiscoverDataSources(doc, providerName)
	if err != nil {
		return nil, notes, err
	}

	out := make(map[string]*BuiltDataSource, len(candidates))
	for _, cand := range candidates {
		tr := uschema.NewTranslator()

		inputAttrs := buildOperationParamAttrs(cand.Operation, cand.TypeName, tr)

		var outputAttrs []*tfprotov6.SchemaAttribute
		if _, respSchema := ResponseSchema(cand.Operation); respSchema != nil {
			outputAttrs = tr.BuildTopLevel(respSchema, cand.TypeName+".output")
		}

		merged := uschema.MergeResourceAttributes(inputAttrs, outputAttrs)
		if len(merged) == 0 {
			notes = append(notes, Note{Path: cand.TypeName, Detail: "neither the operation's own parameters nor its response yielded any attributes -- skipped, no usable schema"})
			continue
		}
		for _, n := range tr.Notes {
			notes = append(notes, Note{Path: cand.TypeName + "." + n.Path, Detail: n.Detail})
		}

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		out[cand.TypeName] = &BuiltDataSource{
			DataSourceCandidate: cand,
			Schema:              &tfprotov6.Schema{Version: 1, Block: block},
		}
	}

	return out, notes, nil
}

// buildOperationParamAttrs translates op's own real query/path
// Parameters (native openapi3.Parameter values -- this source, unlike
// discoverydoc's raw JSON vocabulary, already carries a real
// *openapi3.SchemaRef per parameter, so no separate primitive-type
// mapping is needed here at all) into attributes, synthesizing a plain
// object schema and feeding it through the identical, unchanged
// tr.BuildTopLevel every other real schema in this package goes
// through. Header/cookie parameters are deliberately excluded -- real
// auth/content-negotiation plumbing (Authorization, Accept, ...), never
// a real lookup argument a caller of the generated data source would
// set.
func buildOperationParamAttrs(op *openapi3.Operation, typeName string, tr *uschema.Translator) []*tfprotov6.SchemaAttribute {
	if op == nil || len(op.Parameters) == 0 {
		return nil
	}
	s := openapi3.NewObjectSchema()
	var names []string
	byName := map[string]*openapi3.Parameter{}
	for _, pRef := range op.Parameters {
		if pRef == nil || pRef.Value == nil {
			continue
		}
		p := pRef.Value
		if p.In != "query" && p.In != "path" {
			continue
		}
		if p.Schema == nil || p.Schema.Value == nil {
			continue
		}
		names = append(names, p.Name)
		byName[p.Name] = p
	}
	sort.Strings(names)
	for _, name := range names {
		p := byName[name]
		s.WithPropertyRef(name, p.Schema)
		if p.Required {
			s.Required = append(s.Required, name)
		}
	}
	if len(s.Properties) == 0 {
		return nil
	}
	return tr.BuildTopLevel(s, typeName+".input")
}
