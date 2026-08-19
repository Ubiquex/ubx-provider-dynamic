// Package resourcemap discovers CRUD-shaped resources out of a real
// OpenAPI 3.x document's own paths/operations -- UBI-158 Phase 1's layer 3.
//
// Real-world REST APIs are not path-symmetric the way a naive "singular
// item path has GET/PATCH/DELETE, its own parent path has POST" heuristic
// would assume -- confirmed directly against GitHub's own real spec while
// building this: a repository is read at /repos/{owner}/{repo} but created
// at /orgs/{org}/repos or /user/repos, never at /repos itself (that
// collection path doesn't exist in GitHub's API at all). The one thing
// that DOES reliably connect a create operation to the item it created,
// across real APIs, is that both describe the same underlying resource
// shape -- so this package's real pairing key is response-schema identity
// (matching by the response body's own $ref component name), not path
// structure. Path structure is still used, but only for the narrower,
// reliable question "does this exact item path also have an update/delete
// operation" -- a real, common REST convention this package found to hold
// for both GitHub and Datadog wherever a read path exists at all.
package resourcemap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Resource is one discovered CRUD-shaped resource type.
type Resource struct {
	TypeName string // <provider>_<noun>, ubx's own snake_case convention

	ReadPath      string
	ReadOperation *openapi3.Operation

	CreatePath      string
	CreateMethod    string
	CreateOperation *openapi3.Operation

	// UpdateMethod is "PATCH" or "PUT" -- empty, with UpdateOperation nil,
	// for a genuinely immutable/replace-only resource (real: several
	// GitHub/Datadog resource types have no update operation at all;
	// modeled honestly as create/delete-only rather than inventing one).
	UpdateMethod    string
	UpdateOperation *openapi3.Operation

	DeleteOperation *openapi3.Operation

	// PathParams is ReadPath's own {param} segment names, in URL order --
	// what a CRUD executor needs to fill in from resource state to build
	// a real request URL (layer 6, REST execution). Also used for
	// Update/Delete: both are only ever matched on the exact same path as
	// Read (see findSibling), so they share this same param set.
	PathParams []string

	// CreatePathParams is CreatePath's own {param} segment names -- a
	// real, separate set from PathParams whenever Create lives on a
	// different path than Read (GitHub's own /orgs/{org}/repos vs.
	// /repos/{owner}/{repo}: "org" has no read-path counterpart at all).
	CreatePathParams []string

	// ResponseSchemaRefName is the response component schema's own name
	// (e.g. "full-repository") when the response schema is a $ref; empty
	// for an inline response schema, in which case Noun was derived from
	// ReadPath instead -- see deriveNoun.
	ResponseSchemaRefName string
}

// Note is a real, specific resource-mapping decision or gap worth
// surfacing, mirroring schema.Note's own role for layer 2.
type Note struct {
	Path   string
	Detail string
}

// op is one (method, path, *openapi3.Operation) triple -- the flat form
// this package walks the document into before grouping.
type op struct {
	method string
	path   string
	sub    *openapi3.Operation
}

// Discover walks doc's entire path set and returns every resource this
// package can confidently identify as CRUD-shaped, plus Notes explaining
// anything skipped or heuristically resolved. providerName prefixes every
// TypeName (ubx's own <provider>_<resource> convention).
//
// A "resource," for this package's purposes, requires at minimum a read
// operation (a GET on a path ending in a trailing {param} segment) AND a
// matching create operation (a POST or PUT anywhere in the document whose own
// response references the identical component schema) -- read-only paths
// with no discoverable create are real (GitHub has many: rate_limit,
// meta, ...) but are data sources, not resources, genuinely out of this
// ticket's Phase 1 scope ("resource mapping," CRUD lifecycle), and are
// recorded as skipped Notes rather than silently dropped.
func Discover(doc *openapi3.T, providerName string) ([]Resource, []Note, error) {
	if doc.Paths == nil {
		return nil, nil, fmt.Errorf("document has no paths")
	}

	var ops []op
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Find(path)
		if item == nil {
			continue
		}
		for method, sub := range map[string]*openapi3.Operation{
			"GET": item.Get, "POST": item.Post, "PATCH": item.Patch,
			"PUT": item.Put, "DELETE": item.Delete,
		} {
			if sub != nil {
				ops = append(ops, op{method: method, path: path, sub: sub})
			}
		}
	}

	var notes []Note
	var resources []Resource
	seenTypeNames := map[string]string{} // TypeName -> ReadPath, for collision detection

	readCandidates := filterReadCandidates(ops)
	for _, rc := range readCandidates {
		refName, respSchema := ResponseSchema(rc.sub)
		if respSchema == nil {
			notes = append(notes, Note{Path: rc.path, Detail: "GET has no JSON response schema -- skipped, cannot derive a resource shape"})
			continue
		}

		create, createOp := findCreate(ops, rc.path, refName, respSchema)
		if create == nil {
			notes = append(notes, Note{Path: rc.path, Detail: "no matching create (POST or PUT) operation found by response-schema or parent-collection-path match -- read-only, modeled as a data source concern, not a resource (out of Phase 1 scope)"})
			continue
		}

		service, noun, nounNote := deriveNoun(refName, rc.path)
		if nounNote != "" {
			notes = append(notes, Note{Path: rc.path, Detail: nounNote})
		}
		typeName := providerName + "_" + noun
		if service != "" {
			typeName = providerName + "_" + service + "_" + noun
		}
		if existingPath, dup := seenTypeNames[typeName]; dup {
			// Real, confirmed against GitHub's own spec: two distinct
			// item paths can share one response schema on purpose --
			// /gists/{gist_id} (a gist) and /gists/{gist_id}/{sha} (one
			// specific historical revision of that same gist) both
			// return gist-simple. Not an error: readCandidates is walked
			// in sorted path order, so the shorter/more canonical path
			// (a real, deterministic, if approximate, proxy for "the
			// actual resource, not a sub-view of it") always wins the
			// name and the later one is skipped with a Note, the same
			// "skip with a documented reason" discipline every other gap
			// in this package uses rather than a hard failure that would
			// block every OTHER resource in the same document too.
			notes = append(notes, Note{Path: rc.path, Detail: fmt.Sprintf("resource type name %q already claimed by %q (same response schema, different path) -- skipped rather than disambiguated", typeName, existingPath)})
			continue
		}
		seenTypeNames[typeName] = rc.path

		res := Resource{
			TypeName:              typeName,
			ReadPath:              rc.path,
			ReadOperation:         rc.sub,
			CreatePath:            createOp.path,
			CreateMethod:          createOp.method,
			CreateOperation:       create,
			PathParams:            pathParams(rc.path),
			CreatePathParams:      pathParams(createOp.path),
			ResponseSchemaRefName: refName,
		}

		if u := findSibling(ops, rc.path, "PATCH"); u != nil {
			res.UpdateMethod, res.UpdateOperation = "PATCH", u
		} else if u := findSibling(ops, rc.path, "PUT"); u != nil {
			res.UpdateMethod, res.UpdateOperation = "PUT", u
		} else {
			notes = append(notes, Note{Path: rc.path, Detail: "no PATCH or PUT on this item path -- modeled as create/delete-only, no in-place update"})
		}

		if d := findSibling(ops, rc.path, "DELETE"); d != nil {
			res.DeleteOperation = d
		} else {
			notes = append(notes, Note{Path: rc.path, Detail: "no DELETE on this item path -- modeled without a real destroy operation"})
		}

		resources = append(resources, res)
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].TypeName < resources[j].TypeName })
	return resources, notes, nil
}

// filterReadCandidates returns every GET operation whose path ends in a
// trailing {param} segment -- real APIs' own "single item" convention,
// confirmed against both GitHub's and Datadog's real specs.
func filterReadCandidates(ops []op) []op {
	var out []op
	for _, o := range ops {
		if o.method == "GET" && endsInPathParam(o.path) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func endsInPathParam(path string) bool {
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(segs) == 0 {
		return false
	}
	last := segs[len(segs)-1]
	return strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}")
}

func pathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}"))
		}
	}
	return out
}

func findSibling(ops []op, path, method string) *openapi3.Operation {
	for _, o := range ops {
		if o.path == path && o.method == method {
			return o.sub
		}
	}
	return nil
}

// findCreate looks for a POST or PUT whose own response references the
// identical component schema as the read path (the real, reliable
// cross-API pairing key -- see the package doc comment), preferring the
// fewest path parameters (the shallowest/most general creation endpoint
// -- e.g. GitHub's own /user/repos over a more specific one, when both
// exist) and, as a tie-break, the shortest path string, both purely for
// determinism (CLAUDE.md's own standing rule) since either real match is
// equally valid. PUT is a real, generic create signal, not an
// Azure-specific special case: ARM's own real convention (and any REST
// API's own idempotent-upsert convention -- ARM is not the only one) is
// PUT-to-the-item-path-itself, not POST-to-a-collection -- confirmed
// live against Azure's own real Microsoft.Compute spec, whose
// virtualMachines item path's GET/PUT/PATCH operations all reference the
// identical #/definitions/VirtualMachine response schema, so this same
// response-schema-identity match already catches it correctly once PUT
// is a candidate method, with no separate same-path special case needed.
// The existing fewest-path-params tiebreak still correctly prefers a
// real POST-to-collection endpoint over an item-path PUT whenever BOTH
// exist for the same resource (an item path binds every one of its own
// path params, so it can never have FEWER than its own creating
// collection endpoint) -- confirmed by inspection, not just assumed:
// GitHub's/Kubernetes'/Datadog's own real POST-based resources are
// unaffected by this widening. Falls back to a POST on the read path's
// own immediate parent collection (path with its trailing {param}
// segment stripped) only when no response-schema match exists at all --
// a real but weaker heuristic, since some real POST operations return
// 202/204 with no body to match against.
func findCreate(ops []op, readPath, refName string, readSchema *openapi3.Schema) (*openapi3.Operation, *op) {
	var matches []op
	for _, o := range ops {
		if o.method != "POST" && o.method != "PUT" {
			continue
		}
		candidateRef, candidateSchema := ResponseSchema(o.sub)
		if candidateSchema == nil {
			continue
		}
		if refName != "" && candidateRef != "" {
			if candidateRef == refName {
				matches = append(matches, o)
			}
			continue
		}
		// Neither side has a $ref name (both inline) -- fall back to a
		// structural check: same set of top-level property names. Cheap
		// and real enough to catch an inline-schema'd create/read pair
		// without pulling in a full deep-equality comparison.
		if refName == "" && candidateRef == "" && sameTopLevelProperties(readSchema, candidateSchema) {
			matches = append(matches, o)
		}
	}
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool {
			pi, pj := len(pathParams(matches[i].path)), len(pathParams(matches[j].path))
			if pi != pj {
				return pi < pj
			}
			return matches[i].path < matches[j].path
		})
		return matches[0].sub, &matches[0]
	}

	parent := parentCollectionPath(readPath)
	for _, o := range ops {
		if o.method == "POST" && o.path == parent {
			return o.sub, &o
		}
	}
	return nil, nil
}

func parentCollectionPath(path string) string {
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(segs) == 0 {
		return path
	}
	return strings.Join(segs[:len(segs)-1], "/")
}

func sameTopLevelProperties(a, b *openapi3.Schema) bool {
	if a == nil || b == nil || len(a.Properties) == 0 {
		return false
	}
	if len(a.Properties) != len(b.Properties) {
		return false
	}
	for name := range a.Properties {
		if _, ok := b.Properties[name]; !ok {
			return false
		}
	}
	return true
}

// responseSchema returns the operation's own success response's JSON
// schema -- preferring, in order, the first 2xx status found in ascending
// numeric order (200 before 201 before 202...), then "default" if no
// explicit 2xx exists. Within a matched response, prefers the
// "application/json" content type, falling back to the lexicographically
// first content type present (determinism, not correctness -- a real spec
// with no application/json response at all is rare enough that Phase 1
// doesn't special-case it further).
func ResponseSchema(op *openapi3.Operation) (refName string, schema *openapi3.Schema) {
	if op == nil || op.Responses == nil {
		return "", nil
	}
	for _, code := range []int{200, 201, 202, 203, 204} {
		if rr := op.Responses.Status(code); rr != nil && rr.Value != nil {
			if ref, s := mediaTypeSchema(rr.Value.Content); s != nil {
				return ref, s
			}
		}
	}
	if rr := op.Responses.Default(); rr != nil && rr.Value != nil {
		return mediaTypeSchema(rr.Value.Content)
	}
	return "", nil
}

// RequestBodySchema returns op's own JSON request body schema, nil if it
// has none (a real, common case: several real DELETE-adjacent or
// action-style POSTs take no body at all).
func RequestBodySchema(op *openapi3.Operation) *openapi3.Schema {
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil
	}
	_, s := mediaTypeSchema(op.RequestBody.Value.Content)
	return s
}

func mediaTypeSchema(content openapi3.Content) (refName string, schema *openapi3.Schema) {
	if mt, ok := content["application/json"]; ok && mt.Schema != nil && mt.Schema.Value != nil {
		return refString(mt.Schema.Ref), mt.Schema.Value
	}
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		mt := content[k]
		if mt != nil && mt.Schema != nil && mt.Schema.Value != nil {
			return refString(mt.Schema.Ref), mt.Schema.Value
		}
	}
	return "", nil
}

// refString extracts a component schema's own name from a JSON-pointer
// $ref ("#/components/schemas/full-repository" -> "full-repository");
// empty for anything not shaped like a component reference (an inline
// schema, or a $ref into another document section this package doesn't
// treat as a naming source).
func refString(ref string) string {
	const prefix = "#/components/schemas/"
	if strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix)
	}
	return ""
}

// deriveNoun picks the resource-type service (optional) and noun: the
// response component schema's own name when one exists (the strong, real
// signal), snake_cased -- further split into (service, noun) when that
// name itself carries a real, structural service/group qualifier (see
// splitQualifiedRefName) -- falling back to the read path's own last
// non-parameter segment, naively singularized (trailing "s" stripped, real
// English-plural-only heuristic -- documented as approximate, not a real
// inflection engine, since pulling one in for this fallback path alone
// isn't proportionate to how rarely real specs leave a response schema
// unnamed) when no response schema name exists at all. service is always
// "" in the fallback case -- a bare path segment carries no real service
// signal to extract.
func deriveNoun(refName, readPath string) (service, noun string, note string) {
	if refName != "" {
		if svc, n, ok := splitQualifiedRefName(refName); ok {
			return toSnakeCase(svc), toSnakeCase(n), ""
		}
		return "", toSnakeCase(refName), ""
	}
	segs := strings.Split(strings.TrimSuffix(readPath, "/"), "/")
	var last string
	for i := len(segs) - 2; i >= 0; i-- {
		if !strings.HasPrefix(segs[i], "{") {
			last = segs[i]
			break
		}
	}
	if last == "" {
		last = "resource"
	}
	singular := strings.TrimSuffix(last, "s")
	return "", toSnakeCase(singular), fmt.Sprintf("response schema has no component name (inline schema) -- resource noun %q derived from the read path itself instead, a weaker heuristic than the usual response-schema-name match", singular)
}

// apiVersionPattern matches a real API version token -- "v1", "v1beta1",
// "v2alpha3", ... -- the real, load-bearing signal splitQualifiedRefName
// uses to recognize a dotted, package-qualified response schema name's own
// real structure, rather than guessing from segment count alone.
var apiVersionPattern = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

// splitQualifiedRefName recognizes a real, structural naming convention
// some OpenAPI-sourced APIs use for their own response component schema
// names -- a dotted, package-qualified identifier whose own second-to-last
// segment is a real API version token. Confirmed live against Kubernetes'
// own real, converted-from-Swagger2 spec: "io.k8s.api.apps.v1.Deployment" and
// "io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition"
// are both real, both correctly split by taking the version token's own
// immediate neighbors -- the segment before it as the real API group
// (service), the segment after it as the real resource Kind (noun) --
// without needing to hardcode either real prefix depth ("io.k8s.api." vs.
// "io.k8s.<component>.pkg.apis.") as a special case.
//
// Deliberately generic, not Kubernetes-specific in code: any OpenAPI-sourced
// spec whose own response schemas happen to share this real shape gets the
// identical treatment; a spec whose ref names don't match it (GitHub's/
// Datadog's own flatter, single-concept names, e.g. "full-repository") falls
// through to deriveNoun's own existing, unchanged single-noun behavior.
func splitQualifiedRefName(refName string) (service, noun string, ok bool) {
	segs := strings.Split(refName, ".")
	if len(segs) < 3 {
		return "", "", false
	}
	version := segs[len(segs)-2]
	if !apiVersionPattern.MatchString(version) {
		return "", "", false
	}
	return segs[len(segs)-3], segs[len(segs)-1], true
}

func toSnakeCase(s string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range s {
		switch {
		case r == '-' || r == '.' || r == ' ' || r == '_':
			if b.Len() > 0 {
				b.WriteByte('_')
			}
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
