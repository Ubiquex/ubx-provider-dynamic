// Package dynserver wires layers 1-4 together into a real
// tfprotov6.ProviderServer: resourcemap's discovered resources, schema's
// translated+merged attribute sets, and restexec's real HTTP execution
// against config's own base_url.
package dynserver

import (
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/resourcemap"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// ResourceType is one fully-built, ready-to-serve resource: its discovered
// CRUD operations (resourcemap.Resource), its final, merged tfplugin
// schema, and its own resolved UBI-158 Phase 3 execution policy (Timeouts
// are provider-wide, so every ResourceType shares one resolved value;
// Async/Drift are genuinely per-resource-type, resolved from
// cfg.Resources[TypeName] if present, zero-value otherwise -- Async's own
// zero value has Enabled=false, Drift's own zero value ignores/normalizes
// nothing, both real, safe "do nothing extra" defaults for a resource a
// config author hasn't configured either for).
type ResourceType struct {
	resourcemap.Resource
	Schema     *tfprotov6.Schema
	ObjectType tftypes.Object
	Timeouts   Timeouts
	Async      AsyncPolicy
	Drift      DriftPolicy

	// PathParamAttr/CreatePathParamAttr map a real URL template parameter
	// name (Resource.PathParams/CreatePathParams' own entries, which MUST
	// stay literal -- restexec.BuildPath matches them against the "{name}"
	// segments actually written in ReadPath/CreatePath, a real template,
	// never renamed) to the SCHEMA ATTRIBUTE name state should actually be
	// read from to get that parameter's own value. Identity for the
	// overwhelming common case (no entry at all -- read the state
	// attribute with the same name as the template parameter); only
	// populated when ensurePathParamsPresent found a genuine name
	// collision and had to synthesize a differently-named attribute (see
	// its own doc comment) -- e.g. template parameter "owner" but schema
	// attribute "owner_path", because "owner" itself is already a real,
	// differently-typed response attribute on this type.
	PathParamAttr       map[string]string
	CreatePathParamAttr map[string]string

	// Signals carries the real enum/constraint data uschema.CollectSignals
	// found in this resource's own create-request and read-response
	// OpenAPI schemas, merged into one combined tree (uschema.MergeSignalMaps)
	// the same way merged above already combines createAttrs/readAttrs
	// themselves -- keyed by ToSnakeCase(name) at every level, matching
	// this resource's own Schema attribute names (and therefore
	// ir.Field.WireName, on ubiquex's own side) exactly. nil when the
	// underlying schemas carried no real constraint/enum signal at all,
	// never a placeholder empty map.
	Signals map[string]*uschema.FieldSignal
}

// Build discovers every CRUD-shaped resource in doc and translates each
// one's schema, returning them keyed by TypeName alongside every layer 2/3
// Note collected along the way (translation decisions, skipped/heuristic
// resource-mapping calls) -- the real substance of Phase 1's own "report
// back" requirement. cfg supplies Phase 3's own execution-semantics
// config (retry/timeouts/async/drift); the retry policy itself is
// resolved by the caller (main.go) directly onto the restexec.Client, not
// here -- Build's own job is per-resource-type policy, retry is
// provider-wide and belongs on the Client that already carries BaseURL/
// Authenticator.
func Build(doc *openapi3.T, providerName string, cfg config.Provider) (map[string]*ResourceType, []string, error) {
	resources, mapNotes, err := resourcemap.Discover(doc, providerName)
	if err != nil {
		return nil, nil, fmt.Errorf("resource mapping: %w", err)
	}

	timeouts, err := resolveTimeouts(cfg.Timeouts)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve timeouts: %w", err)
	}

	var notes []string
	for _, n := range mapNotes {
		notes = append(notes, fmt.Sprintf("[resourcemap] %s: %s", n.Path, n.Detail))
	}

	out := make(map[string]*ResourceType, len(resources))
	for _, res := range resources {
		tr := uschema.NewTranslator()

		var createAttrs []*tfprotov6.SchemaAttribute
		var signals map[string]*uschema.FieldSignal
		if reqSchema := resourcemap.RequestBodySchema(res.CreateOperation); reqSchema != nil {
			createAttrs = tr.BuildTopLevel(reqSchema, res.TypeName+".create")
			signals = uschema.MergeSignalMaps(signals, uschema.CollectSignals(reqSchema))
		}

		var readAttrs []*tfprotov6.SchemaAttribute
		if _, respSchema := resourcemap.ResponseSchema(res.ReadOperation); respSchema != nil {
			readAttrs = tr.BuildTopLevel(respSchema, res.TypeName+".read")
			signals = uschema.MergeSignalMaps(signals, uschema.CollectSignals(respSchema))
		}

		merged := uschema.MergeResourceAttributes(createAttrs, readAttrs)
		if len(merged) == 0 {
			notes = append(notes, fmt.Sprintf("[schema] %s: neither the create request body nor the read response yielded any attributes -- skipped, no usable schema", res.TypeName))
			continue
		}

		pathParamAttr := ensurePathParamsPresent(&merged, res.PathParams)
		if len(pathParamAttr) > 0 {
			notes = append(notes, renameNotes(res.TypeName, pathParamAttr)...)
		}
		createPathParamAttr := ensurePathParamsPresent(&merged, res.CreatePathParams)
		if len(createPathParamAttr) > 0 {
			notes = append(notes, renameNotes(res.TypeName, createPathParamAttr)...)
		}

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		schemaOut := &tfprotov6.Schema{Version: 1, Block: block}

		objType, ok := block.ValueType().(tftypes.Object)
		if !ok {
			return nil, notes, fmt.Errorf("resource %s: internal error: root schema did not translate to an Object type", res.TypeName)
		}

		for _, n := range tr.Notes {
			notes = append(notes, fmt.Sprintf("[schema] %s.%s: %s", res.TypeName, n.Path, n.Detail))
		}

		resCfg := cfg.Resources[res.TypeName]
		async, err := resolveAsyncPolicy(resCfg.Async)
		if err != nil {
			return nil, notes, fmt.Errorf("resource %s: resolve async policy: %w", res.TypeName, err)
		}
		drift, err := resolveDriftPolicy(resCfg.Drift)
		if err != nil {
			return nil, notes, fmt.Errorf("resource %s: resolve drift policy: %w", res.TypeName, err)
		}

		out[res.TypeName] = &ResourceType{
			Resource:            res,
			Schema:              schemaOut,
			ObjectType:          objType,
			Timeouts:            timeouts,
			Async:               async,
			Drift:               drift,
			PathParamAttr:       pathParamAttr,
			CreatePathParamAttr: createPathParamAttr,
			Signals:             signals,
		}
	}

	return out, notes, nil
}

// ensurePathParamsPresent guarantees every read-path {param} segment has a
// corresponding, real STRING-or-number attribute in the merged set,
// Required+Computed-free (plain Required string) if the OpenAPI schemas
// themselves never surfaced it as its own named property -- real APIs very
// often don't (GitHub's own "repo" never appears as a field inside the
// repository JSON object itself, only as a path segment), and a CRUD
// executor genuinely cannot build a request URL without them being real,
// settable resource attributes.
//
// Real, confirmed finding, UBI-158 Phase 5 (the conformance gate): a path
// parameter's own name can genuinely COLLIDE with an EXISTING, differently
// -typed response attribute -- confirmed live against GitHub's own real
// github_full_repository: the read path is "/repos/{owner}/{repo}", but
// the response body ALSO carries its own real "owner" field, a nested
// OBJECT (login/id/avatar_url/...), not a string. Before this fix, the
// object attribute silently won -- extractStringAttrs/requestBody then
// failed every real ReadResource/ApplyResourceChange call for this type
// with "attribute type ... cannot be used as a path parameter," a
// complete, structural inability to serve the resource at all, caught via
// this session's own live conformance probes, not by inspection. The fix:
// when the EXISTING attribute at that name is not itself string/number
// (i.e. not something extractStringAttrs could ever use), leave it alone
// (it's real, legitimate response data) and add a DISTINCTLY named
// synthetic path-parameter attribute instead ("<name>_path", extended with
// trailing underscores in the vanishingly unlikely event even THAT
// collides) -- the returned map (template param name -> real attribute
// name) becomes ResourceType.PathParamAttr/CreatePathParamAttr, since
// Resource.PathParams/CreatePathParams themselves must stay literal (they
// match ReadPath/CreatePath's own real "{name}" URL template segments).
func ensurePathParamsPresent(attrs *[]*tfprotov6.SchemaAttribute, pathParams []string) map[string]string {
	have := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range *attrs {
		have[a.Name] = a
	}
	renames := map[string]string{}
	for _, p := range pathParams {
		if existing, ok := have[p]; ok {
			// existing.Type is nil for a NestedType-shaped attribute (a
			// real, common case -- GitHub's own "owner" is exactly this:
			// a nested single-object attribute, Type unset, NestedType
			// populated instead) -- never usable as a plain string path
			// parameter regardless, so treat nil the same as any other
			// incompatible type rather than panicking on a nil-interface
			// method call.
			if existing.Type != nil && (existing.Type.Is(tftypes.String) || existing.Type.Is(tftypes.Number)) {
				continue
			}
			newName := p + "_path"
			for have[newName] != nil {
				newName += "_"
			}
			synthetic := &tfprotov6.SchemaAttribute{
				Name:     newName,
				Type:     tftypes.String,
				Required: true,
				Description: "path parameter, not part of the API's own resource representation " +
					"(renamed from \"" + p + "\": that name is already used by a differently-typed, real response attribute)",
			}
			*attrs = append(*attrs, synthetic)
			have[newName] = synthetic
			renames[p] = newName
			continue
		}
		synthetic := &tfprotov6.SchemaAttribute{
			Name:        p,
			Type:        tftypes.String,
			Required:    true,
			Description: "path parameter, not part of the API's own resource representation",
		}
		*attrs = append(*attrs, synthetic)
		have[p] = synthetic
	}
	return renames
}

// renameNotes reports every real path-parameter rename ensurePathParamsPresent
// made for typeName -- surfaced the same way every other real, load-bearing
// translation decision in this codebase is (schema.Note, resourcemap.Note),
// never silently applied.
func renameNotes(typeName string, renames map[string]string) []string {
	names := make([]string, 0, len(renames))
	for old := range renames {
		names = append(names, old)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, old := range names {
		out = append(out, fmt.Sprintf("[build] %s: path parameter %q renamed to %q -- %q is already a real, differently-typed response attribute on this type", typeName, old, renames[old], old))
	}
	return out
}
