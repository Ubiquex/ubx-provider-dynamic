// Package dsfilter implements UBI-181's own five real data-source
// candidate exclusion rules -- watch paths, operation-status shapes,
// execution/event records, computed non-stored values, and high-volume
// reference duplication. Before this package existed, these five rules
// existed only as numbers in that Linear ticket (259/1/73/64/472,
// quoted by UBI-186 as "UBI-181's five-rule filter over the full
// corpus") -- no committed code anywhere in this repo, ubiquex, or
// ubiquex-docs implemented them, confirmed by a direct grep sweep
// across all three before writing this. UBI-186's own real
// DiscoverDataSources widening (smithy/datasource.go,
// discoverydoc/datasource.go, resourcemap/datasource.go) walked past
// them without reconciling against the ticket's own numbers, which is
// exactly how operation-poll and platform-reference-boilerplate
// candidates ended up in a real, generated corpus: a live count found
// Azure's own generated 2,129 data sources included 64
// operation/operation-status-named candidates and GCP's own 788
// included 113 operation-named and 145 generic-location-named
// candidates, none of it excluded by anything upstream of this
// package.
//
// Shared across all three real schema sources (Smithy/AWS,
// discoverydoc/GCP, resourcemap/Azure+Kubernetes+GitHub+Datadog)
// because the five concepts here are provider-agnostic even though
// each source's own real candidate shape (Smithy has no URL path at
// all; discoverydoc's path is a dotted resource-tree node; resourcemap's
// is a real REST path) differs -- Candidate is the deliberately narrow,
// source-agnostic signal each caller already has in hand, not a new
// unification of the three real candidate types.
package dsfilter

import "strings"

// Reason names which of the five rules excluded a candidate -- recorded
// in a Note at every real call site, never a silent drop, matching this
// codebase's own standing "skip, don't silently drop" discipline
// (identical in spirit to the "already claimed... skipped rather than
// disambiguated" Notes every DiscoverDataSources already emits).
type Reason string

const (
	ReasonWatchPath            Reason = "watch path -- a subscription/streaming endpoint, not a point-in-time lookup"
	ReasonOperationStatus      Reason = "operation-status shape -- async job/operation polling, not stored infrastructure data"
	ReasonExecutionOrEvent     Reason = "execution/event record -- a history/log entry, not current-state data"
	ReasonComputedValue        Reason = "computed, non-stored value -- a derived check/estimate, not a real stored lookup"
	ReasonReferenceDuplication Reason = "high-volume reference duplication -- generic platform reference data repeated near-identically across many services, not this service's own real data"
)

// Candidate is the minimal signal Excluded needs -- Noun is required
// (already snake_cased, already singularized, by the time a caller
// reaches this point in its own real discovery walk); Path,
// OperationName, and ResponseTypeName are each optional (empty string
// when a given source has nothing to offer -- Smithy has no real URL
// path, for instance) and only ever widen what a rule can catch, never
// narrow it.
type Candidate struct {
	// Noun is the candidate's own already-derived noun, e.g.
	// "operation", "target_type", "availability_zone".
	Noun string
	// Path is the candidate's own real path -- a REST path
	// ("/subscriptions/{id}/watch") for resourcemap, a dotted
	// resource-tree node ("projects.locations.operations") for
	// discoverydoc, empty for Smithy (which has no URL-path concept).
	Path string
	// OperationName is the candidate's own real, raw operation/method
	// name -- a Smithy shape's bare local name ("WatchNamespacedPod"),
	// an OpenAPI operationId, or a Discovery Document method key.
	OperationName string
	// ResponseTypeName is the candidate's own real, raw (not
	// snake_cased) response/output schema name, when known --
	// "TargetTypeListResult", "OperationStatus", empty when a source
	// has no named response type to offer.
	ResponseTypeName string
}

// Excluded reports whether c matches one of the five rules, and which.
func Excluded(c Candidate) (Reason, bool) {
	if isWatch(c) {
		return ReasonWatchPath, true
	}
	if isOperationStatus(c) {
		return ReasonOperationStatus, true
	}
	if hasToken(c.Noun, executionOrEventWords) {
		return ReasonExecutionOrEvent, true
	}
	if hasToken(c.Noun, computedValueWords) {
		return ReasonComputedValue, true
	}
	if hasToken(c.Noun, referenceDuplicationWords) {
		return ReasonReferenceDuplication, true
	}
	return "", false
}

var watchWords = []string{"watch", "watches"}

// isWatch checks all three real signals a watch/streaming endpoint can
// surface under: a literal "watch" path segment (Kubernetes' own real
// `/watch/...` convention and query-style watch collections alike), a
// Watch-prefixed operation name (Kubernetes' own real
// `watchNamespacedPod`-style operationId convention), or a bare "watch"
// noun.
func isWatch(c Candidate) bool {
	if hasPathSegment(c.Path, "watch") || hasPathSegment(c.Path, "watches") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(c.OperationName), "watch") {
		return true
	}
	return hasToken(c.Noun, watchWords)
}

var operationWords = []string{"operation", "operations", "operation_status", "long_running_operation", "async_operation"}

// isOperationStatus checks the noun (GCP/Azure's own real
// "projects.locations.operations"-shaped tree nodes, once snake_cased
// and singularized, land on exactly "operation") and, when a source
// offers one, the raw response type name (Azure's own real
// "OperationStatus"/"OperationStatusResult" response shapes) -- checked
// case-insensitively against the response type since that field is
// deliberately NOT pre-snake_cased the way Noun already is.
func isOperationStatus(c Candidate) bool {
	if hasToken(c.Noun, operationWords) {
		return true
	}
	return strings.Contains(strings.ToLower(c.ResponseTypeName), "operation")
}

var executionOrEventWords = []string{"execution", "event", "activity", "activity_log", "audit_log", "log_entry", "history"}

// computedValueWords deliberately uses "name_availability" (Azure and
// GCP's own real checkNameAvailability convention), not a bare
// "availability" -- a bare word would also match "availability_zone",
// which is genuinely rule 5's own high-volume reference duplication,
// not a computed check.
var computedValueWords = []string{"name_availability", "sku_availability", "estimate", "estimation", "quota", "usage", "cost", "validation", "capability", "capabilities", "eligibility", "compatibility"}

var referenceDuplicationWords = []string{"location", "region", "zone", "availability_zone"}

// hasToken reports whether noun equals phrase, or phrase appears in
// noun as a whole "_"-delimited component (so "chaos_availability_zone"
// matches "availability_zone", but "zonefile" does not spuriously match
// "zone") -- word-boundary matching on the already-snake_cased noun,
// not a bare substring test.
//
// Also tries noun with a trailing "s" stripped, and unchanged if it
// already has none: AWS's own real Smithy-sourced nouns are commonly
// still plural at this point (List-verb-stripped, e.g. "operations",
// "locations" -- Smithy's own real read-candidate discovery has no
// singularization step of its own, unlike discoverydoc/resourcemap's
// nouns, which are already singular by the time they reach here), and
// duplicating every phrase in both forms across all five word lists
// would be real, ongoing maintenance debt for the same real check this
// one trailing-"s" strip already covers.
func hasToken(noun string, phrases []string) bool {
	if matchesExact(noun, phrases) {
		return true
	}
	if singular := strings.TrimSuffix(noun, "s"); singular != noun {
		return matchesExact(singular, phrases)
	}
	return false
}

func matchesExact(noun string, phrases []string) bool {
	for _, phrase := range phrases {
		if noun == phrase ||
			strings.HasPrefix(noun, phrase+"_") ||
			strings.HasSuffix(noun, "_"+phrase) ||
			strings.Contains(noun, "_"+phrase+"_") {
			return true
		}
	}
	return false
}

// hasPathSegment reports whether path (either "/"-separated, a real
// REST path, or "."-separated, discoverydoc's own dotted resource-tree
// node path) contains segment as a whole, case-insensitive component.
func hasPathSegment(path, segment string) bool {
	if path == "" {
		return false
	}
	lower := strings.ToLower(path)
	segment = strings.ToLower(segment)
	for _, part := range strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '.' }) {
		part = strings.Trim(part, "{}")
		if part == segment {
			return true
		}
	}
	return false
}

// actionVerbTokens are UBI-181's own narrowest, unambiguous tokens: a
// real, hand-classified 55-item sample (drawn from this exact corpus,
// against the wider "any non-standard verb" candidate pool) confirmed
// each one is used, in practice, for an action ON an existing item, not
// a sibling collection's own separate create -- restore/undelete
// (recreating something that existed: a real Google Backup and DR
// `backups.restore`, GitHub's own `packages/restore-package-for-user`),
// import/initiate* (starting a new tracked entity: a real Azure
// `..._InitiateScan`, a real GCP `ragFiles.import`), provision (a real,
// common infra-instantiation verb). Safe to match via SamePathAction's
// own single-action-suffix form ("/id:restore"/"/id/restore"), not just
// an exact path -- unlike createFamilyTokens below, none of these
// plausibly names a genuine sibling collection's own create.
//
// "deploy" was in an earlier draft of this list and is deliberately NOT
// here: a live, full-corpus check found 494 real Azure operationIds
// containing the substring "deploy", virtually all of them the noun
// "Deployment"/"DeploymentScripts"/"DeploymentStacks" (real ARM resource
// families), not the verb -- zero genuine matches found anywhere in this
// corpus, against real, confirmed false positives (GitHub's own
// "review-pending-deployments-for-run", an approval action, not a
// create; Datadog's own "CancelFleetDeploymentV2", explicitly a
// cancellation). If real evidence for "deploy" as a verb ever surfaces,
// it needs a narrower match than a bare substring (this whole corpus's
// own "provisioningState"-style false-friend risk applies here too, not
// just deploy -- watched for, not yet found live).
//
// Deliberately NOT the ticket's own original broader list: "enable" and
// "subscribe" were both named there but are dropped here, on real
// evidence -- the same sample found them overwhelmingly used for an
// action on an ALREADY-modeled resource (GCP's own
// `secrets.versions.enable`/`.disable`, `projects.enableXpnHost`/
// `.enableXpnResource`), never a hidden create. Broadening this list
// again needs the identical real evidence this one was built from, not
// a guess.
var actionVerbTokens = []string{
	"restore", "undelete", "import", "provision", "initiate",
}

// createFamilyTokens are UBI-181's own create-shaped tokens, genuinely
// ambiguous with a sibling collection's own create when matched via a
// path SUFFIX -- a real, live-found case confirmed this: GitHub's own
// real "/orgs/{org}/repos" (POST, creating a NEW repository under an
// org) has the exact same one-extra-static-segment shape as a genuine
// action suffix, and its own operationId ("repos/create-in-org")
// contains "create" -- matching it against the "/orgs/{org}" read
// candidate would misattribute repos' own create to organization,
// exactly the wrong-resource-modeling risk this whole allowlist exists
// to avoid, not just added noise. Callers restrict these to an EXACT
// path match only (see findAllowlistedCreate, resourcemap.go) -- the
// real, confirmed case this still legitimately catches is
// `Databases_Create`, which sits on the identical path as its own GET
// (the real ARM PUT-on-same-path convention), never a suffix. "create"
// itself is included even though it is already the standard verb:
// resourcemap's own findCreate never checks verb NAMING at all, only
// response-schema or parent-collection-path match, so a real,
// live-confirmed miss (Databases_Create's async response never
// structurally matched its own read schema) needs the verb name itself
// as a third, independent signal. addorupdate is GitHub's own real
// add-or-update-shaped naming with no "create" substring at all.
var createFamilyTokens = []string{"create", "addorupdate"}

// MatchesActionVerb reports whether name (an operation name, operation
// ID, or discoverydoc method key) matches one of actionVerbTokens --
// safe against SamePathAction's suffix form, see that function's own
// doc comment.
func MatchesActionVerb(name string) bool {
	return matchesAnyToken(name, actionVerbTokens)
}

// MatchesCreateVerb reports whether name matches actionVerbTokens OR
// createFamilyTokens -- the full combined allowlist, safe wherever
// SamePathAction's ambiguous suffix case cannot arise at all: GCP's own
// discoverydoc method keys are matched by same-node key lookup, never a
// path suffix, so createFamilyTokens' own exact-path-only restriction
// is a non-issue there.
func MatchesCreateVerb(name string) bool {
	return matchesAnyToken(name, actionVerbTokens) || matchesAnyToken(name, createFamilyTokens)
}

// matchesAnyToken is the real, shared substring check both exported
// functions above use: name (an operation name, operation ID, or
// discoverydoc method key, from any of the three real schema sources),
// once separators are stripped and case is normalized to a bare
// lowercase letter run, contains one of tokens as a substring -- not a
// prefix- or suffix-only test, so GCP's own clean, single-word method
// keys ("restore", "initiateBackup") and OpenAPI's own compound
// operationIds ("SqlPoolVulnerabilityAssessmentScans_InitiateScan",
// "repos/create-or-update-file-contents") match the identical way.
// recreateFalseFriend is stripped from each real SEGMENT (see
// verbSegments) before matching "create" -- a real, live-found false
// friend (generating this exact batch's own real full-corpus run,
// UBI-181): GCP's own real "downloadRecreateInstallScript"
// (networkMonitoringProviders' monitoringPoints, whose own real methods
// are download/get/list-only, genuinely no create operation exists at
// all) and "recreateInstances" (Compute's own instanceGroupManagers, a
// real action re-imaging an EXISTING group's members, not a new group)
// both contain "create" only as a substring of "recreate" -- neither is
// a real create.
//
// Stripping "recreate" from the WHOLE cleaned name (an earlier version
// of this fix) was itself a real, live-found bug: Azure's own
// "<Resource>_<Verb>" naming convention means the underscore separating
// two genuinely unrelated words is exactly what a naive full-string
// strip destroys -- "PrivateStore_CreateOrUpdate" (a real,
// live-published operation, no "recreate" concept in it at all)
// concatenates to "...store" + "create..." across that now-deleted
// underscore, which itself spells "recreate" purely by accident (the
// real word boundary was the only thing preventing the collision).
// Segmenting first, on every real non-letter separator (verbSegments),
// keeps that boundary intact -- the false-friend strip only ever runs
// WITHIN one real, unbroken word run, never across two unrelated ones.
const recreateFalseFriend = "recreate"

// verbSegments splits name into its own real CONCEPT runs, on every
// real structural separator (underscore, slash, colon, dot) --
// deliberately NOT on hyphens or camelCase transitions, since this
// corpus's own real compound verb names are each already one
// intentional, continuous phrase this package depends on matching as a
// single unit, whether joined by camelCase ("createOrUpdate",
// "initiateBackup") or by kebab-case (GitHub's own real
// "create-or-update-file-contents", "add-or-update-repo-permissions-
// in-org" -- a live, found-in-review regression: an earlier version of
// this function DID break on hyphens, which silently split
// "add-or-update" into three separate words and stopped it matching
// the "addorupdate" token at all). Structural separators genuinely
// divide two UNRELATED concepts in this corpus's own real naming
// conventions (Azure: "<Resource>_<Verb>"; GitHub: "<scope>/<verb-
// phrase>"; GCP: "<namespace>.<method>"); a hyphen never does -- it is
// this corpus's own kebab-case equivalent of camelCase, joining one
// phrase, not separating two.
func verbSegments(name string) []string {
	var segments []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			segments = append(segments, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r == '-':
			// Kebab-case within one real compound phrase -- dropped,
			// not a segment break (see doc comment above).
		default:
			flush()
		}
	}
	flush()
	return segments
}

func matchesAnyToken(name string, tokens []string) bool {
	for _, segment := range verbSegments(name) {
		cleaned := strings.ReplaceAll(segment, recreateFalseFriend, "")
		for _, tok := range tokens {
			if strings.Contains(cleaned, tok) {
				return true
			}
		}
	}
	return false
}

// SamePathAction reports whether opPath is genuinely the same resource
// as candidatePath -- either byte-identical, or candidatePath with
// exactly one trailing static action segment appended (REST's own
// common ":action"/"/action" convention for an operation on an existing
// item, confirmed against this corpus's own real Azure App Service
// spec: "/sites/{name}/backups/{backupId}" ->
// "/sites/{name}/backups/{backupId}/restore"). Deliberately excludes
// anything that introduces a NEW path parameter or more than one extra
// static segment -- that shape is a genuinely separate, nested child
// resource, not an action on candidatePath's own resource, confirmed
// live against this exact corpus's own real misattribution case: Azure's
// SqlPoolSensitivityLabels_CreateOrUpdate lives at
// ".../columns/{columnName}/sensitivityLabels/{sensitivityLabelSource}"
// -- two extra segments AND a new path parameter beyond
// ".../columns/{columnName}" -- attributing that create operation to
// the "column" candidate would misattribute a real, separate resource's
// create to the wrong noun entirely, not just add noise.
func SamePathAction(candidatePath, opPath string) bool {
	if candidatePath == opPath {
		return true
	}
	if !strings.HasPrefix(opPath, candidatePath) {
		return false
	}
	suffix := strings.TrimPrefix(opPath[len(candidatePath):], ":")
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" || strings.ContainsAny(suffix, "/{") {
		return false
	}
	return true
}
