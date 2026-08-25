// Package discoverydoc translates a real GCP Discovery Document (the
// third real schema-source format alongside OpenAPI and Smithy) into the
// same real *tfprotov6.Schema shape internal/schema.Translator already
// produces for OpenAPI -- schema-layer only, matching UBI-158 Phase 1's
// own original Kubernetes-checkpoint precedent (discover + translate
// first, real REST wire execution is separate, later work; see this
// package's own doc comment on Build for exactly what's out of scope).
//
// Real, confirmed structural finding (fetched live against Google Cloud
// Pub/Sub's own real, published discovery document,
// https://pubsub.googleapis.com/$discovery/rest?version=v1, before
// writing any code here): a Discovery Document's own real shape is
// GENUINELY different from OpenAPI's at the document level (a recursive
// resources.<name>.resources/methods tree instead of a flat Paths map,
// explicit method KEYS named "get"/"create"/"patch"/"delete" instead of
// resourcemap's own response-schema-identity heuristic -- GCP's own real
// convention is MORE reliable here, needing no heuristic at all) but its
// own "schemas" component dialect is close enough to OpenAPI's schema
// object (type/properties/items/additionalProperties/enum/description/
// readOnly/$ref) that this package converts a Discovery Document schema
// into a real, already-resolved *openapi3.Schema tree and hands it
// STRAIGHT to internal/schema.Translator's existing, mature,
// heavily-tested BuildTopLevel -- real code reuse of the one genuinely
// hard, load-bearing layer (nested object/array/map/union field
// translation), not a second, parallel implementation of it.
//
// Real, confirmed finding this reuse depends on, checked directly
// against Pub/Sub's own real schemas before relying on it: Discovery
// Documents carry NO required-field signal at the schema/body level at
// all -- neither a per-property "required" boolean nor an object-level
// "required" array anywhere in the real, live document (grepped, not
// assumed) -- only "readOnly" (a real, structured boolean, the identical
// OpenAPI convention Translator already reads). This is not a gap this
// package works around: it means every converted property naturally
// resolves to Optional (readOnly=false) or Computed (readOnly=true)
// through Translator's own existing fieldPolicy logic, unchanged, with
// zero special-casing needed here for GCP's own real "requiredness is
// enforced server-side, not declared in the schema" convention.
package discoverydoc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/fetchcache"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/typename"
)

// Document is one real, parsed Discovery Document -- the fields this
// package actually reads, not a full re-implementation of Google's own
// discovery schema (real, deliberate scope: everything else in a real
// document, batchPath/icons/auth/..., is genuinely irrelevant to schema
// translation).
type Document struct {
	Name string `json:"name"`
	// Revision is Google's own real per-document content-version stamp
	// (a YYYYMMDD-shaped string, confirmed live) -- read only as evidence
	// that two fetches genuinely differ (fetchcache's own doc comment has
	// the full, live-confirmed finding this documents), never itself fed
	// into a typeName or any other hashed value.
	Revision         string                  `json:"revision"`
	DiscoveryVersion string                  `json:"discoveryVersion"`
	BaseURL          string                  `json:"baseUrl"`
	RootURL          string                  `json:"rootUrl"`
	ServicePath      string                  `json:"servicePath"`
	Schemas          map[string]*rawSchema   `json:"schemas"`
	Resources        map[string]*rawResource `json:"resources"`
}

type rawResource struct {
	Methods   map[string]*rawMethod   `json:"methods"`
	Resources map[string]*rawResource `json:"resources"`
}

type rawMethod struct {
	ID             string               `json:"id"`
	HTTPMethod     string               `json:"httpMethod"`
	Path           string               `json:"path"`
	FlatPath       string               `json:"flatPath"`
	Description    string               `json:"description"`
	Parameters     map[string]*rawParam `json:"parameters"`
	ParameterOrder []string             `json:"parameterOrder"`
	Request        *rawRef              `json:"request"`
	Response       *rawRef              `json:"response"`
}

type rawParam struct {
	Type     string `json:"type"`
	Location string `json:"location"`
	Required bool   `json:"required"`
}

type rawRef struct {
	Ref string `json:"$ref"`
}

// rawSchema is one real Discovery-Document schema entry -- deliberately
// NOT a full JSON Schema implementation, only the real, confirmed
// vocabulary Pub/Sub's own live document actually uses (see this
// package's own doc comment for what was checked, not assumed).
type rawSchema struct {
	Type                 string                `json:"type"`
	Format               string                `json:"format"`
	Description          string                `json:"description"`
	Ref                  string                `json:"$ref"`
	Properties           map[string]*rawSchema `json:"properties"`
	Items                *rawSchema            `json:"items"`
	AdditionalProperties *rawSchema            `json:"additionalProperties"`
	Enum                 []string              `json:"enum"`
	ReadOnly             bool                  `json:"readOnly"`
}

// Load fetches and parses a real Discovery Document from source (an
// http(s) URL -- every real, published Google API discovery document is
// served this way; a bare file path is not a real usage shape for this
// source, unlike openapi.Load's own local-file convenience).
//
// The actual HTTP round trip goes through fetchcache.Get, not directly
// -- see that package's own doc comment for the real, live-confirmed
// finding it exists to work around (Google's own live discovery
// endpoints do not reliably return the same content twice in a row).
// Disabled by default: with UBX_PROVIDER_DYNAMIC_FETCH_CACHE unset,
// this is exactly the plain fetch it always was.
func Load(source string) (*Document, error) {
	body, err := fetchcache.Get(source, fetchDiscoveryDocument)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse discovery document %q: %w", source, err)
	}
	if doc.Resources == nil {
		return nil, fmt.Errorf("discovery document %q: no top-level \"resources\" -- not a real Discovery Document, or an API with nothing CRUD-shaped to offer", source)
	}
	return &doc, nil
}

func fetchDiscoveryDocument(source string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery document %q: %w", source, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch discovery document %q: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch discovery document %q: HTTP %d", source, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read discovery document %q: %w", source, err)
	}
	return body, nil
}

// Resource is one real, CRUD-shaped resource this package discovered --
// the schema-layer-only real fields this checkpoint's own scope needs
// (real HTTP method + path template for each real operation found,
// enough for a future, separate REST-execution checkpoint to consume
// directly; NOT wired to any execution today -- see Build's own doc
// comment).
type Resource struct {
	TypeName string

	ReadPath   string
	ReadMethod *rawMethod

	CreatePath   string
	CreateMethod string
	createMethod *rawMethod

	UpdateMethod string
	updateMethod *rawMethod

	HasDelete bool
}

// Note mirrors internal/schema.Note/internal/resourcemap.Note's own
// real role: a specific, worth-surfacing discovery/translation decision,
// never silently dropped.
type Note struct {
	Path   string
	Detail string
}

// Discover walks doc's own real resources tree (recursively, in
// deterministic key order) and returns every node that carries a real
// "get" method -- GCP's own real, explicit, reliable CRUD signal (no
// response-schema-identity heuristic needed the way OpenAPI's flatter,
// less structured paths require; internal/resourcemap's own doc comment
// has the full account of why THAT heuristic exists at all -- GCP simply
// doesn't need it). A node with "get" but no "create"/"insert" is a
// real, read-only data-source concern, recorded as a Note and skipped,
// the identical "skip, don't fail" discipline resourcemap's own Discover
// already uses.
//
// versionQualifier is config.Provider.VersionQualifier passed straight
// through (see that field's own doc comment) -- when non-empty, it is
// threaded into every typeName this call produces, between the API name
// and the noun. Real, found-live bug this parameter fixes: Google keeps
// a Discovery Document's own top-level "name" field IDENTICAL across
// release channels (compute's v1 and beta documents both report
// name="compute"; only the document's own separate "version" field
// differs), so configuring both a stable and a beta/alpha entry for the
// same API produces byte-identical typeNames for every resource the two
// channels share -- google_compute_instance from v1 AND from beta alike
// -- and the seenTypeNames guard below silently keeps only whichever one
// this function's own single call processes, with no cross-call
// awareness that the other channel's entry claimed the same name first.
// Exactly the shape UBI-176 already found and fixed for Kubernetes
// (internal/resourcemap.go's own versionPriority/version-qualified-name
// logic, for a different real cause -- OpenAPI ref names carrying a real
// version token this package's own Discovery Document input never has).
//
// Deliberately a config-declared, per-entry override (mirroring
// config.Provider.WireName's own established shape) rather than derived
// from doc.Version automatically: a real GCP API's own configured
// baseline is not reliably "v1" (12 of this corpus's 162 configured
// GCP APIs use v2/v3/v1b3 as their own GA channel, confirmed live via
// the real discovery directory), so there is no single literal string
// this package could safely treat as "the unversioned case" without
// risking a silent, corpus-wide rename the day a non-"v1"-baselined API
// ever grows a same-named secondary channel. A config-declared flag,
// set only on the entries that are deliberately being added as a
// secondary channel, has no such risk: every one of the 162 already-
// configured entries leaves this unset, producing byte-identical output
// to before this parameter existed -- verified directly, not assumed.
func Discover(doc *Document, providerName string, versionQualifier string) ([]Resource, []Note, error) {
	var resources []Resource
	var notes []Note
	seenTypeNames := map[string]bool{}

	var walk func(node map[string]*rawResource, path []string)
	walk = func(node map[string]*rawResource, path []string) {
		for _, name := range sortedKeys(node) {
			r := node[name]
			nodePath := append(append([]string{}, path...), name)
			pathStr := strings.Join(nodePath, ".")

			if get, ok := r.Methods["get"]; ok && get != nil {
				create, createFound := firstMethod(r.Methods, "create", "insert")
				if !createFound {
					create, createFound = firstPrefixedMethod(r.Methods, "create", "insert")
				}
				if !createFound {
					notes = append(notes, Note{Path: pathStr, Detail: "no matching create (\"create\"/\"insert\", or a \"create\"/\"insert\"-prefixed method key) -- read-only, modeled as a data source concern, not a resource"})
				} else {
					// Real, live-confirmed finding: a Discovery Document's
					// own resource-tree keys are camelCase
					// ("backendBuckets", "targetHttpProxies", ...), unlike
					// OpenAPI's own ref names (already snake_case by the
					// time deriveNoun sees them) -- caught only by
					// actually generating Go code against the real,
					// live, configured GCP Compute document (95 real
					// resources), which rejected a raw camelCase wire
					// name outright. uschema.ToSnakeCase is the identical
					// real conversion internal/schema.translate.go's own
					// ToSnakeCase already applies to every OpenAPI
					// property name, reused here for the resource noun
					// itself, not invented separately.
					noun := singularize(uschema.ToSnakeCase(name))
					// UBI-180: doc.Name -- the Discovery Document's own
					// top-level API name field -- gets the identical
					// ToSnakeCase treatment as the resource noun above,
					// for the identical real reason: it is Google's own,
					// live, API-owner-chosen string, not a value this
					// package controls, and every other real Discovery
					// Document's own doc.Name has been lowercase so far
					// (the sibling check in sdk/codegen/templates/{go,py,
					// ts}'s own splitWireName -- lowercase ascii + digit +
					// underscore only, no best-effort coercion, by this
					// whole codebase's own standing design choice,
					// docs/sdk.md row 5 -- had never actually been
					// exercised by a real mixed-case one until
					// siteVerification's own real, live doc.Name broke
					// it). Normalizing here, at the one real place a
					// Discovery Document's own name enters a typeName,
					// keeps every downstream consumer (generated Go/TS/
					// Python identifiers, this project's own docs site)
					// working from the same real, lowercase, snake_case
					// convention every other configured provider already
					// produces -- not a silent transform: the resulting
					// typeName is a real, deliberate, load-bearing part
					// of the wire contract users write against, so this
					// is the one, single place it happens, not a per-
					// language escape hatch three separate codegen
					// templates would each need to reimplement identically
					// (and could silently drift on).
					// UBI-185: providerName's own trailing token(s) can
					// already equal doc.Name's -- a real, live-found case
					// (google_billingbudgets combined with a doc.Name
					// that is ITSELF "billingbudgets" produced
					// google_billingbudgets_billingbudgets_budget instead
					// of the real, correct google_billingbudgets_budget,
					// confirmed systematic across every non-Compute GCP
					// family, not a one-off) -- the exact same overlap
					// class internal/resourcemap's own Discover() already
					// found and fixed for Azure/Kubernetes/GitHub/Datadog
					// (typename.Combine), just never applied here because
					// this is a structurally separate code path (Discovery
					// Documents, not OpenAPI/Swagger) with its own,
					// independent, blind concatenation. Sharing the one
					// real implementation, not porting a second copy, is
					// exactly why the bug survived the first fix at all.
					service := uschema.ToSnakeCase(doc.Name)
					if versionQualifier != "" {
						service += "_" + versionQualifier
					}
					typeName := typename.Combine(providerName, service, noun)
					if seenTypeNames[typeName] {
						notes = append(notes, Note{Path: pathStr, Detail: fmt.Sprintf("resource type name %q already claimed by another resource path -- skipped rather than disambiguated", typeName)})
					} else {
						seenTypeNames[typeName] = true
						update, updateFound := firstMethod(r.Methods, "patch", "update")
						res := Resource{
							TypeName:     typeName,
							ReadPath:     get.FlatPath,
							ReadMethod:   get,
							CreatePath:   create.FlatPath,
							CreateMethod: strings.ToUpper(create.HTTPMethod),
							createMethod: create,
						}
						if updateFound {
							res.UpdateMethod = strings.ToUpper(update.HTTPMethod)
							res.updateMethod = update
						} else {
							notes = append(notes, Note{Path: pathStr, Detail: "no \"patch\" or \"update\" method -- modeled as create/delete-only, no in-place update"})
						}
						if _, ok := r.Methods["delete"]; ok {
							res.HasDelete = true
						} else {
							notes = append(notes, Note{Path: pathStr, Detail: "no \"delete\" method -- modeled without a real destroy operation"})
						}
						resources = append(resources, res)
					}
				}
			}

			if r.Resources != nil {
				walk(r.Resources, nodePath)
			}
		}
	}
	walk(doc.Resources, nil)

	sort.Slice(resources, func(i, j int) bool { return resources[i].TypeName < resources[j].TypeName })
	return resources, notes, nil
}

func firstMethod(methods map[string]*rawMethod, names ...string) (*rawMethod, bool) {
	for _, n := range names {
		if m, ok := methods[n]; ok && m != nil {
			return m, true
		}
	}
	return nil, false
}

// firstPrefixedMethod is firstMethod's own real fallback for the resource-
// suffixed method-name convention some genuine GCP Discovery Documents use
// instead of a bare "create"/"insert" key -- confirmed live against
// iam:v2's own real "policies" collection (method key "createPolicy", not
// "create" -- the collection itself is intentionally generic, serving
// multiple real policy kinds via a URL "kind" segment, so Google
// disambiguated the method name rather than the collection name) --
// firstMethod's own strict exact-key match was silently treating this
// real, fully CRUD-capable resource as read-only.
//
// Prefix-only (not a substring/fuzzy match), and only ever used as a
// fallback after firstMethod's own exact match has already failed:
// every real "createXxx"/"insertXxx" method found so far genuinely IS
// that collection's own create operation, just resource-suffixed for
// clarity, matching the same discipline internal/resourcemap's own doc
// comment holds itself to for OpenAPI (narrow, live-confirmed
// heuristics only, never a broad verb guess). Deliberately does NOT
// also match a differently-named verb like "register"
// (domains:v1's own real "registrations" resource uses this instead --
// a genuinely different action, not this same naming convention) --
// folding an unrelated verb in here would risk treating some other
// real side-effecting action as an ordinary create; left as a real,
// separate, unresolved case, not silently swept into this fix.
// Deterministic when more than one candidate matches: picks the
// lexicographically first method key.
func firstPrefixedMethod(methods map[string]*rawMethod, prefixes ...string) (*rawMethod, bool) {
	var bestKey string
	var best *rawMethod
	for k, m := range methods {
		if m == nil {
			continue
		}
		for _, p := range prefixes {
			if k != p && strings.HasPrefix(k, p) && (bestKey == "" || k < bestKey) {
				bestKey, best = k, m
			}
		}
	}
	return best, best != nil
}

func sortedKeys(m map[string]*rawResource) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// singularize is the identical, deliberately approximate "trailing s
// stripped" heuristic internal/resourcemap.deriveNoun's own fallback
// path already uses for the same real reason: pulling in a real
// inflection engine for this one naming step isn't proportionate.
// singularize is real, found-in-review-fixable, TWICE in the same
// session: the original version (`strings.TrimSuffix(s, "s")`)
// mishandled every real "-es" English plural ("addresses" ->
// "addresse", "policies" -> "policie", "proxies" -> "proxie", 22 real
// resources affected). The first fix over-corrected: checking the
// SUFFIX BEFORE stripping ("ses"/"xes"/"zes"/"ches"/"shes") wrongly
// also matched real words that already end in "-se" in their own
// singular form and only ever add a bare "s" for the plural --
// confirmed live, shipped, and caught only by re-reading the real
// generated page list before reporting done: "licenses" -> "licens"
// (should be "license"), the exact same class of real, embarrassing
// misspelling the first fix was meant to eliminate, not reintroduce.
// The real, correct check is on the RESULT after stripping "es", not
// the suffix before: only accept the "-es" strip when what's left
// ends in a real sibilant sound English actually pluralizes with
// "-es" ("ss"/"x"/"z"/"ch"/"sh" -- "address"/"box"/"buzz"/"branch"/
// "dish"), never for a word that already, naturally ends in "-se"
// ("license", "house", "purse" -- singular already, just add "-s" for
// the real plural). A narrow, well-known English singularization
// rule, not a design decision (unlike the real, separate alpha/beta
// API-version-collision question this same generic naming layer also
// has -- that one needs an actual choice about what makes a type name
// unique across versions; this one has a single correct behavior,
// this function just took two real attempts to reach it).
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

// Build translates every resource Discover found into a real, merged
// *tfprotov6.Schema (create request + read response, the identical
// real merge semantics internal/dynserver.Build's own OpenAPI path
// already uses) plus its own combined enum/constraint FieldSignal tree.
//
// Real, deliberate, honestly-scoped limitation: this checkpoint is
// schema-layer only, the identical real precedent UBI-158 Phase 1 set
// for Kubernetes (discover + translate first, prove it live, real REST
// wire execution is separate, later work -- Smithy's own real Phase 1 ->
// Phase 4 staging is the same shape again). Build does NOT wire any real
// HTTP execution path -- ReadPath/CreatePath/CreateMethod/UpdateMethod on
// the returned BuiltResource are real, correct strings (GCP's own real
// flatPath/httpMethod), ready for a future, separate checkpoint to
// consume, but nothing in THIS package calls restexec or wires a
// dynserver.Server today.
func Build(doc *Document, providerName string, versionQualifier string) (map[string]*BuiltResource, []Note, error) {
	resources, notes, err := Discover(doc, providerName, versionQualifier)
	if err != nil {
		return nil, notes, err
	}

	out := make(map[string]*BuiltResource, len(resources))
	for _, res := range resources {
		tr := uschema.NewTranslator()

		var createAttrs []*tfprotov6.SchemaAttribute
		var signals map[string]*uschema.FieldSignal
		if reqSchema := resolveRef(res.createMethod.Request, doc); reqSchema != nil {
			createAttrs = tr.BuildTopLevel(reqSchema, res.TypeName+".create")
			signals = uschema.MergeSignalMaps(signals, uschema.CollectSignals(reqSchema))
		}

		var readAttrs []*tfprotov6.SchemaAttribute
		if respSchema := resolveRef(res.ReadMethod.Response, doc); respSchema != nil {
			readAttrs = tr.BuildTopLevel(respSchema, res.TypeName+".read")
			signals = uschema.MergeSignalMaps(signals, uschema.CollectSignals(respSchema))
		}

		merged := uschema.MergeResourceAttributes(createAttrs, readAttrs)
		if len(merged) == 0 {
			notes = append(notes, Note{Path: res.TypeName, Detail: "neither the create request body nor the read response yielded any attributes -- skipped, no usable schema"})
			continue
		}

		for _, n := range tr.Notes {
			notes = append(notes, Note{Path: res.TypeName + "." + n.Path, Detail: n.Detail})
		}

		block := &tfprotov6.SchemaBlock{Version: 1, Attributes: merged}
		schema := &tfprotov6.Schema{Version: 1, Block: block}

		out[res.TypeName] = &BuiltResource{
			Resource: res,
			Schema:   schema,
			Signals:  signals,
		}
	}

	return out, notes, nil
}

// BuiltResource is Build's own real per-resource result -- the real,
// translated schema plus the original Resource (real path/method
// strings a future REST-execution checkpoint needs) and this resource's
// own real, combined enum/constraint signal tree (identical real shape
// internal/dynserver.ResourceType.Signals already carries for the
// OpenAPI source, so ubiquex's own --dump-signals consumer needs no
// per-source special-casing).
type BuiltResource struct {
	Resource Resource
	Schema   *tfprotov6.Schema
	Signals  map[string]*uschema.FieldSignal
}

// resolveRef resolves ref against doc.Schemas into a real
// *openapi3.Schema tree, ready for internal/schema.Translator to
// consume unchanged -- nil for an unresolvable or absent ref (a real,
// honest "nothing to translate" case some real GCP methods have, e.g. a
// request-body-less action-style POST; Translator's own BuildTopLevel
// already handles a nil schema by returning no attributes, matching
// resourcemap's own RequestBodySchema convention).
func resolveRef(ref *rawRef, doc *Document) *openapi3.Schema {
	if ref == nil || ref.Ref == "" {
		return nil
	}
	return convertSchema(&rawSchema{Ref: ref.Ref}, doc.Schemas, map[string]bool{})
}

// convertSchema converts one real Discovery-Document schema node into a
// real, already-resolved *openapi3.Schema -- $ref resolved directly
// against all (a real, single-document, flat resolution; Discovery
// Documents, unlike Azure's own multi-file OpenAPI specs, never
// reference an external document), with a real cycle guard (active,
// keyed by ref name) matching internal/schema.Translator's own real
// object-identity cycle guard in spirit -- a real cycle here becomes a
// real, honest DynamicPseudoType downstream (buildType's own existing
// "no concrete type" fallback), never an infinite loop.
func convertSchema(raw *rawSchema, all map[string]*rawSchema, active map[string]bool) *openapi3.Schema {
	if raw == nil {
		return openapi3.NewSchema()
	}
	if raw.Ref != "" {
		if active[raw.Ref] {
			return openapi3.NewSchema()
		}
		target, ok := all[raw.Ref]
		if !ok {
			return openapi3.NewSchema()
		}
		active[raw.Ref] = true
		resolved := convertSchema(target, all, active)
		delete(active, raw.Ref)
		return resolved
	}

	var s *openapi3.Schema
	switch raw.Type {
	case "object":
		s = openapi3.NewObjectSchema()
		for _, name := range sortedSchemaKeys(raw.Properties) {
			child := convertSchema(raw.Properties[name], all, active)
			s.WithPropertyRef(name, openapi3.NewSchemaRef("", child))
		}
		if raw.AdditionalProperties != nil {
			s.WithAdditionalProperties(convertSchema(raw.AdditionalProperties, all, active))
		}
	case "array":
		s = openapi3.NewArraySchema()
		s.Items = openapi3.NewSchemaRef("", convertSchema(raw.Items, all, active))
	case "string":
		s = openapi3.NewStringSchema()
		for _, e := range raw.Enum {
			s.Enum = append(s.Enum, e)
		}
	case "integer", "number":
		s = openapi3.NewFloat64Schema()
	case "boolean":
		s = openapi3.NewBoolSchema()
	default:
		// Real, honest fallback -- a Discovery Document schema with no
		// "type" at all (real, legal, e.g. "any") or a type this
		// package has no real, confirmed case for yet (not invented
		// speculatively; extend when a real, live document is found
		// that needs it).
		s = openapi3.NewSchema()
	}
	s.Description = raw.Description
	s.ReadOnly = raw.ReadOnly
	return s
}

func sortedSchemaKeys(m map[string]*rawSchema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
