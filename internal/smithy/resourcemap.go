// Resource mapping for Smithy-sourced services -- a genuinely different
// heuristic from internal/resourcemap's own OpenAPI path/response-schema
// pairing (Phase 1), because Smithy's real, current AWS models carry no
// Smithy `resource` shapes in practice: confirmed directly this session
// against five real, structurally different services (SQS, S3, DynamoDB,
// EC2, Lambda) -- despite the Smithy spec itself defining a real `resource`
// shape kind with CRUD operation bindings, none of AWS's own published
// models actually use it. What every real AWS service DOES use, uniformly,
// is a verb-prefixed operation-naming convention (CreateQueue/
// GetQueueAttributes/SetQueueAttributes/DeleteQueue) -- this package groups
// CRUD resources from that naming convention instead, the AWS-wide
// equivalent of Phase 1's own path-based grouping.
package smithy

import (
	"sort"
	"strings"
)

// Resource is one discovered CRUD-shaped AWS resource, real operation
// shapeIds only -- no execution machinery here (Checkpoint 2's own scope,
// once real per-protocol wire executors exist); Checkpoint 1's job is
// proving discovery, schema translation, and naming alone.
type Resource struct {
	Noun string // the real, shared operation-name noun (e.g. "Queue")

	CreateOperationID string
	ReadOperationID   string
	UpdateOperationID string // empty if no real update-shaped operation was found
	DeleteOperationID string // empty if no real delete-shaped operation was found
}

// ResourceNote mirrors resourcemap.Note's own role (Phase 1) for this
// package -- named distinctly from toschema.go's own Note (a real
// collision otherwise: both types belong to the same real, single
// concern -- "translation/mapping decision worth surfacing" -- for two
// different layers of this package).
type ResourceNote struct {
	OperationID string
	Detail      string
}

// createVerbs includes "Run" alongside the obvious "Create" for one real,
// well-documented, cross-service reason, not an EC2-specific special
// case: AWS's own real API convention for a "launch" style create --
// confirmed live in EC2's own model (RunInstances, RunScheduledInstances,
// both real batch-create operations for what Terraform's own
// per-resource model treats as a single resource) -- is a genuine, if
// narrower, AWS-wide verb, not invented for this one service.
var createVerbs = []string{"Create", "Run"}
var readVerbs = []string{"Get", "Describe"}
var updateVerbs = []string{"Update", "Modify", "Put", "Set"}
var deleteVerbs = []string{"Delete"}

// Discover groups svc's own real operations into CRUD resources by their
// shared verb-prefixed naming convention -- see the package doc comment.
// A resource requires at least a Create and a Read match to be reported,
// the identical bar Phase 1's own OpenAPI resourcemap uses, for the
// identical reason (a create-less/read-less operation set is a data
// source concern, not a manageable resource).
func Discover(doc *Model, svc *Service) ([]Resource, []ResourceNote, error) {
	opNames := make([]string, 0, len(svc.Shape.Operations))
	opByName := map[string]string{} // bare name -> full shapeId
	for _, ref := range svc.Shape.Operations {
		name := bareName(ref.Target)
		opNames = append(opNames, name)
		opByName[name] = ref.Target
	}
	sort.Strings(opNames)

	var notes []ResourceNote
	var resources []Resource
	claimed := map[string]bool{} // noun -> already reported

	for _, name := range opNames {
		rawNoun, ok := stripVerb(name, createVerbs)
		if !ok || rawNoun == "" {
			continue
		}

		// A batch/plural create verb (RunInstances -- see createVerbs' own
		// doc comment) leaves a plural remainder ("Instances"); real
		// per-resource read/delete operations almost always name the
		// SINGULAR resource, and HashiCorp's own real Terraform resource
		// naming is essentially always singular too ("aws_instance," never
		// "aws_instances") even when a real "DescribeInstances"-shaped
		// plural read op also exists and would otherwise match just as
		// well via bestMatch's own prefix bucket. Try the naive singular
		// (trailing "s" stripped) FIRST when the raw noun ends in "s," only
		// falling back to the literal plural form if no singular read
		// match exists at all -- the identical "approximate, not a real
		// inflection engine" honesty internal/resourcemap.go's own
		// deriveNoun already applies for OpenAPI's own equivalent fallback.
		noun := rawNoun
		if strings.HasSuffix(noun, "s") {
			if singular := strings.TrimSuffix(noun, "s"); !claimed[singular] {
				if _, ok := bestMatch(opNames, readVerbs, singular); ok {
					noun = singular
				}
			}
		}
		if claimed[noun] {
			continue
		}
		readName, readOK := bestMatch(opNames, readVerbs, noun)
		if !readOK {
			notes = append(notes, ResourceNote{OperationID: opByName[name], Detail: "no matching Get/Describe-shaped read operation found for noun " + noun + " -- read-only or create-only surface, not modeled as a resource"})
			continue
		}
		claimed[noun] = true

		res := Resource{
			Noun:              noun,
			CreateOperationID: opByName[name],
			ReadOperationID:   opByName[readName],
		}
		if updateName, ok := bestMatch(opNames, updateVerbs, noun); ok {
			res.UpdateOperationID = opByName[updateName]
		} else {
			notes = append(notes, ResourceNote{OperationID: opByName[name], Detail: "no Update/Modify/Put/Set-shaped operation found for noun " + noun + " -- modeled as create/delete-only, no in-place update"})
		}
		if deleteName, ok := exactMatch(opNames, deleteVerbs, noun); ok {
			res.DeleteOperationID = opByName[deleteName]
		} else {
			notes = append(notes, ResourceNote{OperationID: opByName[name], Detail: "no Delete-shaped operation found for noun " + noun + " -- modeled without a real destroy operation"})
		}
		resources = append(resources, res)
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].Noun < resources[j].Noun })
	return resources, notes, nil
}

// stripVerb removes the first verb in verbs that prefixes name, reporting
// the remaining noun. Case-sensitive, exact prefix -- real AWS operation
// names are PascalCase throughout, confirmed across every model this
// session read.
func stripVerb(name string, verbs []string) (noun string, ok bool) {
	for _, v := range verbs {
		if strings.HasPrefix(name, v) {
			return strings.TrimPrefix(name, v), true
		}
	}
	return "", false
}

// bestMatch finds, among candidates, the operation whose own name is
// verb-prefixed (one of verbs) followed by noun, or by noun-as-a-prefix
// of a longer remainder (AWS's own real "GetQueueAttributes"/
// "SetQueueAttributes" convention -- the read/update operation is very
// often named "<Verb><Noun>Attributes", not bare "<Verb><Noun>").
// Deterministic preference order, honestly heuristic, not a guaranteed
// correct answer for every real service: (1) an exact "<Verb><Noun>"
// match, (2) a "<Verb><Noun>Attributes" match, (3) the shortest
// remaining-prefix match, (4) alphabetically first -- ties broken the
// same way every time, never left to map-iteration order.
func bestMatch(candidates []string, verbs []string, noun string) (string, bool) {
	var exact, attrSuffix, prefix []string
	for _, name := range candidates {
		rest, ok := stripVerb(name, verbs)
		if !ok {
			continue
		}
		switch {
		case rest == noun:
			exact = append(exact, name)
		case rest == noun+"Attributes":
			attrSuffix = append(attrSuffix, name)
		case strings.HasPrefix(rest, noun):
			prefix = append(prefix, name)
		}
	}
	for _, bucket := range [][]string{exact, attrSuffix, prefix} {
		if len(bucket) == 0 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			return len(bucket[i]) < len(bucket[j]) || (len(bucket[i]) == len(bucket[j]) && bucket[i] < bucket[j])
		})
		return bucket[0], true
	}
	return "", false
}

// exactMatch is bestMatch's own narrower sibling for Delete, where real
// AWS operation names essentially never carry a suffix beyond the bare
// noun (DeleteQueue, DeleteBucket, DeleteFunction -- confirmed across
// every model this session read, zero counterexamples found).
func exactMatch(candidates []string, verbs []string, noun string) (string, bool) {
	for _, name := range candidates {
		if rest, ok := stripVerb(name, verbs); ok && rest == noun {
			return name, true
		}
	}
	return "", false
}

// bareName strips a fully-qualified Smithy shapeId's own namespace prefix
// ("com.amazonaws.sqs#CreateQueue" -> "CreateQueue").
func bareName(shapeID string) string {
	if i := strings.IndexByte(shapeID, '#'); i >= 0 {
		return shapeID[i+1:]
	}
	return shapeID
}
