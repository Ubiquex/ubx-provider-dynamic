package smithy

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

// Note is one real, specific Smithy->OpenAPI-shape translation decision
// worth surfacing, mirroring schema.Note's own role (internal/schema) for
// the OpenAPI->tfplugin layer this package feeds into.
type Note struct {
	ShapeID string
	Detail  string
}

// Converter converts Smithy shapes into *openapi3.Schema trees -- the
// real reuse seam UBI-158 Phase 4 was asked to build: the OUTPUT type
// system is openapi3.Schema, the exact same input internal/schema's own
// Translator (built for Phase 1's OpenAPI source) already consumes
// unchanged. This package's only real job is producing that shape
// correctly from Smithy's own, structurally different source format --
// no second tfplugin-facing translator exists or is needed.
//
// Memoized by shapeId, not rebuilt per reference: a shape referenced
// multiple times (including a real, genuinely self-referential shape --
// confirmed to exist in real AWS models, e.g. recursive filter/expression
// trees in some services) always resolves to the IDENTICAL *openapi3.Schema
// pointer. This is what lets internal/schema.Translator's own existing
// cycle guard (Translator.active, keyed on *openapi3.Schema pointer
// identity -- see translate.go's own doc comment) catch a Smithy-sourced
// cycle with zero new code: the guard was built pointer-identity-generic
// from the start, not OpenAPI-specific.
type Converter struct {
	doc   *Model
	cache map[string]*openapi3.Schema
	Notes []Note
}

// NewConverter returns a ready-to-use Converter over doc's own shape map.
func NewConverter(doc *Model) *Converter {
	return &Converter{doc: doc, cache: map[string]*openapi3.Schema{}}
}

func (c *Converter) note(shapeID, format string, args ...any) {
	c.Notes = append(c.Notes, Note{ShapeID: shapeID, Detail: fmt.Sprintf(format, args...)})
}

// unitShapeID is Smithy's own built-in "no data" shape -- a real
// operation with no meaningful input/output (e.g. a bare Delete call)
// targets it directly, and every enum member also targets it (the
// member's own real value lives in its smithy.api#enumValue trait
// instead, never in the target). Resolves to a real, empty object schema
// -- correct for a genuinely empty input/output; enum member handling
// never calls Convert on it at all (fillEnum reads the trait directly).
const unitShapeID = "smithy.api#Unit"

// Convert resolves shapeID into an *openapi3.Schema, recursing into
// referenced shapes as needed.
func (c *Converter) Convert(shapeID string) (*openapi3.Schema, error) {
	if shapeID == unitShapeID {
		return &openapi3.Schema{Properties: openapi3.Schemas{}}, nil
	}
	if cached, ok := c.cache[shapeID]; ok {
		return cached, nil
	}
	shape, ok := c.doc.Shapes[shapeID]
	if !ok {
		return nil, fmt.Errorf("unresolved shape reference %q", shapeID)
	}

	s := &openapi3.Schema{}
	// Memoized BEFORE recursing into members -- required for a real
	// self-referential shape to terminate at all (see the package doc
	// comment): the second reference to shapeID, reached mid-recursion,
	// must observe this same, already-cached (if still being filled in)
	// pointer, not attempt to build a second one and recurse forever.
	c.cache[shapeID] = s

	if desc, present, err := stringTrait(shape, "smithy.api#documentation"); err != nil {
		return nil, fmt.Errorf("%s: %w", shapeID, err)
	} else if present {
		s.Description = desc
	}

	var err error
	switch shape.Type {
	case "structure":
		err = c.fillStructure(shapeID, shape, s)
	case "union":
		err = c.fillUnion(shapeID, shape, s)
	case "list", "set":
		err = c.fillList(shapeID, shape, s)
	case "map":
		err = c.fillMap(shapeID, shape, s)
	case "enum":
		err = c.fillEnum(shapeID, shape, s, false)
	case "intEnum":
		err = c.fillEnum(shapeID, shape, s, true)
	case "string":
		s.Type = &openapi3.Types{"string"}
	case "boolean":
		s.Type = &openapi3.Types{"boolean"}
	case "integer", "long", "short", "byte":
		s.Type = &openapi3.Types{"integer"}
	case "float", "double", "bigDecimal", "bigInteger":
		s.Type = &openapi3.Types{"number"}
	case "blob":
		s.Type = &openapi3.Types{"string"}
		c.note(shapeID, "blob shape typed as string -- real wire protocols base64-encode blob payloads over JSON (restJson1/awsJson1_x/restXml text nodes), so this is a faithful, non-lossy fit for those; a raw binary body binding (rare, real only for a handful of S3 operations, none of which are CRUD-resource-shaped) would be genuinely lossy under this mapping, but this phase never reaches one")
	case "timestamp":
		s.Type = &openapi3.Types{"string"}
		s.Format = "date-time"
	case "document":
		c.note(shapeID, "Smithy document shape (genuinely arbitrary, self-describing JSON, Smithy's own real equivalent of OpenAPI's schema-less object) -- left schema-less here on purpose; internal/schema.Translator's own existing free-form-object fallback resolves this to tfplugin's real DynamicPseudoType, the identical honest degradation OpenAPI's own unconstrained objects already get")
	default:
		c.note(shapeID, "unrecognized Smithy shape type %q -- left schema-less (resolves to dynamic)", shape.Type)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// fillStructure translates a structure shape's own members into object
// properties, collecting Required from each MEMBER's own
// smithy.api#required trait -- a real, notable format difference from
// OpenAPI/JSON-Schema, which puts "required" as one list on the parent
// object instead of a trait on each child. Reconciled here, once, rather
// than needing internal/schema.Translator to know two different
// conventions: by the time a Smithy-sourced schema reaches that package,
// it already looks exactly like an OpenAPI one.
func (c *Converter) fillStructure(shapeID string, shape Shape, s *openapi3.Schema) error {
	s.Type = &openapi3.Types{"object"}
	s.Properties = openapi3.Schemas{}
	for _, name := range sortedMemberNames(shape.Members) {
		member := shape.Members[name]
		memberSchema, err := c.Convert(member.Target)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", shapeID, name, err)
		}
		s.Properties[name] = openapi3.NewSchemaRef("", memberSchema)
		if member.HasTrait("smithy.api#required") {
			s.Required = append(s.Required, name)
		}
	}
	return nil
}

// fillUnion translates Smithy's own tagged-union shape (real, exactly-
// one-member-set semantics) into a real oneOf of single-property,
// single-required-field object branches -- one branch per union member,
// each shaped exactly like "this member, and only this member, is set."
// This is a faithful, non-invented mapping onto OpenAPI's own oneOf
// construct, which activates internal/schema.Translator's own existing
// oneOf-of-objects-with-no-shared-discriminator collapse path (translate.go's
// own buildUnion) automatically -- the identical real, documented
// lossiness (union of every branch's properties, all Optional, mutual
// exclusivity not enforced) OpenAPI's own oneOf already gets, needing no
// new translation logic in that package at all.
func (c *Converter) fillUnion(shapeID string, shape Shape, s *openapi3.Schema) error {
	for _, name := range sortedMemberNames(shape.Members) {
		member := shape.Members[name]
		memberSchema, err := c.Convert(member.Target)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", shapeID, name, err)
		}
		branch := &openapi3.Schema{
			Type:       &openapi3.Types{"object"},
			Properties: openapi3.Schemas{name: openapi3.NewSchemaRef("", memberSchema)},
			Required:   []string{name},
		}
		s.OneOf = append(s.OneOf, openapi3.NewSchemaRef("", branch))
	}
	return nil
}

func (c *Converter) fillList(shapeID string, shape Shape, s *openapi3.Schema) error {
	if shape.Member == nil {
		return fmt.Errorf("%s: list/set shape has no member", shapeID)
	}
	item, err := c.Convert(shape.Member.Target)
	if err != nil {
		return fmt.Errorf("%s: %w", shapeID, err)
	}
	s.Type = &openapi3.Types{"array"}
	s.Items = openapi3.NewSchemaRef("", item)
	return nil
}

// fillMap translates a Smithy map shape into an additionalProperties-
// shaped object, exactly the same real, non-lossy fit
// internal/schema.Translator's own buildMap already gives an OpenAPI
// additionalProperties object. The key member's own target is not
// translated at all -- tfplugin's own Map type requires string keys
// unconditionally, and every real Smithy map this session found targets
// a string (or string-like enum) key; a genuinely non-string-keyed real
// map would be a real gap, noted here rather than silently assumed away.
func (c *Converter) fillMap(shapeID string, shape Shape, s *openapi3.Schema) error {
	if shape.Value == nil {
		return fmt.Errorf("%s: map shape has no value member", shapeID)
	}
	if shape.Key != nil {
		if keyShape, ok := c.doc.Shapes[shape.Key.Target]; ok && keyShape.Type != "string" && keyShape.Type != "enum" {
			c.note(shapeID, "map key targets a non-string shape (%s, kind %q) -- tfplugin's own Map type requires string keys; the key's own real type is discarded, only the value is preserved", shape.Key.Target, keyShape.Type)
		}
	}
	val, err := c.Convert(shape.Value.Target)
	if err != nil {
		return fmt.Errorf("%s: %w", shapeID, err)
	}
	s.Type = &openapi3.Types{"object"}
	s.AdditionalProperties = openapi3.AdditionalProperties{Schema: openapi3.NewSchemaRef("", val)}
	return nil
}

// fillEnum translates Smithy 2.0's own dedicated enum/intEnum shapes
// (distinct from a plain string+enum-trait, which real Smithy 1.0 models
// used and Smithy 2.0 models no longer emit -- confirmed against five
// real, current models this session, all Smithy 2.0, all using the
// dedicated shape) into a plain string/integer schema carrying the real
// enum values from each member's own smithy.api#enumValue trait.
func (c *Converter) fillEnum(shapeID string, shape Shape, s *openapi3.Schema, isInt bool) error {
	if isInt {
		s.Type = &openapi3.Types{"integer"}
	} else {
		s.Type = &openapi3.Types{"string"}
	}
	for _, name := range sortedMemberNames(shape.Members) {
		member := shape.Members[name]
		var val any = name
		if raw, ok := member.Traits["smithy.api#enumValue"]; ok {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return fmt.Errorf("%s: member %s: decode smithy.api#enumValue: %w", shapeID, name, err)
			}
			val = decoded
		}
		s.Enum = append(s.Enum, val)
	}
	return nil
}

func stringTrait(shape Shape, name string) (string, bool, error) {
	raw, ok := shape.Traits[name]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, err
	}
	return s, true, nil
}

func sortedMemberNames(members map[string]Member) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
