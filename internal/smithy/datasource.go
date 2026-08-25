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
// operation -- discovery, plus the real namespace UBI-98 established
// data sources should mirror. Schema translation and local-name casing
// remain real, separate, later steps (the identical "prove the
// candidate set is real and correct first" scope Checkpoint 1's own
// Resource/Build split already established for resources) -- but
// Namespace itself is populated here, not deferred, because it's the
// same real signal a resource's own namespace already uses (see
// naming.go's own ServiceTraits.EndpointPrefix doc comment) and main.go's
// own --dump-namespaces already exposes it identically for both.
type DataSourceCandidate struct {
	Noun        string // the operation's own noun, its read verb stripped
	OperationID string // full shapeId, e.g. "com.amazonaws.ec2#DescribeInstances"

	// Namespace is svc's own real endpointPrefix -- UBI-98's own real
	// fix uses CloudFormation's real namespace field to name a
	// RESOURCE (aws.ec2.Instance, not the old aws.instance.Instance);
	// a Smithy-sourced DATA SOURCE has no CFN counterpart to draw that
	// from, so it uses Smithy's own real endpointPrefix instead --
	// confirmed live this session to agree with CFN's own real
	// namespace field for 178 of 181 real overlapping resources
	// (98.3%), the 3 exceptions being real, already-understood cases
	// (a human-product-name-vs-wire-slug difference for Elasticsearch,
	// and two genuine multi-service collisions CFN and Smithy each
	// resolve differently on their own). This is also what makes the
	// EC2/SSO aws_instance collision, the 3-way aws_route collision,
	// and the EC2/OpenSearchServerless aws_vpc_endpoint collision moot
	// for data sources specifically: aws.data.ec2.Instance and
	// aws.data.sso.Instance are distinct namespaced identifiers by
	// construction, never forced through a single shared flat name the
	// way the pre-UBI-98 scheme was.
	//
	// Real, checked, not assumed: endpointPrefix is blank on 93 of 430
	// real AWS Smithy service models (confirmed live -- AccessAnalyzer's
	// own real aws.api#service trait carries arnNamespace and
	// cloudTrailEventSource but no endpointPrefix at all). Falls back to
	// svc.Traits.ArnNamespace, which covers 92 of those 93 (naming.go's
	// own doc comment already establishes ArnNamespace and
	// EndpointPrefix as the same real string in the common case, SQS
	// confirmed live: both "sqs"). Only when BOTH are empty does
	// Namespace stay empty -- a real, honest, still-open gap for that
	// one remaining service, not silently papered over.
	Namespace string
}

// ServiceNamespace picks svc's own best available real service identity
// -- EndpointPrefix first, ArnNamespace as a real fallback (see
// DataSourceCandidate.Namespace's own doc comment for why both exist
// and how often each is populated). Exported so main.go's own
// --dump-namespaces uses the identical real fallback for a Smithy
// RESOURCE's own namespace, not a second, divergent implementation.
func ServiceNamespace(svc *Service) string {
	if svc.Traits.EndpointPrefix != "" {
		return svc.Traits.EndpointPrefix
	}
	return svc.Traits.ArnNamespace
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
		candidates = append(candidates, DataSourceCandidate{Noun: noun, OperationID: full, Namespace: ServiceNamespace(svc)})
	}
	return candidates, nil
}
