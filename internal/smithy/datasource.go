// Data-source discovery for Smithy-sourced services -- UBI-186's own
// inverse of resourcemap.go's Discover: instead of starting from a real
// create-verb operation and finding its read counterpart, this walks
// every real read-verb-prefixed operation (Get/Describe/List -- see
// readVerbs) and returns the ones Discover does NOT already consume as a
// resource's own ReadOperationID. Mirrors UBI-181's own definition of a
// data-source candidate for the OpenAPI/Discovery-Doc schema sources
// (operations a schema source declares with no matching create verb),
// applied to Smithy's own real verb-prefixed naming convention instead of
// a path/response-schema pairing.
package smithy

import "sort"

// DataSourceCandidate is one real, unclaimed read-shaped Smithy
// operation -- discovery only, the identical "prove the candidate set is
// real and correct first" scope Checkpoint 1's own Resource/Build split
// already established for resources (schema translation and naming
// resolution are real, separate, later steps, not done here).
type DataSourceCandidate struct {
	Noun        string // the operation's own noun, its read verb stripped
	OperationID string // full shapeId, e.g. "com.amazonaws.ec2#DescribeInstances"
}

// DiscoverDataSources returns svc's own real read-verb-prefixed
// operations that Discover does not already consume as a discovered
// resource's own read operation. Requires running Discover first (not
// duplicating its own create/read pairing logic) so an operation already
// serving as a resource's real read op is never double-counted as a
// separate data source -- the same "already accounted for" boundary
// UBI-181's own filter rules draw for the other five providers' own skip
// corpora.
func DiscoverDataSources(doc *Model, svc *Service) ([]DataSourceCandidate, error) {
	resources, _, err := Discover(doc, svc)
	if err != nil {
		return nil, err
	}
	claimedReads := make(map[string]bool, len(resources))
	for _, r := range resources {
		claimedReads[r.ReadOperationID] = true
	}

	opNames := make([]string, 0, len(svc.Shape.Operations))
	opByName := map[string]string{}
	for _, ref := range svc.Shape.Operations {
		name := bareName(ref.Target)
		opNames = append(opNames, name)
		opByName[name] = ref.Target
	}
	sort.Strings(opNames)

	var candidates []DataSourceCandidate
	for _, name := range opNames {
		noun, ok := stripVerb(name, readVerbs)
		if !ok || noun == "" {
			continue
		}
		full := opByName[name]
		if claimedReads[full] {
			continue
		}
		candidates = append(candidates, DataSourceCandidate{Noun: noun, OperationID: full})
	}
	return candidates, nil
}
