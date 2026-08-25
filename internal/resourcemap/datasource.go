package resourcemap

import (
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

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
		service, _, noun, nounNote := deriveNoun(refName, o.path)
		if nounNote != "" {
			notes = append(notes, Note{Path: o.path, Detail: nounNote})
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
