// Package dynserver wires layers 1-4 together into a real
// tfprotov6.ProviderServer: resourcemap's discovered resources, schema's
// translated+merged attribute sets, and restexec's real HTTP execution
// against config's own base_url.
package dynserver

import (
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/resourcemap"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// ResourceType is one fully-built, ready-to-serve resource: its discovered
// CRUD operations (resourcemap.Resource) plus its final, merged tfplugin
// schema.
type ResourceType struct {
	resourcemap.Resource
	Schema     *tfprotov6.Schema
	ObjectType tftypes.Object
}

// Build discovers every CRUD-shaped resource in doc and translates each
// one's schema, returning them keyed by TypeName alongside every layer 2/3
// Note collected along the way (translation decisions, skipped/heuristic
// resource-mapping calls) -- the real substance of Phase 1's own "report
// back" requirement.
func Build(doc *openapi3.T, providerName string) (map[string]*ResourceType, []string, error) {
	resources, mapNotes, err := resourcemap.Discover(doc, providerName)
	if err != nil {
		return nil, nil, fmt.Errorf("resource mapping: %w", err)
	}

	var notes []string
	for _, n := range mapNotes {
		notes = append(notes, fmt.Sprintf("[resourcemap] %s: %s", n.Path, n.Detail))
	}

	out := make(map[string]*ResourceType, len(resources))
	for _, res := range resources {
		tr := uschema.NewTranslator()

		var createAttrs []*tfprotov6.SchemaAttribute
		if reqSchema := resourcemap.RequestBodySchema(res.CreateOperation); reqSchema != nil {
			createAttrs = tr.BuildTopLevel(reqSchema, res.TypeName+".create")
		}

		var readAttrs []*tfprotov6.SchemaAttribute
		if _, respSchema := resourcemap.ResponseSchema(res.ReadOperation); respSchema != nil {
			readAttrs = tr.BuildTopLevel(respSchema, res.TypeName+".read")
		}

		merged := uschema.MergeResourceAttributes(createAttrs, readAttrs)
		if len(merged) == 0 {
			notes = append(notes, fmt.Sprintf("[schema] %s: neither the create request body nor the read response yielded any attributes -- skipped, no usable schema", res.TypeName))
			continue
		}

		ensurePathParamsPresent(&merged, res.PathParams)
		ensurePathParamsPresent(&merged, res.CreatePathParams)

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		schemaOut := &tfprotov6.Schema{Version: 1, Block: block}

		objType, ok := block.ValueType().(tftypes.Object)
		if !ok {
			return nil, notes, fmt.Errorf("resource %s: internal error: root schema did not translate to an Object type", res.TypeName)
		}

		for _, n := range tr.Notes {
			notes = append(notes, fmt.Sprintf("[schema] %s.%s: %s", res.TypeName, n.Path, n.Detail))
		}

		out[res.TypeName] = &ResourceType{Resource: res, Schema: schemaOut, ObjectType: objType}
	}

	return out, notes, nil
}

// ensurePathParamsPresent guarantees every read-path {param} segment has a
// corresponding attribute in the merged set, Required+Computed-free (plain
// Required string) if the OpenAPI schemas themselves never surfaced it as
// its own named property -- real APIs very often don't (GitHub's own
// "owner"/"repo" never appear as fields inside the repository JSON object
// itself, only as path segments), and a CRUD executor genuinely cannot
// build a request URL without them being real, settable resource attributes.
func ensurePathParamsPresent(attrs *[]*tfprotov6.SchemaAttribute, pathParams []string) {
	have := map[string]bool{}
	for _, a := range *attrs {
		have[a.Name] = true
	}
	for _, p := range pathParams {
		if have[p] {
			continue
		}
		*attrs = append(*attrs, &tfprotov6.SchemaAttribute{
			Name:        p,
			Type:        tftypes.String,
			Required:    true,
			Description: "path parameter, not part of the API's own resource representation",
		})
		have[p] = true
	}
}
