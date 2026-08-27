package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Bundle rewrites every EXTERNAL $ref reachable from doc's real entry
// points (every path's own operations, plus every existing components
// entry) into a real, network-free LOCAL one -- reference bundling, not
// value inlining. UBI-193 Part 1's own real design question, reported
// and confirmed before this was built: Azure's own real specs (the only
// provider with this problem today) split themselves across shared
// files by real, relative path ("../../../../../../common-types/
// resource-management/v5/types.json#/definitions/ProxyResource" --
// confirmed live, sampled across 16 real, diverse Azure specs, 100%
// relative, 0% absolute URLs) -- Load already resolves every one of
// these into a real, shared *openapi3.SchemaRef (or ParameterRef/
// RequestBodyRef/ResponseRef/HeaderRef) .Value while real network
// access is available; the ONLY real gap is that kin-openapi's own
// MarshalJSON re-emits the bare {"$ref": "..."} string once .Ref is
// set, discarding the resolved .Value, which a later network-free
// reparse can't follow.
//
// Deliberately NOT "clear Ref, deep-copy Value inline at every
// reference site": Azure's own real ref graph is genuinely CYCLIC, not
// just deep -- confirmed live, not hypothetical: network/
// virtualNetwork.json's own real PublicIPAddress reaches itself in 4
// hops through its own real linkedPublicIPAddress/servicePublicIPAddress
// properties, crossing loadBalancer.json and networkGateway.json along
// the way. Deep-copying .Value at every shared site would recurse
// forever on a real cycle like that one. Bundle instead does what real
// OpenAPI bundling tools (redocly/swagger-cli bundle) do: give each
// DISTINCT external target exactly ONE new entry under doc.Components,
// then point every reference to it -- the one that already existed
// externally, and every other real occurrence of the identical target
// -- at that ONE local entry via a plain "#/components/.../<name>"
// pointer, by mutating the SAME shared *SchemaRef (etc.) object kin-
// openapi already resolved every reference site to. A $ref pointer,
// local or (formerly) external, never needs eager expansion -- this is
// exactly how doc's own pre-existing internal "#/..." refs already
// round-trip cleanly today, cycles included, with zero special-casing.
// .Value is left untouched on every mutated ref (harmless: MarshalJSON
// always prefers .Ref over .Value once .Ref is set; internal/schema
// .Translator, which runs on this same in-memory doc immediately after
// Bundle in GenerateOpenAPIMember, reads .Value directly and has never
// cared about .Ref at all).
//
// x-ms-examples (Azure's own real Swagger vendor extension for
// per-operation example payloads) is deliberately never visited here,
// and needs no explicit skip logic to make that true: it's real,
// sizeable (914 of 3,909 external refs sampled live across this
// package's own 16-spec sample, 23.4%), but it lives entirely inside
// each Operation's own Extensions map, which this walk never descends
// into -- it only follows kin-openapi's own TYPED ref fields (Schema/
// Parameter/RequestBody/Response/Header), the same real fields
// internal/schema.Translator's own BuildTopLevel reads schema content
// from. Confirmed live before writing this: kin-openapi's own Load
// leaves x-ms-examples' own $ref completely raw and unresolved even
// after a real, successful fetch (it isn't a structural OpenAPI ref
// field at all, just an arbitrary vendor-extension value) -- there is
// no path from this walk's own real entry points to one, not "found
// but ignored."
//
// Scope, named rather than silently assumed: this walk follows Schema
// (Properties/Items/AdditionalProperties/AllOf/OneOf/AnyOf/Not),
// Parameter and Header (Schema, Content), RequestBody (Content), and
// Response (Content, Headers) -- every real field this sample's own
// 3,909 non-example external refs were found under. It does NOT follow
// Callbacks, Links, SecuritySchemes, or a whole-PathItem-level external
// $ref (PathItem.Ref) -- none of the real, live-sampled Azure specs
// this package has checked use any of these for a real, non-example
// external ref; if a future real spec does, Bundle will leave that
// specific ref unresolved and generation will still fail loud
// (ErrExternalRefsUnsupported), never silently produce an incomplete
// snapshot.
//
// A no-op, confirmed live, for every provider without external refs
// (kubernetes, datadog, github's own 3 example-only refs) -- safe to
// call unconditionally from every real openapi-sourced generation, not
// just Azure's.
func Bundle(doc *openapi3.T) {
	b := &bundler{
		doc:                doc,
		visitedSchema:      map[*openapi3.SchemaRef]bool{},
		visitedParameter:   map[*openapi3.ParameterRef]bool{},
		visitedRequestBody: map[*openapi3.RequestBodyRef]bool{},
		visitedResponse:    map[*openapi3.ResponseRef]bool{},
		visitedHeader:      map[*openapi3.HeaderRef]bool{},

		externalSchemas:       map[*openapi3.SchemaRef]string{},
		externalParameters:    map[*openapi3.ParameterRef]string{},
		externalRequestBodies: map[*openapi3.RequestBodyRef]string{},
		externalResponses:     map[*openapi3.ResponseRef]string{},
		externalHeaders:       map[*openapi3.HeaderRef]string{},
	}
	b.collect()
	b.rewrite()
}

// bundler carries Bundle's own real, two-phase state: collect walks
// doc's real graph once, recording every distinct external ref
// (deduplicated by pointer identity, matching kin-openapi's own real
// de-dup of repeated references to the identical external target,
// confirmed live) alongside its own real $ref string; rewrite assigns
// each a deterministic local name and mutates it in place.
type bundler struct {
	doc *openapi3.T

	visitedSchema      map[*openapi3.SchemaRef]bool
	visitedParameter   map[*openapi3.ParameterRef]bool
	visitedRequestBody map[*openapi3.RequestBodyRef]bool
	visitedResponse    map[*openapi3.ResponseRef]bool
	visitedHeader      map[*openapi3.HeaderRef]bool

	externalSchemas       map[*openapi3.SchemaRef]string
	externalParameters    map[*openapi3.ParameterRef]string
	externalRequestBodies map[*openapi3.RequestBodyRef]string
	externalResponses     map[*openapi3.ResponseRef]string
	externalHeaders       map[*openapi3.HeaderRef]string
}

// isExternalRef reports whether ref is a real external $ref (points
// outside the current document) rather than a real internal one
// ("#/components/..." or, pre-conversion, "#/definitions/...") or no
// ref at all (an inline schema).
func isExternalRef(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "#")
}

// ---------------------------------------------------------------------
// collect: walk doc's real graph once, recording every distinct
// external ref by pointer identity. Each visited* map doubles as this
// walk's own real cycle guard -- Azure's own real ref graph is
// confirmed cyclic (this file's own doc comment), so a plain recursive
// walk with no guard would never terminate.
// ---------------------------------------------------------------------

// foreign propagates across a walk once it crosses a real external ref:
// a nested ref found INSIDE an externally-resolved subtree carries its
// OWN document's own JSON-Pointer namespace, not the main document's --
// confirmed live, not assumed: common-types/resource-management/v3/
// types.json's own real ProxyResource definition has its own real
// "allOf": [{"$ref": "#/definitions/Resource"}], and that "#/definitions/
// Resource" is relative to types.json itself, genuinely meaningless
// once ProxyResource is hoisted into the main document's own
// components (reparsing "#/definitions/Resource" against a v3 document
// with no top-level "definitions" field at all fails loud, confirmed
// live before this propagation existed). A "#/..."-shaped ref
// encountered while foreign is already true still needs bundling, the
// identical way a plainly-external one does -- once any hop has left
// the main document, every ref from there on is foreign until it's
// re-anchored under a new local name.
func (b *bundler) collect() {
	for _, path := range b.doc.Paths.InMatchingOrder() {
		b.walkPathItem(b.doc.Paths.Find(path))
	}
	for _, s := range b.doc.Components.Schemas {
		b.walkSchema(s, false)
	}
	for _, p := range b.doc.Components.Parameters {
		b.walkParameter(p, false)
	}
	for _, rb := range b.doc.Components.RequestBodies {
		b.walkRequestBody(rb, false)
	}
	for _, r := range b.doc.Components.Responses {
		b.walkResponse(r, false)
	}
	for _, h := range b.doc.Components.Headers {
		b.walkHeader(h, false)
	}
}

func (b *bundler) walkPathItem(item *openapi3.PathItem) {
	if item == nil {
		return
	}
	for _, p := range item.Parameters {
		b.walkParameter(p, false)
	}
	for _, op := range []*openapi3.Operation{
		item.Connect, item.Delete, item.Get, item.Head, item.Options,
		item.Patch, item.Post, item.Put, item.Trace, item.Query,
	} {
		b.walkOperation(op)
	}
	for _, op := range item.AdditionalOperations {
		b.walkOperation(op)
	}
}

func (b *bundler) walkOperation(op *openapi3.Operation) {
	if op == nil {
		return
	}
	for _, p := range op.Parameters {
		b.walkParameter(p, false)
	}
	b.walkRequestBody(op.RequestBody, false)
	if op.Responses != nil {
		for _, key := range op.Responses.Keys() {
			b.walkResponse(op.Responses.Value(key), false)
		}
	}
}

func (b *bundler) walkSchema(ref *openapi3.SchemaRef, foreign bool) {
	if ref == nil || b.visitedSchema[ref] {
		return
	}
	b.visitedSchema[ref] = true
	needsBundle := ref.Ref != "" && (foreign || isExternalRef(ref.Ref))
	if needsBundle {
		b.externalSchemas[ref] = ref.Ref
	}
	if ref.Value == nil {
		return
	}
	childForeign := foreign || isExternalRef(ref.Ref)
	v := ref.Value
	for _, name := range sortedKeys(v.Properties) {
		b.walkSchema(v.Properties[name], childForeign)
	}
	b.walkSchema(v.Items, childForeign)
	b.walkSchema(v.AdditionalProperties.Schema, childForeign)
	b.walkSchema(v.Not, childForeign)
	for _, s := range v.AllOf {
		b.walkSchema(s, childForeign)
	}
	for _, s := range v.OneOf {
		b.walkSchema(s, childForeign)
	}
	for _, s := range v.AnyOf {
		b.walkSchema(s, childForeign)
	}
}

func (b *bundler) walkParameter(ref *openapi3.ParameterRef, foreign bool) {
	if ref == nil || b.visitedParameter[ref] {
		return
	}
	b.visitedParameter[ref] = true
	needsBundle := ref.Ref != "" && (foreign || isExternalRef(ref.Ref))
	if needsBundle {
		b.externalParameters[ref] = ref.Ref
	}
	if ref.Value == nil {
		return
	}
	childForeign := foreign || isExternalRef(ref.Ref)
	b.walkSchema(ref.Value.Schema, childForeign)
	b.walkContent(ref.Value.Content, childForeign)
}

func (b *bundler) walkHeader(ref *openapi3.HeaderRef, foreign bool) {
	if ref == nil || b.visitedHeader[ref] {
		return
	}
	b.visitedHeader[ref] = true
	needsBundle := ref.Ref != "" && (foreign || isExternalRef(ref.Ref))
	if needsBundle {
		b.externalHeaders[ref] = ref.Ref
	}
	if ref.Value == nil {
		return
	}
	childForeign := foreign || isExternalRef(ref.Ref)
	b.walkSchema(ref.Value.Schema, childForeign)
	b.walkContent(ref.Value.Content, childForeign)
}

func (b *bundler) walkRequestBody(ref *openapi3.RequestBodyRef, foreign bool) {
	if ref == nil || b.visitedRequestBody[ref] {
		return
	}
	b.visitedRequestBody[ref] = true
	needsBundle := ref.Ref != "" && (foreign || isExternalRef(ref.Ref))
	if needsBundle {
		b.externalRequestBodies[ref] = ref.Ref
	}
	if ref.Value == nil {
		return
	}
	childForeign := foreign || isExternalRef(ref.Ref)
	b.walkContent(ref.Value.Content, childForeign)
}

func (b *bundler) walkResponse(ref *openapi3.ResponseRef, foreign bool) {
	if ref == nil || b.visitedResponse[ref] {
		return
	}
	b.visitedResponse[ref] = true
	needsBundle := ref.Ref != "" && (foreign || isExternalRef(ref.Ref))
	if needsBundle {
		b.externalResponses[ref] = ref.Ref
	}
	if ref.Value == nil {
		return
	}
	childForeign := foreign || isExternalRef(ref.Ref)
	b.walkContent(ref.Value.Content, childForeign)
	for _, name := range sortedKeys(ref.Value.Headers) {
		b.walkHeader(ref.Value.Headers[name], childForeign)
	}
}

func (b *bundler) walkContent(content openapi3.Content, foreign bool) {
	for _, name := range sortedKeys(content) {
		mt := content[name]
		if mt == nil {
			continue
		}
		b.walkSchema(mt.Schema, foreign)
		b.walkSchema(mt.ItemSchema, foreign)
	}
}

// sortedKeys returns m's own keys, sorted -- collect's own walk order
// doesn't affect Bundle's real output (name assignment is sorted
// independently in rewrite), but a stable order here keeps the walk
// itself reproducible for anyone stepping through it, matching this
// project's own determinism discipline.
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------
// rewrite: assign every collected external ref a deterministic local
// name and materialize + mutate in place.
// ---------------------------------------------------------------------

func (b *bundler) rewrite() {
	if b.doc.Components.Schemas == nil {
		b.doc.Components.Schemas = openapi3.Schemas{}
	}
	if b.doc.Components.Parameters == nil {
		b.doc.Components.Parameters = openapi3.ParametersMap{}
	}
	if b.doc.Components.RequestBodies == nil {
		b.doc.Components.RequestBodies = openapi3.RequestBodies{}
	}
	if b.doc.Components.Responses == nil {
		b.doc.Components.Responses = openapi3.ResponseBodies{}
	}
	if b.doc.Components.Headers == nil {
		b.doc.Components.Headers = openapi3.Headers{}
	}

	schemaNames := bundleNames(refStrings(b.externalSchemas), existingNames(b.doc.Components.Schemas))
	for ref, refStr := range b.externalSchemas {
		name := schemaNames[refStr]
		b.doc.Components.Schemas[name] = &openapi3.SchemaRef{Value: ref.Value}
		ref.Ref = "#/components/schemas/" + name
	}

	parameterNames := bundleNames(refStrings(b.externalParameters), existingNames(b.doc.Components.Parameters))
	for ref, refStr := range b.externalParameters {
		name := parameterNames[refStr]
		b.doc.Components.Parameters[name] = &openapi3.ParameterRef{Value: ref.Value}
		ref.Ref = "#/components/parameters/" + name
	}

	requestBodyNames := bundleNames(refStrings(b.externalRequestBodies), existingNames(b.doc.Components.RequestBodies))
	for ref, refStr := range b.externalRequestBodies {
		name := requestBodyNames[refStr]
		b.doc.Components.RequestBodies[name] = &openapi3.RequestBodyRef{Value: ref.Value}
		ref.Ref = "#/components/requestBodies/" + name
	}

	responseNames := bundleNames(refStrings(b.externalResponses), existingNames(b.doc.Components.Responses))
	for ref, refStr := range b.externalResponses {
		name := responseNames[refStr]
		b.doc.Components.Responses[name] = &openapi3.ResponseRef{Value: ref.Value}
		ref.Ref = "#/components/responses/" + name
	}

	headerNames := bundleNames(refStrings(b.externalHeaders), existingNames(b.doc.Components.Headers))
	for ref, refStr := range b.externalHeaders {
		name := headerNames[refStr]
		b.doc.Components.Headers[name] = &openapi3.HeaderRef{Value: ref.Value}
		ref.Ref = "#/components/headers/" + name
	}
}

func refStrings[T comparable](m map[T]string) []string {
	seen := make(map[string]bool, len(m))
	out := make([]string, 0, len(m))
	for _, ref := range m {
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func existingNames[T any](m map[string]T) map[string]bool {
	names := make(map[string]bool, len(m))
	for name := range m {
		names[name] = true
	}
	return names
}

// bundleNames computes a deterministic, collision-free local component
// name for each distinct external $ref string in refs, sorted before
// assignment so name assignment never depends on Go's own randomized
// map iteration order (this project's own determinism rule -- anything
// feeding a hash must have canonical, reproducible output). existing is
// every name already used in the real target component map -- a real
// collision (an external target's own derived base name already taken,
// either by a pre-existing local definition or another external target
// sharing the same base name from a different file) is resolved by
// appending a deterministic numeric suffix, in sorted order, never
// randomly.
func bundleNames(refs []string, existing map[string]bool) map[string]string {
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	taken := make(map[string]bool, len(existing)+len(sorted))
	for k := range existing {
		taken[k] = true
	}
	names := make(map[string]string, len(sorted))
	for _, ref := range sorted {
		base := refLocalName(ref)
		name := base
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		taken[name] = true
		names[ref] = name
	}
	return names
}

// refLocalName derives a real, readable local component name from an
// external $ref string's own fragment ("../../.../types.json#/
// definitions/ProxyResource" -> "ProxyResource") -- falls back to the
// referenced file's own base name (extension stripped) for the rare,
// unseen-in-any-real-sample case of a fragment-less external ref (the
// whole external file IS the schema).
func refLocalName(ref string) string {
	file, frag, hasFrag := strings.Cut(ref, "#/")
	if hasFrag {
		parts := strings.Split(frag, "/")
		if last := parts[len(parts)-1]; last != "" {
			return last
		}
	}
	base := file
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".json")
	base = strings.TrimSuffix(base, ".yaml")
	base = strings.TrimSuffix(base, ".yml")
	if base == "" {
		base = "external"
	}
	return base
}
