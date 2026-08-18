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

		// createOp's own OUTPUT is real, additional schema signal Checkpoint
		// 1 did not yet translate -- a real, structural gap Checkpoint 2's
		// own live testing surfaced (not assumed from reading the code):
		// SQS's own real CreateQueueRequest (create input) and
		// GetQueueAttributesResult (read output) neither one carries
		// QueueUrl at all -- it exists ONLY on CreateQueueResult (create
		// OUTPUT), yet GetQueueAttributes' own real INPUT REQUIRES it to
		// look the queue up at all. Without merging create's own output
		// too, "queue_url" was never a real schema attribute, and a real
		// ReadResource call had no identifying value to send -- confirmed
		// live via TestServer_RealSQSReadResource failing with an empty
		// request body before this fix.
		var createOutputAttrs []*tfprotov6.SchemaAttribute
		if createOp.Output != nil {
			s, err := conv.Convert(createOp.Output.Target)
			if err != nil {
				return nil, notes, fmt.Errorf("resource %s: create output: %w", res.Noun, err)
			}
			tr := uschema.NewTranslator()
			createOutputAttrs = tr.BuildTopLevel(s, providerName+"."+res.Noun+".create_output")
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

		merged := uschema.MergeResourceAttributes(uschema.MergeResourceAttributes(createAttrs, createOutputAttrs), readAttrs)
		if len(merged) == 0 {
			notes = append(notes, fmt.Sprintf("[schema] %s: neither the create input/output nor the read output yielded any attributes -- skipped", res.Noun))
			continue
		}

		ensureIdentifyingAttrsPresent(&merged, doc, res.ReadOperationID)

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

// ensureIdentifyingAttrsPresent guarantees every REQUIRED member of
// readOperationID's own real input shape has a corresponding schema
// attribute -- Smithy's own real equivalent of dynserver/build.go's
// identical ensurePathParamsPresent, needed for the identical real reason:
// a CRUD executor cannot look a resource instance up without a real value
// to send, and a real AWS read operation's own required input (e.g. SQS's
// GetQueueAttributes needs QueueUrl) is not guaranteed to appear in create
// input/output or read output at all -- merging create's own output
// (this file's own recent change) covers SQS's own real case, but is not a
// universal guarantee across every real AWS service's own shape, so this
// exists as the same honest fallback dynserver's own OpenAPI path takes:
// synthesize a plain, Required string attribute for anything still
// missing, rather than leaving the resource unable to address its own
// instances.
func ensureIdentifyingAttrsPresent(attrs *[]*tfprotov6.SchemaAttribute, doc *Model, readOperationID string) {
	readOp, ok := doc.Shapes[readOperationID]
	if !ok || readOp.Input == nil {
		return
	}
	inputShape, ok := doc.Shapes[readOp.Input.Target]
	if !ok {
		return
	}
	have := map[string]bool{}
	for _, a := range *attrs {
		have[a.Name] = true
	}
	for memberName, member := range inputShape.Members {
		if !member.HasTrait("smithy.api#required") {
			continue
		}
		snake := uschema.ToSnakeCase(memberName)
		if have[snake] {
			continue
		}
		*attrs = append(*attrs, &tfprotov6.SchemaAttribute{
			Name:        snake,
			Type:        tftypes.String,
			Required:    true,
			Description: "identifying value required by the read operation, not part of the API's own create/read response representation",
		})
		have[snake] = true
	}
}

func schemaNotes(providerName, noun string, notes []uschema.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, fmt.Sprintf("[schema] %s.%s.%s: %s", providerName, noun, n.Path, n.Detail))
	}
	return out
}
