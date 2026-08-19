// Real, structural gap this file exists to close, found live during the
// onboarding pipeline's own description-generator checkpoint (ubiquex's
// own STATE.md): tfprotov6.SchemaAttribute -- the real Terraform plugin
// protocol v6 wire type every translated attribute above (translate.go)
// eventually becomes -- carries NO constraint or enum-value fields at
// all, confirmed by direct inspection of that library's own real source.
// So a real OpenAPI schema's own enum/min/max/pattern signal, genuinely
// useful context for writing a field's own description, is lost before
// it ever crosses the tfplugin6 RPC boundary into ubiquex.
//
// This file collects that signal SEPARATELY, via its own smaller,
// independent walk over the same *openapi3.Schema tree translate.go
// itself walks -- deliberately NOT intertwined with Translator's own
// mature, heavily-tested attribute-building recursion (BuildAttribute/
// buildProperties/buildObject/buildArray/buildMap/buildUnion/buildAllOf).
// This walk is best-effort and does not need to perfectly mirror every
// one of that recursion's own lossy edge cases (oneOf/anyOf branch
// merging, cycle/depth guards) -- a signal this walk misses just means
// the description generator (live or checked-in-artifact) has one less
// piece of context for that one field and may abstain more readily,
// never a wrong or misleading signal, so an imperfect correspondence
// degrades gracefully rather than needing to be airtight.
package schema

import "github.com/getkin/kin-openapi/openapi3"

// FieldSignal carries the real constraint/enum data described above for
// one field, plus (recursively) the same for any of its own nested
// object properties -- Nested is keyed by ToSnakeCase(name), the
// identical convention tfprotov6.SchemaAttribute.Name (and therefore
// ir.Field.WireName on ubiquex's own side) already uses, so a caller can
// walk an ir.Field tree and this tree in lockstep, one key lookup per
// level, with no separate path-reconciliation step of its own.
type FieldSignal struct {
	Enum      []string                `json:"enum,omitempty"`
	Minimum   *float64                `json:"minimum,omitempty"`
	Maximum   *float64                `json:"maximum,omitempty"`
	MinLength *uint64                 `json:"min_length,omitempty"`
	MaxLength *uint64                 `json:"max_length,omitempty"`
	Pattern   string                  `json:"pattern,omitempty"`
	Nested    map[string]*FieldSignal `json:"nested,omitempty"`
}

func (f *FieldSignal) isEmpty() bool {
	return f == nil || (len(f.Enum) == 0 && f.Minimum == nil && f.Maximum == nil &&
		f.MinLength == nil && f.MaxLength == nil && f.Pattern == "" && len(f.Nested) == 0)
}

// signalMaxDepth mirrors translate.go's own defaultMaxDepth guard --
// independent, deliberately simple cycle/depth protection for this
// smaller, separate walk (object identity, the same reliable, cheap
// test translate.go's own enterObject already uses).
const signalMaxDepth = 60

// CollectSignals is this file's real entry point -- internal/dynserver's
// own Build calls this once per create/read schema, merging both real
// results (MergeFieldSignal) into one combined tree per resource type,
// the same way Translator's own shared instance already merges create +
// read into one real, combined attribute set.
func CollectSignals(s *openapi3.Schema) map[string]*FieldSignal {
	return collectSignals(s, nil)
}

func collectSignals(s *openapi3.Schema, active []*openapi3.Schema) map[string]*FieldSignal {
	if s == nil {
		return nil
	}
	for _, a := range active {
		if a == s {
			return nil // real cycle -- stop, matches translate.go's own enterObject test
		}
	}
	if len(active) >= signalMaxDepth {
		return nil
	}
	active = append(active, s)

	props := map[string]*openapi3.SchemaRef{}
	for name, ref := range s.Properties {
		props[name] = ref
	}
	for _, branch := range s.AllOf {
		if bs := deref(branch); bs != nil {
			for name, ref := range bs.Properties {
				props[name] = ref
			}
		}
	}
	// oneOf/anyOf: only merge branches that are ALL objects (mirrors
	// translate.go's own buildUnion "allObjects" case exactly) -- a
	// mixed or all-scalar union has no per-field signal to attach at
	// this level; its own leaf signal (enum/pattern on the union itself)
	// is still captured by leafSignal in the caller, unaffected by this.
	for _, branches := range [][]*openapi3.SchemaRef{s.OneOf, s.AnyOf} {
		if len(branches) == 0 {
			continue
		}
		allObj := true
		for _, b := range branches {
			bs := deref(b)
			if bs == nil || !isObjectType(bs) {
				allObj = false
				break
			}
		}
		if !allObj {
			continue
		}
		for _, b := range branches {
			bs := deref(b)
			for name, ref := range bs.Properties {
				if _, exists := props[name]; !exists {
					props[name] = ref
				}
			}
		}
	}

	if len(props) == 0 {
		return nil
	}

	out := map[string]*FieldSignal{}
	for name, ref := range props {
		prop := deref(ref)
		if prop == nil {
			continue
		}
		sig := leafSignal(prop)
		if nested := nestedSignalFor(prop, active); nested != nil {
			sig.Nested = nested
		}
		if !sig.isEmpty() {
			out[ToSnakeCase(name)] = sig
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nestedSignalFor returns prop's own nested per-field signal map, when
// it has one -- a direct object's own properties, or an array's own
// object-shaped items' properties (mirrors translate.go's own
// buildObject/buildArray object-items handling: both a plain nested
// object field and a list-of-objects field carry per-field constraints
// the same way, and cli/sdkdescribe.go's own walkNested, on ubiquex's
// side, already treats KindObject and KindList/Set/Map-of-Object
// identically for this exact reason).
func nestedSignalFor(prop *openapi3.Schema, active []*openapi3.Schema) map[string]*FieldSignal {
	if prop.Type.Is("array") {
		if prop.Items == nil {
			return nil
		}
		items := deref(prop.Items)
		if items == nil {
			return nil
		}
		return collectSignals(items, active)
	}
	if isObjectType(prop) && len(prop.Properties) > 0 {
		return collectSignals(prop, active)
	}
	return nil
}

// leafSignal extracts s's own real, direct enum/constraint fields --
// never s's nested properties (nestedSignalFor's own job, called
// separately by the caller).
func leafSignal(s *openapi3.Schema) *FieldSignal {
	sig := &FieldSignal{
		Minimum:   s.Min,
		Maximum:   s.Max,
		MaxLength: s.MaxLength,
		Pattern:   s.Pattern,
	}
	if s.MinLength > 0 {
		v := s.MinLength
		sig.MinLength = &v
	}
	for _, e := range s.Enum {
		// Real OpenAPI enum values are always JSON scalars; only string
		// values are meaningful as prompt-ready signal text -- a numeric
		// or boolean enum member would need its own real rendering this
		// package deliberately doesn't invent here (dropped, not
		// guessed at; the field's own Minimum/Maximum still carries a
		// numeric range's real signal regardless).
		if str, ok := e.(string); ok {
			sig.Enum = append(sig.Enum, str)
		}
	}
	return sig
}

// MergeFieldSignal combines two real signals for what internal/dynserver's
// own Build has already established is the SAME logical field (a
// create-request schema and a read-response schema both describing one
// real resource attribute) -- union of Enum values (deduplicated,
// order-stable), the narrower/wider of Minimum/Maximum, whichever
// Pattern/length bound is actually set, and a recursive merge of Nested.
// Never called with a nil receiver or arg that's already nil -- callers
// (mergeSignalMaps) only invoke this when both sides are non-nil.
func MergeFieldSignal(a, b *FieldSignal) *FieldSignal {
	out := &FieldSignal{
		Minimum:   firstNonNil(a.Minimum, b.Minimum),
		Maximum:   firstNonNil(a.Maximum, b.Maximum),
		MinLength: firstNonNilUint(a.MinLength, b.MinLength),
		MaxLength: firstNonNilUint(a.MaxLength, b.MaxLength),
		Pattern:   firstNonEmpty(a.Pattern, b.Pattern),
	}
	out.Enum = mergeEnum(a.Enum, b.Enum)
	out.Nested = mergeSignalMaps(a.Nested, b.Nested)
	return out
}

func mergeEnum(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range a {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range b {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// mergeSignalMaps is CollectSignals' own real create+read merge point,
// used both by internal/dynserver's own Build (top-level) and by
// MergeFieldSignal (recursively, for Nested).
func mergeSignalMaps(dst, src map[string]*FieldSignal) map[string]*FieldSignal {
	if len(dst) == 0 {
		return src
	}
	if len(src) == 0 {
		return dst
	}
	out := make(map[string]*FieldSignal, len(dst))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if existing, ok := out[k]; ok {
			out[k] = MergeFieldSignal(existing, v)
		} else {
			out[k] = v
		}
	}
	return out
}

// MergeSignalMaps is mergeSignalMaps' own exported form -- internal/dynserver's
// own Build calls this directly (a different package) to combine one
// resource's create-schema and read-schema signal maps into the single
// combined map ResourceType.Signals carries.
func MergeSignalMaps(dst, src map[string]*FieldSignal) map[string]*FieldSignal {
	return mergeSignalMaps(dst, src)
}

func firstNonNil(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

func firstNonNilUint(a, b *uint64) *uint64 {
	if a != nil {
		return a
	}
	return b
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
