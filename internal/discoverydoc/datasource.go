package discoverydoc

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/typename"
)

// DataSourceCandidate is one real, unclaimed "get"-only node -- this
// package's own real analog of smithy.DataSourceCandidate (that
// package's own doc comment): a real read node Discover already finds
// and, until this file existed, only ever recorded as a discarded Note
// ("no matching create... read-only, modeled as a data source concern,
// not a resource" -- Discover's own doc comment, unchanged). Namespace
// is the identical service string (uschema.ToSnakeCase(doc.Name)[+
// versionQualifier]) TypeName's own real construction already folds in
// via typename.Combine, carried separately here for the same real
// reason smithy.DataSourceCandidate.Namespace is: ir.ServiceAndLocalNameForType
// (ubiquex, sdk/codegen/ir) reads it as RealNamespace to split a wire
// type into (service, local) for the generated aws.data.<service>.<Local>
// import-path shape.
type DataSourceCandidate struct {
	TypeName   string
	Namespace  string
	ReadMethod *rawMethod
}

// DiscoverDataSources walks doc's own real resources tree exactly like
// Discover does (the identical sortedKeys/recursive-walk shape, kept
// deliberately separate rather than a shared internal walker: Discover's
// own real per-node logic -- update/delete detection, resource
// assembly -- has nothing a data-source candidate needs, and a single
// walker parameterized to serve both would obscure more than it'd
// reuse), but collects a "get"-without-create node as a real candidate
// instead of discarding it as a Note. seenTypeNames is scoped to data
// sources only -- a data source MAY legitimately claim the same
// TypeName a resource elsewhere in this same document already claimed
// (hashicorp/aws's own real "aws_instance" is both a resource and a
// data source; ir.ResourceType.IsDataSource, not the wire type string,
// is what keeps the two apart downstream) -- only two data-source
// candidates colliding with EACH OTHER is a real problem worth a Note.
func DiscoverDataSources(doc *Document, providerName string, versionQualifier string) ([]DataSourceCandidate, []Note, error) {
	var candidates []DataSourceCandidate
	var notes []Note
	seenTypeNames := map[string]bool{}

	var walk func(node map[string]*rawResource, path []string)
	walk = func(node map[string]*rawResource, path []string) {
		for _, name := range sortedKeys(node) {
			r := node[name]
			nodePath := append(append([]string{}, path...), name)
			pathStr := strings.Join(nodePath, ".")

			if get, ok := r.Methods["get"]; ok && get != nil {
				_, createFound := firstMethod(r.Methods, "create", "insert")
				if !createFound {
					_, createFound = firstPrefixedMethod(r.Methods, "create", "insert")
				}
				if !createFound {
					noun := singularize(uschema.ToSnakeCase(name))
					service := uschema.ToSnakeCase(doc.Name)
					if versionQualifier != "" {
						service += "_" + versionQualifier
					}
					typeName := typename.Combine(providerName, service, noun)
					if seenTypeNames[typeName] {
						notes = append(notes, Note{Path: pathStr, Detail: "data source type name \"" + typeName + "\" already claimed by another read-only path -- skipped rather than disambiguated"})
					} else {
						seenTypeNames[typeName] = true
						candidates = append(candidates, DataSourceCandidate{TypeName: typeName, Namespace: service, ReadMethod: get})
					}
				}
			}

			if r.Resources != nil {
				walk(r.Resources, nodePath)
			}
		}
	}
	walk(doc.Resources, nil)

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].TypeName < candidates[j].TypeName })
	return candidates, notes, nil
}

// BuiltDataSource is BuildDataSources' own real per-candidate result --
// mirrors smithy.BuiltDataSource in shape (no execution seam: a real,
// live data-source read goes through ReadResource, keyed by TypeName,
// never a separate RPC -- see that type's own doc comment for the full
// account, identical here).
type BuiltDataSource struct {
	DataSourceCandidate
	Schema *tfprotov6.Schema
}

// BuildDataSources translates every DiscoverDataSources candidate into a
// real, servable *tfprotov6.Schema -- mirrors Build's own real create/
// read merge shape (uschema.MergeResourceAttributes, unchanged, the
// identical function Build already calls): a candidate's own real query
// Parameters become Required/Optional attributes (the real lookup
// arguments a caller sets -- a "get" method's own real parameters, never
// a request body, since a real REST GET carries none), its Response
// becomes Computed attributes (the real values the operation returns).
func BuildDataSources(doc *Document, providerName string, versionQualifier string) (map[string]*BuiltDataSource, []Note, error) {
	candidates, notes, err := DiscoverDataSources(doc, providerName, versionQualifier)
	if err != nil {
		return nil, notes, err
	}

	out := make(map[string]*BuiltDataSource, len(candidates))
	for _, cand := range candidates {
		tr := uschema.NewTranslator()

		inputAttrs := buildParamAttrs(cand.ReadMethod, cand.TypeName, tr)

		var outputAttrs []*tfprotov6.SchemaAttribute
		if respSchema := resolveRef(cand.ReadMethod.Response, doc); respSchema != nil {
			outputAttrs = tr.BuildTopLevel(respSchema, cand.TypeName+".output")
		}

		merged := uschema.MergeResourceAttributes(inputAttrs, outputAttrs)
		if len(merged) == 0 {
			notes = append(notes, Note{Path: cand.TypeName, Detail: "neither the read parameters nor the read response yielded any attributes -- skipped, no usable schema"})
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

// buildParamAttrs translates get's own real query/path Parameters into
// attributes -- a real REST GET carries these instead of a JSON request
// body (rawMethod.Request is always nil for a "get" method, confirmed
// against this package's own real, live Pub/Sub verification document),
// so this synthesizes a plain object schema from Parameters directly
// (each one flat/scalar -- string/integer/number/boolean, the same real
// primitive vocabulary convertSchema's own switch already handles) and
// feeds it through the identical, unchanged tr.BuildTopLevel every other
// real schema in this package goes through, rather than hand-building
// tfprotov6.SchemaAttribute values in a second, parallel way.
//
// Real, deliberate divergence from a body schema's own real "no
// required signal at the schema level" finding (this package's own doc
// comment): a Discovery Document's PARAMETER shape is structurally
// different and DOES carry a real, explicit Required boolean per
// parameter (confirmed live against Pub/Sub's own real "get" methods) --
// set directly onto the synthesized schema's own Required list here,
// the one real place this distinction needs to be reconciled.
func buildParamAttrs(get *rawMethod, typeName string, tr *uschema.Translator) []*tfprotov6.SchemaAttribute {
	if len(get.Parameters) == 0 {
		return nil
	}
	names := make([]string, 0, len(get.Parameters))
	for name := range get.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)

	s := openapi3.NewObjectSchema()
	for _, name := range names {
		p := get.Parameters[name]
		s.WithPropertyRef(name, openapi3.NewSchemaRef("", paramPrimitiveSchema(p.Type)))
		if p.Required {
			s.Required = append(s.Required, name)
		}
	}
	return tr.BuildTopLevel(s, typeName+".input")
}

// paramPrimitiveSchema mirrors convertSchema's own real primitive-type
// switch (string/integer/number/boolean) -- a real query/path parameter
// is always flat/scalar, never an object/array itself, so this needs
// none of convertSchema's own recursive object/array/ref handling, just
// the same leaf-type vocabulary.
func paramPrimitiveSchema(t string) *openapi3.Schema {
	switch t {
	case "string":
		return openapi3.NewStringSchema()
	case "integer", "number":
		return openapi3.NewFloat64Schema()
	case "boolean":
		return openapi3.NewBoolSchema()
	default:
		return openapi3.NewSchema()
	}
}
