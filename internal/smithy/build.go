package smithy

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// BuiltResource is one fully-processed Smithy-sourced resource: real
// discovery (resourcemap.go), real schema translation reusing Phase 1's
// unchanged translator (toschema.go), and real naming resolution
// (naming.go) -- everything UBI-158 Phase 4 Checkpoint 1 was asked to
// prove. Deliberately NOT wired into dynserver.ResourceType/Server yet:
// that type's own CRUD fields (ReadPath, CreateMethod, ...) are
// REST-path-shaped, meaningful only once a real per-protocol wire
// executor exists to fill and use them -- Checkpoint 2's own explicit
// scope (item 3, "wire protocols"), not this one. Serving a Smithy-backed
// resource through the real tfplugin RPC surface is Checkpoint 2's
// deliverable; Checkpoint 1's is proving discovery, translation, and
// naming are all real and correct first.
type BuiltResource struct {
	Resource
	HashiCorpName string
	NameStrategy  Strategy
	Schema        *tfprotov6.Schema
	ObjectType    tftypes.Object
}

// Build runs the full Checkpoint 1 pipeline against one service's own
// Smithy model: load, find the service, discover CRUD resources, translate
// each one's schema (reusing internal/schema.Translator, completely
// unchanged, per this phase's own explicit reuse instruction), and resolve
// each one's real HashiCorp-compatible name. providerName is this
// provider's own config name (e.g. "aws") -- used only for Note
// formatting here, since BuiltResource.HashiCorpName (not a
// <providerName>_<noun> derived name) is the real, authoritative type
// name a Smithy-sourced resource actually gets, unlike Phase 1's OpenAPI
// resources.
func Build(doc *Model, providerName string, known KnownNames) (map[string]*BuiltResource, []string, error) {
	svc, err := FindService(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("find service: %w", err)
	}

	resources, mapNotes, err := Discover(doc, svc)
	if err != nil {
		return nil, nil, fmt.Errorf("discover resources: %w", err)
	}

	var notes []string
	for _, n := range mapNotes {
		notes = append(notes, fmt.Sprintf("[resourcemap] %s: %s", n.OperationID, n.Detail))
	}

	out := make(map[string]*BuiltResource, len(resources))
	for _, res := range resources {
		conv := NewConverter(doc)

		createOp := doc.Shapes[res.CreateOperationID]
		var createAttrs []*tfprotov6.SchemaAttribute
		if createOp.Input != nil {
			s, err := conv.Convert(createOp.Input.Target)
			if err != nil {
				return nil, notes, fmt.Errorf("resource %s: create input: %w", res.Noun, err)
			}
			tr := uschema.NewTranslator()
			createAttrs = tr.BuildTopLevel(s, providerName+"."+res.Noun+".create")
			notes = append(notes, schemaNotes(providerName, res.Noun, tr.Notes)...)
		}

		readOp := doc.Shapes[res.ReadOperationID]
		var readAttrs []*tfprotov6.SchemaAttribute
		if readOp.Output != nil {
			s, err := conv.Convert(readOp.Output.Target)
			if err != nil {
				return nil, notes, fmt.Errorf("resource %s: read output: %w", res.Noun, err)
			}
			tr := uschema.NewTranslator()
			readAttrs = tr.BuildTopLevel(s, providerName+"."+res.Noun+".read")
			notes = append(notes, schemaNotes(providerName, res.Noun, tr.Notes)...)
		}

		merged := uschema.MergeResourceAttributes(createAttrs, readAttrs)
		if len(merged) == 0 {
			notes = append(notes, fmt.Sprintf("[schema] %s: neither the create input nor the read output yielded any attributes -- skipped", res.Noun))
			continue
		}

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		objType, ok := block.ValueType().(tftypes.Object)
		if !ok {
			return nil, notes, fmt.Errorf("resource %s: internal error: root schema did not translate to an Object type", res.Noun)
		}

		name, strategy := Resolve(svc, res.Noun, known)
		if strategy == StrategyUnresolved {
			notes = append(notes, fmt.Sprintf("[naming] %s: no confirmed HashiCorp-compatible name found (tried %s and its bare fallback) -- needs a real, curated alias", res.Noun, name))
		}

		out[name] = &BuiltResource{
			Resource:      res,
			HashiCorpName: name,
			NameStrategy:  strategy,
			Schema:        &tfprotov6.Schema{Version: 1, Block: block},
			ObjectType:    objType,
		}
	}

	return out, notes, nil
}

func schemaNotes(providerName, noun string, notes []uschema.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, fmt.Sprintf("[schema] %s.%s.%s: %s", providerName, noun, n.Path, n.Detail))
	}
	return out
}
