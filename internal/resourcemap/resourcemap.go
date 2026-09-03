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
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dsfilter"
	"github.com/ubiquex/ubx-provider-dynamic/internal/typename"
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

	// Phase 1: resolve every real read candidate into a base typeName,
	// independent of any collision it might turn out to share with a
	// sibling -- genuine skips (no response schema, no matching create)
	// are recorded as Notes here and go no further, unchanged from
	// before this was split into two phases.
	readCandidates := filterReadCandidates(ops)
	var cands []candidate
	for _, rc := range readCandidates {
		refName, respSchema := ResponseSchema(rc.sub)
		if respSchema == nil {
			notes = append(notes, Note{Path: rc.path, Detail: "GET has no JSON response schema -- skipped, cannot derive a resource shape"})
			continue
		}

		service, version, noun, nounNote := deriveNoun(refName, rc.path)
		if nounNote != "" {
			notes = append(notes, Note{Path: rc.path, Detail: nounNote})
		}

		create, createOp := findCreate(ops, rc.path, refName, respSchema)
		if create == nil {
			var excludedOpID string
			var excludedReason dsfilter.Reason
			create, createOp, excludedOpID, excludedReason = findAllowlistedCreate(ops, rc.path, noun, refName)
			if create == nil {
				if excludedOpID != "" {
					notes = append(notes, Note{Path: rc.path, Detail: fmt.Sprintf("an alternate create-verb operation (%s) exists but is excluded by the five-rule filter (%s) -- read-only, modeled as a data source concern, not a resource", excludedOpID, excludedReason)})
				} else {
					notes = append(notes, Note{Path: rc.path, Detail: "no matching create (POST or PUT) operation found by response-schema, parent-collection-path, or UBI-181's narrow create-verb allowlist -- read-only, modeled as a data source concern, not a resource (out of Phase 1 scope)"})
				}
				continue
			}
			notes = append(notes, Note{Path: rc.path, Detail: fmt.Sprintf("matched via UBI-181's narrow create-verb allowlist (%s), not a standard create/insert operation", createOp.sub.OperationID)})
		}
		baseTypeName := typename.Combine(providerName, service, noun)
		cands = append(cands, candidate{
			rc: rc, refName: refName, create: create, createOp: createOp,
			service: service, version: version, noun: noun,
			baseTypeName: baseTypeName,
		})
	}

	// Phase 2: within each group of candidates sharing a base typeName,
	// pick the real winner -- the real, standard Kubernetes API
	// version-priority order (see versionPriority's own doc comment)
	// when every member carries a real version token, falling back to
	// original ReadPath order (the exact pre-existing tie-break) when it
	// doesn't. The winner keeps the plain, unversioned typeName; every
	// other member gets a version-qualified fallback name instead of
	// being dropped, when it has a real version token to qualify with
	// (UBI-176: 21 real Kubernetes resources -- an alpha/beta/older-
	// major sibling of an already-claimed stable type -- were silently
	// lost this way before; see versionPriority's own doc comment for
	// why "older-major" is real too, not just alpha/beta).
	byBase := map[string][]int{} // baseTypeName -> indices into cands
	for i, c := range cands {
		byBase[c.baseTypeName] = append(byBase[c.baseTypeName], i)
	}

	finalTypeName := make([]string, len(cands))
	seenTypeNames := map[string]string{} // TypeName -> ReadPath, for the defensive re-collision check below

	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases) // deterministic iteration order, output is re-sorted by TypeName below regardless

	for _, base := range bases {
		idxs := byBase[base]
		sort.SliceStable(idxs, func(a, b int) bool {
			ca, cb := cands[idxs[a]], cands[idxs[b]]
			if ca.version != "" && cb.version != "" {
				pa, pb := versionPriority(ca.version), versionPriority(cb.version)
				if pa != pb {
					return pa.higherThan(pb)
				}
			} else if (ca.version != "") != (cb.version != "") {
				// Mixed real-version/no-version collision in the same
				// group is not a real shape this package's own real,
				// live-checked providers ever produce (confirmed:
				// Kubernetes' own qualified ref names always carry a
				// version; GitHub's/Datadog's own flat ref names never
				// do) -- if it ever did, preferring the versioned one is
				// the safer default (it can be recovered under a
				// versioned name if it loses; an unversioned candidate
				// has no such fallback).
				return ca.version != ""
			}
			return idxs[a] < idxs[b] // original ReadPath order, the exact pre-existing tie-break
		})

		winner := cands[idxs[0]]
		finalTypeName[idxs[0]] = winner.baseTypeName
		seenTypeNames[winner.baseTypeName] = winner.rc.path

		for _, i := range idxs[1:] {
			c := cands[i]
			if c.version == "" {
				notes = append(notes, Note{Path: c.rc.path, Detail: fmt.Sprintf("resource type name %q already claimed by %q (same response schema, different path) -- skipped rather than disambiguated", base, winner.rc.path)})
				continue
			}
			versionedTypeName := providerName + "_" + c.version + "_" + c.noun
			if c.service != "" {
				versionedTypeName = providerName + "_" + c.service + "_" + c.version + "_" + c.noun
			}
			if existingPath, dup := seenTypeNames[versionedTypeName]; dup {
				notes = append(notes, Note{Path: c.rc.path, Detail: fmt.Sprintf("resource type name %q already claimed by %q (same response schema, different path); version-qualified name %q ALSO already claimed by %q -- skipped rather than disambiguated further", base, winner.rc.path, versionedTypeName, existingPath)})
				continue
			}
			finalTypeName[i] = versionedTypeName
			seenTypeNames[versionedTypeName] = c.rc.path
		}
	}

	for i, c := range cands {
		if finalTypeName[i] == "" {
			continue // lost the collision, already noted above
		}
		res := Resource{
			TypeName:              finalTypeName[i],
			ReadPath:              c.rc.path,
			ReadOperation:         c.rc.sub,
			CreatePath:            c.createOp.path,
			CreateMethod:          c.createOp.method,
			CreateOperation:       c.create,
			PathParams:            pathParams(c.rc.path),
			CreatePathParams:      pathParams(c.createOp.path),
			ResponseSchemaRefName: c.refName,
		}

		if u := findSibling(ops, c.rc.path, "PATCH"); u != nil {
			res.UpdateMethod, res.UpdateOperation = "PATCH", u
		} else if u := findSibling(ops, c.rc.path, "PUT"); u != nil {
			res.UpdateMethod, res.UpdateOperation = "PUT", u
		} else {
			notes = append(notes, Note{Path: c.rc.path, Detail: "no PATCH or PUT on this item path -- modeled as create/delete-only, no in-place update"})
		}

		if d := findSibling(ops, c.rc.path, "DELETE"); d != nil {
			res.DeleteOperation = d
		} else {
			notes = append(notes, Note{Path: c.rc.path, Detail: "no DELETE on this item path -- modeled without a real destroy operation"})
		}

		resources = append(resources, res)
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].TypeName < resources[j].TypeName })
	return resources, notes, nil
}

// candidate is one real read candidate that cleared Phase 1 (has a
// response schema and a matching create) -- everything Phase 2 needs to
// resolve a same-base-typeName collision without re-deriving anything.
// Its own position in the cands slice (built in the same, already
// ReadPath-sorted order readCandidates has) IS the original ReadPath
// order tie-break -- no separate index field needed.
type candidate struct {
	rc                     op
	refName                string
	create                 *openapi3.Operation
	createOp               *op
	service, version, noun string
	baseTypeName           string
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

// findAllowlistedCreate is Phase 1's own third, narrowest create
// signal, tried only after findCreate's real schema-match and parent-
// collection-path checks both come back empty: a same-path
// (dsfilter.SamePathAction, never a prefix/sibling match -- see its own
// doc comment for the real misattribution this closes, found live in
// this exact corpus: Azure's SqlPoolSensitivityLabels_CreateOrUpdate
// lives two segments and a new path parameter below its own read
// candidate, a genuinely separate nested resource, not an action on it)
// POST/PUT whose own operation ID matches UBI-181's narrow create-verb
// allowlist (dsfilter.MatchesCreateVerb), gated through the five-rule
// filter (dsfilter.Excluded) on the candidate's own noun so a
// genuinely non-resource pattern (a watch path, an operation-status
// shape, ...) that happens to carry an allowlisted-sounding verb
// nearby is never promoted.
//
// excludedOpID/excludedReason are set (with a nil match) when an
// allowlisted verb was found at the right path but the noun itself was
// excluded -- the caller surfaces this as its own real, specific Note
// rather than the generic "no matching create" one, the same "skip,
// don't silently drop" discipline this whole file already follows.
func findAllowlistedCreate(ops []op, readPath, noun, refName string) (matchOp *openapi3.Operation, matched *op, excludedOpID string, excludedReason dsfilter.Reason) {
	for i := range ops {
		o := ops[i]
		if o.method != "POST" && o.method != "PUT" {
			continue
		}
		if o.sub == nil || o.sub.OperationID == "" {
			continue
		}
		// createFamilyTokens (bare "create"/"addorupdate") are
		// genuinely ambiguous with a sibling collection's own create
		// when matched via a path suffix (dsfilter.MatchesCreateVerb's
		// own doc comment has the real, live-found case this guards --
		// GitHub's "/orgs/{org}/repos" POST, misattributed to
		// "/orgs/{org}" otherwise) -- restricted to an exact path
		// match. actionVerbTokens (restore/initiate/...) are safe
		// against SamePathAction's own single-action-suffix form too.
		switch {
		case o.path == readPath && dsfilter.MatchesCreateVerb(o.sub.OperationID):
		case o.path != readPath && dsfilter.SamePathAction(readPath, o.path) && dsfilter.MatchesActionVerb(o.sub.OperationID):
		default:
			continue
		}
		reason, excluded := dsfilter.Excluded(dsfilter.Candidate{
			Noun: noun, Path: readPath, OperationName: o.sub.OperationID, ResponseTypeName: refName,
		})
		if excluded {
			if excludedOpID == "" {
				excludedOpID, excludedReason = o.sub.OperationID, reason
			}
			continue
		}
		return o.sub, &ops[i], "", ""
	}
	return nil, nil, excludedOpID, excludedReason
}

func parentCollectionPath(path string) string {
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(segs) == 0 {
		return path
	}
	return strings.Join(segs[:len(segs)-1], "/")
}

// sameTopLevelProperties is findCreate's own inline-schema fallback
// signal: cheap, real enough to catch an inline-schema'd create/read
// pair without a full deep-equality comparison -- as long as matching
// property NAMES alone is not the whole check. Confirmed live,
// UBI-222: Cloudflare's own real generic response envelope
// ("errors"/"messages"/"result"/"success", shared verbatim across
// thousands of genuinely unrelated real endpoints) made a name-only
// comparison match ANY two inline-schema'd operations using that
// shape -- confirmed, cloudflare_abuse_report's own real CREATE
// operation was wired to POST /accounts/move ("Batch move accounts...
// Not implemented", per its own real description), a completely
// unrelated endpoint that happens to share the identical four
// property names. Both responses wrap their own real entity in a
// "result" field that is itself a named $ref (abuse-reports_AbuseReport
// vs organizations-api_BatchAccountMoveResponse) -- genuinely
// different real types the name-only check could not see.
//
// Fixed by also requiring every shared property that is a named $ref
// on EITHER side to be the IDENTICAL $ref on both -- the real,
// discriminating signal a generic envelope's own boilerplate fields
// (errors/messages/success, rarely $refs pointing at the entity
// itself) don't carry, but the one field actually wrapping the real
// entity (Cloudflare's own "result", not assumed to always be named
// that -- checked generically, whichever shared property is itself
// $ref'd) does.
func sameTopLevelProperties(a, b *openapi3.Schema) bool {
	if a == nil || b == nil || len(a.Properties) == 0 {
		return false
	}
	if len(a.Properties) != len(b.Properties) {
		return false
	}
	for name, aProp := range a.Properties {
		bProp, ok := b.Properties[name]
		if !ok {
			return false
		}
		aRef := aProp.Ref
		bRef := bProp.Ref
		if (aRef != "" || bRef != "") && aRef != bRef {
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

// deriveNoun picks the resource-type service (optional), noun, and API
// version (optional -- see splitQualifiedRefName): the response component
// schema's own name when one exists (the strong, real signal), snake_cased
// -- further split into (service, version, noun) when that name itself
// carries a real, structural service/group qualifier (see
// splitQualifiedRefName) -- falling back to the read path's own last
// non-parameter segment, singularized via singularize below, when no
// response schema name exists at all. service and version are always
// "" in the fallback case -- a bare path segment carries no real
// service/version signal to extract.
func deriveNoun(refName, readPath string) (service, version, noun string, note string) {
	if refName != "" {
		if svc, v, n, ok := splitQualifiedRefName(refName); ok {
			return toSnakeCase(svc), v, toSnakeCase(n), ""
		}
		return "", "", toSnakeCase(refName), ""
	}
	// Starts at the TRUE last segment, not len(segs)-2 -- correct
	// either way when the path ends in a {param} (a single-item
	// resource read path: the param is skipped, same as before), but
	// only correct when it doesn't (a collection-shaped data source
	// path with no trailing {param}, real content skipped-then-lost
	// there was the real UBI-222 Cloudflare bug: "ai" instead of
	// "finetune" for .../ai/finetunes, "d1" instead of "database" for
	// .../d1/database, and a genuine crash when the wrongly-picked
	// segment singularized down to an empty string).
	segs := strings.Split(strings.TrimSuffix(readPath, "/"), "/")
	var last string
	for i := len(segs) - 1; i >= 0; i-- {
		if !strings.HasPrefix(segs[i], "{") {
			last = segs[i]
			break
		}
	}
	if last == "" {
		last = "resource"
	}
	singular := singularize(last)
	return "", "", toSnakeCase(singular), fmt.Sprintf("response schema has no component name (inline schema) -- resource noun %q derived from the read path itself instead, a weaker heuristic than the usual response-schema-name match", singular)
}

// singularize is internal/discoverydoc's own real, twice-fixed
// heuristic (UBI-102), duplicated here rather than imported -- this
// package stays independent of discoverydoc's own GCP-specific
// concerns, the same real reasoning every other schema source pair in
// this org already follows, and a pure, ~15-line string function costs
// nothing to keep in sync by hand versus a cross-source dependency that
// has no other reason to exist.
//
// The original version here (bare `strings.TrimSuffix(s, "s")`) was the
// IDENTICAL bug discoverydoc.singularize's own doc comment already
// named as its real twin at the time THAT fix was written -- "the
// identical, deliberately approximate heuristic
// internal/resourcemap.deriveNoun's own fallback path already uses for
// the same real reason" -- and was never actually ported over. Found
// live against DigitalOcean's own real spec (UBI-222): "byoip_prefixes"
// -> "byoip_prefixe", "registries" -> "registrie", the exact same class
// of real, embarrassing misspelling discoverydoc's own fix already
// eliminated for GCP. Verified this ports cleanly: run directly against
// both real, broken DigitalOcean strings, produces "prefix" and
// "registry".
func singularize(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "es"):
		stripped := strings.TrimSuffix(s, "es")
		if strings.HasSuffix(stripped, "ss") || strings.HasSuffix(stripped, "x") ||
			strings.HasSuffix(stripped, "z") || strings.HasSuffix(stripped, "ch") ||
			strings.HasSuffix(stripped, "sh") {
			return stripped
		}
		return strings.TrimSuffix(s, "s")
	default:
		return strings.TrimSuffix(s, "s")
	}
}

// apiVersionPattern matches a real API version token -- "v1", "v1beta1",
// "v2alpha3", ... -- the real, load-bearing signal splitQualifiedRefName
// uses to recognize a dotted, package-qualified response schema name's own
// real structure, rather than guessing from segment count alone.
var apiVersionPattern = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

// versionPriorityPattern is apiVersionPattern's own numeric-capturing
// twin, used only by versionPriority (apiVersionPattern itself stays a
// pure validity check, unchanged, since every other caller only needs a
// yes/no answer).
var versionPriorityPattern = regexp.MustCompile(`^v([0-9]+)(?:(alpha|beta)([0-9]+))?$`)

// vPriority orders real Kubernetes-style version tokens so a HIGHER
// value is a MORE preferred version -- tier first (GA beats any
// prerelease, beta beats alpha), then major version number, then
// prerelease number.
type vPriority struct {
	tier, major, pre int
}

func (p vPriority) higherThan(other vPriority) bool {
	if p.tier != other.tier {
		return p.tier > other.tier
	}
	if p.major != other.major {
		return p.major > other.major
	}
	return p.pre > other.pre
}

// versionPriority parses a real, already apiVersionPattern-validated
// Kubernetes-style version token into vPriority -- the identical, real,
// standard algorithm kube-apiserver's own version-priority ordering
// uses (k8s.io/apimachinery's CompareKubeAwareVersionStrings): a GA
// version ("vN") always outranks ANY prerelease regardless of number,
// beta always outranks alpha, and within the same tier a higher
// major/prerelease number wins. Confirmed live against the real,
// checked-in per-API-group discovery snapshots Kubernetes itself
// publishes alongside its own OpenAPI spec (same repo, same release
// tag, api/discovery/apis__<group>.json's own "preferredVersion" field,
// not baked into swagger.json itself) -- this function's own output
// matches that real signal exactly for every currently-real collision
// this package resolves (autoscaling/v2 over v1, coordination/v1beta1
// over v1alpha2, scheduling/v1beta1 over v1alpha3), computed locally
// from the version string alone rather than requiring a second, live
// network fetch this package's own architecture doesn't otherwise need
// (Discover takes an already-parsed document, no I/O of its own).
// A malformed token (should never happen -- every caller already
// checked apiVersionPattern first) sorts as the lowest possible
// priority rather than panicking.
func versionPriority(v string) vPriority {
	m := versionPriorityPattern.FindStringSubmatch(v)
	if m == nil {
		return vPriority{tier: -1}
	}
	major, _ := strconv.Atoi(m[1])
	if m[2] == "" {
		return vPriority{tier: 2, major: major} // GA
	}
	pre, _ := strconv.Atoi(m[3])
	tier := 0 // alpha
	if m[2] == "beta" {
		tier = 1
	}
	return vPriority{tier: tier, major: major, pre: pre}
}

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
//
// version is returned alongside service/noun (not discarded) so a caller
// can disambiguate two resources that share both -- the real, live-found
// case (UBI-176) this same version token used to be dropped for entirely:
// "io.k8s.api.apps.v1.Deployment" and a hypothetical
// "io.k8s.api.apps.v1beta1.Deployment" both produce service="apps",
// noun="Deployment" and, pre-fix, an IDENTICAL typeName -- Discover's own
// seenTypeNames collision guard then silently kept only whichever sorted
// first by ReadPath and dropped the other outright, no matter which
// version was actually the real, current stable one (confirmed live: 21
// real Kubernetes resources -- alpha/beta siblings of an already-covered
// stable type, PLUS one real case, autoscaling/v2 HorizontalPodAutoscaler,
// where the OLDER v1 sorted first and silently ate v2's own name purely by
// string comparison, not because v1 is actually the current API). Already
// returned as a valid typeName segment (apiVersionPattern guarantees a
// clean lowercase "v[0-9]+(alpha|beta[0-9]+)?" shape) -- no further
// snake_case pass needed the way service/noun get one.
func splitQualifiedRefName(refName string) (service, version, noun string, ok bool) {
	segs := strings.Split(refName, ".")
	if len(segs) < 3 {
		return "", "", "", false
	}
	version = segs[len(segs)-2]
	if !apiVersionPattern.MatchString(version) {
		return "", "", "", false
	}
	return segs[len(segs)-3], version, segs[len(segs)-1], true
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
