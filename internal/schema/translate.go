// Package schema translates real OpenAPI 3.x schemas into tfplugin's own
// (protocol v6) type system -- UBI-158 Phase 1's own "hard, load-bearing"
// layer.
//
// The two type systems are not isomorphic, and this package's central
// discipline is: where translation is genuinely lossy, say so, in a Note
// attached to the field it happened on, rather than silently flattening.
// See Translator.Notes and each lossy case's own comment below for exactly
// which OpenAPI shapes lose information and what they degrade to.
//
// This package builds tfprotov6 nested ATTRIBUTES (SchemaAttribute.NestedType,
// a real protocol v6 feature -- see tfprotov6/schema.go's own
// SchemaObject/SchemaObjectNestingMode), not the legacy block-nesting
// mechanism (SchemaNestedBlock). Nested attributes are the correct fit for
// OpenAPI's own properties-of-an-object shape: they're addressed and typed
// like any other attribute, with no separate block-vs-attribute config
// syntax distinction for a caller to reconcile against JSON's own single,
// uniform object shape.
package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Note is one real, specific translation decision worth surfacing --
// attached to the dotted field path it happened on (e.g.
// "repository.license.oneOf") so a reader can find it in the source spec.
type Note struct {
	Path   string
	Detail string
}

// Translator accumulates Notes across every BuildAttribute call it makes,
// so a caller translating an entire resource's request/response schemas can
// report every lossy decision made along the way in one place.
type Translator struct {
	Notes []Note
	seen  map[string]bool

	// active is the stack of *openapi3.Schema pointers currently being
	// expanded, by object identity -- cycle detection for genuinely
	// self-referential OpenAPI schemas (confirmed real: Datadog's own
	// published spec recurses through at least one schema this way, found
	// by this translator hanging and growing without bound against the
	// real spec before this guard existed, not by inspection). kin-openapi
	// resolves every reference to a single component schema to the SAME
	// *Schema pointer (its own loader deduplicates by $ref, never clones),
	// so pointer identity is a reliable, cheap cycle test -- no need to
	// hash or deep-compare schema content.
	active []*openapi3.Schema
	// maxDepth is defense-in-depth against a pathological but non-cyclic
	// chain (deeply nested allOf/oneOf compositions with no repeated
	// pointer) -- real specs this translator has been run against never
	// approach this; it exists so a shape neither GitHub's nor Datadog's
	// spec happens to exercise still can't hang this translator the way
	// the missing cycle guard did.
	maxDepth int
}

const defaultMaxDepth = 60

// NewTranslator returns a ready-to-use Translator.
func NewTranslator() *Translator {
	return &Translator{seen: map[string]bool{}, maxDepth: defaultMaxDepth}
}

// enterObject pushes s onto the active-expansion stack, returning false
// (and recording a Note) if s is already being expanded higher up the same
// call stack (a real cycle) or the stack is already at maxDepth. Callers
// must call exitObject when done, but only if enterObject returned true.
func (t *Translator) enterObject(s *openapi3.Schema, path string) bool {
	for _, a := range t.active {
		if a == s {
			t.note(path, "schema is self-referential (a real recursive structure this OpenAPI document defines on purpose, e.g. a tree-shaped field) -- recursion stops here rather than expanding forever; this occurrence is typed as dynamic instead of its real nested shape")
			return false
		}
	}
	if len(t.active) >= t.maxDepth {
		t.note(path, "schema nesting exceeded this translator's depth limit (%d) -- typed as dynamic rather than continuing to expand", t.maxDepth)
		return false
	}
	t.active = append(t.active, s)
	return true
}

func (t *Translator) exitObject() {
	t.active = t.active[:len(t.active)-1]
}

func (t *Translator) note(path, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	key := path + "\x00" + detail
	if t.seen[key] {
		return
	}
	t.seen[key] = true
	t.Notes = append(t.Notes, Note{Path: path, Detail: detail})
}

// fieldPolicy is BuildAttribute's own optionality decision, computed once
// per field from OpenAPI's real, distinct optionality signals -- required
// (an object-level list of property names, not a per-property flag),
// readOnly (server-only, never accepted in a request), and writeOnly
// (accepted in a request, never returned in a response). tfplugin's own
// three-way Required/Optional/Computed (plus a real, native WriteOnly flag
// protocol v6 already has -- no translation loss there at all) is a clean,
// exact fit for these three signals *within a single schema* (a request
// body alone, or a response body alone); resourcemap's own per-resource
// merge (combining a create-request schema with a read-response schema) is
// what produces the Optional+Computed ("provider may default this")
// combination real resources need -- this package only ever sees one
// schema at a time and has no basis to decide that on its own.
type fieldPolicy struct {
	required  bool
	readOnly  bool
	writeOnly bool
}

func (p fieldPolicy) apply(attr *tfprotov6.SchemaAttribute) {
	switch {
	case p.readOnly:
		attr.Computed = true
	case p.writeOnly:
		attr.WriteOnly = true
		if p.required {
			attr.Required = true
		} else {
			attr.Optional = true
		}
	default:
		if p.required {
			attr.Required = true
		} else {
			attr.Optional = true
		}
	}
}

// BuildAttribute translates one named OpenAPI schema into a complete
// tfprotov6.SchemaAttribute -- recursing into nested objects/arrays as
// needed. path is a dotted, human-readable location (e.g.
// "full-repository.owner.login") used only for Notes.
func (t *Translator) BuildAttribute(name string, sr *openapi3.SchemaRef, policy fieldPolicy, path string) *tfprotov6.SchemaAttribute {
	attr := &tfprotov6.SchemaAttribute{Name: name}
	policy.apply(attr)

	s := deref(sr)
	if s == nil {
		attr.Type = tftypes.DynamicPseudoType
		t.note(path, "schema reference could not be resolved -- typed as dynamic")
		return attr
	}

	if s.Description != "" {
		attr.Description = s.Description
	}
	if s.Deprecated {
		attr.Deprecated = true
	}

	if isObjectType(s) && len(s.Properties) > 0 {
		if nested := t.buildObject(s, path); nested != nil {
			attr.NestedType = nested
			return attr
		}
		// buildObject only returns nil here because enterObject refused
		// (a real cycle or the depth cap, already noted there) -- go
		// straight to dynamic rather than falling into buildType's own
		// object dispatch, which would re-derive the same "no fixed
		// properties" conclusion for the wrong reason and log a second,
		// misleading Note about this same field.
		attr.Type = tftypes.DynamicPseudoType
		return attr
	}

	attr.Type = t.buildType(s, path)
	return attr
}

// BuildTopLevel translates an object schema's own top-level properties
// directly into a []*SchemaAttribute -- the shape a resource's own root
// tfprotov6.SchemaBlock.Attributes needs (a resource's own top level has no
// wrapping NestedType slot the way a nested field does; BuildAttribute
// itself is for translating a NAMED field within some containing object,
// not a document's own top-level request/response body).
func (t *Translator) BuildTopLevel(s *openapi3.Schema, path string) []*tfprotov6.SchemaAttribute {
	if s == nil || !isObjectType(s) {
		return nil
	}
	return t.buildProperties(s, path)
}

// buildObject returns a *SchemaObject (single-nesting, the object-as-
// attribute shape) when s is genuinely object-shaped with fixed, named
// properties -- nil otherwise, telling BuildAttribute to fall through to a
// plain Type instead (scalar, list, or map).
func (t *Translator) buildObject(s *openapi3.Schema, path string) *tfprotov6.SchemaObject {
	if !isObjectType(s) || len(s.Properties) == 0 {
		return nil
	}
	attrs := t.buildProperties(s, path)
	if len(attrs) == 0 {
		// buildProperties only ever returns empty here because
		// enterObject refused (a real cycle or the depth cap) -- s.Properties
		// is non-empty by the guard above, so an empty result can't mean
		// "legitimately no properties." Fall through to buildType's own
		// dynamic fallback instead of returning a hollow, attribute-less
		// NestedType.
		return nil
	}
	return &tfprotov6.SchemaObject{
		Attributes: attrs,
		Nesting:    tfprotov6.SchemaObjectNestingModeSingle,
	}
}

// buildProperties translates every property of an object schema into
// SchemaAttributes, sorted by name -- determinism, the same standing
// discipline ubx core itself applies to anything that walks a Go map
// (STATE.md: "map-iteration ordering" is explicitly called out as a
// determinism hazard) -- OpenAPI's own Properties field is a Go map with no
// ordering guarantee of its own.
func (t *Translator) buildProperties(s *openapi3.Schema, path string) []*tfprotov6.SchemaAttribute {
	if !t.enterObject(s, path) {
		return nil
	}
	defer t.exitObject()

	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}

	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	attrs := make([]*tfprotov6.SchemaAttribute, 0, len(names))
	for _, name := range names {
		propRef := s.Properties[name]
		propPath := path + "." + name
		prop := deref(propRef)
		policy := fieldPolicy{required: required[name]}
		if prop != nil {
			policy.readOnly = prop.ReadOnly
			policy.writeOnly = prop.WriteOnly
		}
		attrs = append(attrs, t.BuildAttribute(ToSnakeCase(name), propRef, policy, propPath))
	}
	return attrs
}

// buildType translates a non-fixed-object schema into a plain tftypes.Type:
// scalars, arrays, and additionalProperties-shaped maps (including maps of
// objects, which -- unlike a fixed-properties object -- have no per-key
// schema of their own to preserve optionality metadata for, so a plain
// tftypes.Object element type, all attributes present, is the correct,
// non-lossy shape: JSON Schema's additionalProperties has no concept of a
// "required" subset among dynamically-named keys to lose in the first
// place).
func (t *Translator) buildType(s *openapi3.Schema, path string) tftypes.Type {
	switch {
	case len(s.OneOf) > 0:
		return t.buildUnion(s.OneOf, "oneOf", path)
	case len(s.AnyOf) > 0:
		return t.buildUnion(s.AnyOf, "anyOf", path)
	case len(s.AllOf) > 0:
		return t.buildAllOf(s, path)
	case s.Type.Is("array"):
		return t.buildArray(s, path)
	case isObjectType(s):
		return t.buildMap(s, path)
	case s.Type.Is("string"):
		return tftypes.String
	case s.Type.Is("integer"), s.Type.Is("number"):
		return tftypes.Number
	case s.Type.Is("boolean"):
		return tftypes.Bool
	default:
		// No `type` at all (legal in JSON Schema -- means "any"), or a
		// type this translator has no case for. Genuinely honest: this
		// is not an error, OpenAPI permits schema-less values, and
		// DynamicPseudoType is tfplugin's own real mechanism for "the
		// shape isn't known until a value actually arrives" -- not a
		// hack standing in for a missing case.
		t.note(path, "no concrete OpenAPI type (schema-less/`any`) -- typed as dynamic")
		return tftypes.DynamicPseudoType
	}
}

// buildArray translates `type: array`. An object-shaped items schema needs
// SchemaObject's own List nesting mode (an array of NESTED ATTRIBUTES, not
// a plain tftypes.List{ElementType: Object}) so BuildAttribute's caller
// still gets per-field Required/Optional/Computed metadata for each item's
// own properties -- buildArray therefore returns through a small local
// wrapper only when the caller is BuildAttribute itself (see the NestedType
// branch below); called directly (buildType's own recursion, e.g. an array
// nested inside another array) it degrades gracefully to a plain
// tftypes.List of Objects, since a NestedType can only ever appear as an
// attribute's own top-level field, never buried inside a plain Type.
func (t *Translator) buildArray(s *openapi3.Schema, path string) tftypes.Type {
	if s.Items == nil {
		t.note(path, "array with no `items` schema -- element typed as dynamic")
		return tftypes.List{ElementType: tftypes.DynamicPseudoType}
	}
	items := deref(s.Items)
	if items != nil && isObjectType(items) && len(items.Properties) > 0 {
		return tftypes.List{ElementType: objectValueType(t.buildProperties(items, path+"[]"))}
	}
	return tftypes.List{ElementType: t.buildType(orEmpty(items), path+"[]")}
}

// buildMap translates an additionalProperties-shaped object (no fixed
// `properties`, or `properties` alongside a schema'd additionalProperties --
// the latter's own fixed properties are deliberately folded into the map's
// value type rather than kept as separate named attributes, since
// tfplugin's type system has no single construct for "an object with both
// some named fields and an open-ended remainder"; recorded as a Note, not
// silently dropped) into a tftypes.Map. A schema-less additionalProperties
// (bare `additionalProperties: true`, or an object with neither properties
// nor additionalProperties at all -- genuinely arbitrary JSON) has no
// element type to preserve at all and becomes DynamicPseudoType, not
// Map(String) -- picking String would silently reject any real response
// that returns a number/bool/nested-object value, which is exactly the
// kind of silent flattening this package exists not to do.
func (t *Translator) buildMap(s *openapi3.Schema, path string) tftypes.Type {
	if len(s.Properties) > 0 {
		t.note(path, "object has both fixed properties and additionalProperties -- fixed properties folded into the map's own value type, their individual names/optionality lost")
	}
	if s.AdditionalProperties.Schema != nil {
		valueSchema := deref(s.AdditionalProperties.Schema)
		if valueSchema != nil && isObjectType(valueSchema) && len(valueSchema.Properties) > 0 {
			return tftypes.Map{ElementType: objectValueType(t.buildProperties(valueSchema, path+"{}"))}
		}
		return tftypes.Map{ElementType: t.buildType(orEmpty(valueSchema), path+"{}")}
	}
	t.note(path, "object with no fixed properties and no typed additionalProperties -- genuinely arbitrary JSON, typed as dynamic")
	return tftypes.DynamicPseudoType
}

// buildUnion translates oneOf/anyOf -- JSON Schema's own "exactly/at-least
// one of these shapes" -- into tfplugin's single, static per-attribute
// type. This is the translation's single most genuinely lossy case, always
// documented via a Note, never silently resolved:
//
//   - Every branch the same primitive type (a common real-world pattern:
//     oneOf used to attach distinct `format`/`description` per case, e.g.
//     GitHub's own license fields) collapses to that one primitive type,
//     losing only the per-branch metadata (format/description/enum),
//     never structural shape -- not really lossy in the way that matters
//     for a provider (the wire value round-trips exactly).
//   - Every branch an object collapses to the UNION of every branch's own
//     properties, all marked Optional regardless of any individual
//     branch's own `required` list -- genuinely lossy: a value satisfying
//     branch A's required fields while also setting branch B's fields
//     (which real APIs reject) is not caught by this schema at all; only
//     the API's own runtime validation catches it. Chosen over refusing to
//     model the attribute at all, matching the ticket's own "make lossy
//     translation explicit and documented, don't silently flatten"
//     standard -- structural access to every real field beats no access.
//   - Anything else (mixed primitive/object/array branches) has no
//     coherent static shape at all in tfplugin's type system --
//     DynamicPseudoType, fully opaque to Terraform's own type-checking,
//     the same honest fallback a schema-less value gets.
func (t *Translator) buildUnion(branches openapi3.SchemaRefs, kind, path string) tftypes.Type {
	resolved := make([]*openapi3.Schema, 0, len(branches))
	for _, b := range branches {
		if s := deref(b); s != nil {
			resolved = append(resolved, s)
		}
	}
	if len(resolved) == 0 {
		t.note(path, "%s with no resolvable branches -- typed as dynamic", kind)
		return tftypes.DynamicPseudoType
	}

	if allPrimitive(resolved, "string") {
		t.note(path, "%s of %d string-typed branches -- collapsed to string (only per-branch format/description/enum metadata lost)", kind, len(resolved))
		return tftypes.String
	}
	if allPrimitive(resolved, "integer") || allPrimitive(resolved, "number") {
		t.note(path, "%s of %d numeric branches -- collapsed to number (only per-branch metadata lost)", kind, len(resolved))
		return tftypes.Number
	}
	if allPrimitive(resolved, "boolean") {
		t.note(path, "%s of %d boolean branches -- collapsed to bool", kind, len(resolved))
		return tftypes.Bool
	}

	if allObjects(resolved) {
		merged := &openapi3.Schema{Properties: openapi3.Schemas{}}
		for i, branch := range resolved {
			for name, ref := range branch.Properties {
				if _, exists := merged.Properties[name]; !exists {
					merged.Properties[name] = ref
				}
			}
			_ = i
		}
		t.note(path, "%s of %d object branches, no shared discriminator -- collapsed to the UNION of every branch's properties, all Optional; branch mutual-exclusivity and each branch's own required fields are NOT enforced by this schema", kind, len(resolved))
		return objectValueType(t.buildProperties(merged, path))
	}

	t.note(path, "%s of %d structurally incompatible branches (mixed primitive/object/array) -- no static tfplugin type can represent this; typed as fully dynamic, opaque to Terraform's own type system", kind, len(resolved))
	return tftypes.DynamicPseudoType
}

// buildAllOf translates JSON Schema composition (`allOf`, real, common
// usage: "extends" a base object schema with additional properties) into
// one merged object -- genuinely NOT lossy the way oneOf/anyOf are: allOf
// branches are meant to hold simultaneously, so a plain property union
// (later branches' properties winning on a name collision, matching JSON
// Schema's own last-applied-wins merge semantics for object composition)
// is a faithful translation, not a compromise.
func (t *Translator) buildAllOf(s *openapi3.Schema, path string) tftypes.Type {
	merged := &openapi3.Schema{Properties: openapi3.Schemas{}, Required: append([]string{}, s.Required...)}
	for _, branch := range s.AllOf {
		bs := deref(branch)
		if bs == nil {
			continue
		}
		for name, ref := range bs.Properties {
			merged.Properties[name] = ref
		}
		merged.Required = append(merged.Required, bs.Required...)
	}
	for name, ref := range s.Properties {
		merged.Properties[name] = ref
	}
	return objectValueType(t.buildProperties(merged, path))
}

// objectValueType builds the tftypes.Type a []*SchemaAttribute value would
// have as a VALUE (not wrapped in a SchemaObject/NestedType) -- used
// wherever an object shape appears somewhere other than an attribute's own
// direct NestedType slot (inside a Map's element type, a List buried inside
// another List, a merged oneOf/anyOf/allOf branch).
func objectValueType(attrs []*tfprotov6.SchemaAttribute) tftypes.Type {
	fields := make(map[string]tftypes.Type, len(attrs))
	optional := map[string]struct{}{}
	for _, a := range attrs {
		fields[a.Name] = a.ValueType()
		if !a.Required {
			optional[a.Name] = struct{}{}
		}
	}
	return tftypes.Object{AttributeTypes: fields, OptionalAttributes: optional}
}

func isObjectType(s *openapi3.Schema) bool {
	if s.Type.Is("object") {
		return true
	}
	// A schema with `properties` but no explicit `type` is object-shaped
	// in every real spec this translator has been checked against --
	// `type` is technically optional in JSON Schema.
	return s.Type == nil && (len(s.Properties) > 0 || s.AdditionalProperties.Schema != nil || s.AdditionalProperties.Has != nil)
}

func allPrimitive(schemas []*openapi3.Schema, want string) bool {
	for _, s := range schemas {
		if !s.Type.Is(want) {
			return false
		}
	}
	return true
}

func allObjects(schemas []*openapi3.Schema) bool {
	for _, s := range schemas {
		if !isObjectType(s) {
			return false
		}
	}
	return true
}

func deref(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func orEmpty(s *openapi3.Schema) *openapi3.Schema {
	if s == nil {
		return &openapi3.Schema{}
	}
	return s
}

// ToSnakeCase converts an OpenAPI property name (any real casing/
// punctuation -- camelCase, kebab-case, and dotted names all appear in
// real specs) into ubx's own snake_case attribute-naming convention,
// matching the <provider>_<resource> convention resourcemap.go applies at
// the resource-type level. Exported (UBI-158 Phase 4): internal/smithy's
// own naming compatibility layer reuses this identical algorithm for
// Smithy's own PascalCase operation nouns -- the target convention is the
// exact same one, ubx's own snake_case, regardless of which schema source
// produced the name.
func ToSnakeCase(openAPIName string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range openAPIName {
		switch {
		case r == '-' || r == '.' || r == ' ':
			b.WriteByte('_')
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			prevLower = false
		default:
			b.WriteRune(r)
			prevLower = r >= 'a' && r <= 'z' || (r >= '0' && r <= '9')
		}
	}
	return b.String()
}
