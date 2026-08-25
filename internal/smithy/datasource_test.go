package smithy

import (
	"encoding/json"
	"testing"
)

// TestDiscoverDataSources_RealSQSModel proves the inverse-discovery pass
// against SQS's own real, full operation list (23 operations): Queue's
// own real resource claims CreateQueue+GetQueueAttributes (+
// SetQueueAttributes/DeleteQueue), so GetQueueAttributes must NOT
// reappear as a data-source candidate, while every other real
// Get/Describe/List-prefixed operation should.
func TestDiscoverDataSources_RealSQSModel(t *testing.T) {
	m := loadFixture(t, "testdata/sqs.json")
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoverDataSources(m, svc)
	if err != nil {
		t.Fatal(err)
	}

	byNoun := map[string]string{}
	nsByNoun := map[string]string{}
	for _, c := range candidates {
		byNoun[c.Noun] = c.OperationID
		nsByNoun[c.Noun] = c.Namespace
	}

	want := map[string]string{
		"QueueUrl":               "com.amazonaws.sqs#GetQueueUrl",
		"DeadLetterSourceQueues": "com.amazonaws.sqs#ListDeadLetterSourceQueues",
		"MessageMoveTasks":       "com.amazonaws.sqs#ListMessageMoveTasks",
		"QueueTags":              "com.amazonaws.sqs#ListQueueTags",
		"Queues":                 "com.amazonaws.sqs#ListQueues",
	}
	for noun, opID := range want {
		if got := byNoun[noun]; got != opID {
			t.Errorf("noun %s: got OperationID %q, want %q", noun, got, opID)
		}
		// UBI-98: every real data-source candidate carries SQS's own
		// real endpointPrefix ("sqs"), mirroring what a Resource
		// generated from the same service would use for its own
		// namespace -- confirmed directly against the real, checked-in
		// SQS fixture's own aws.api#service trait.
		if got := nsByNoun[noun]; got != "sqs" {
			t.Errorf("noun %s: got Namespace %q, want %q", noun, got, "sqs")
		}
	}
	if _, ok := byNoun["QueueAttributes"]; ok {
		t.Errorf("GetQueueAttributes is Queue's own real ReadOperationID -- must not also appear as a data-source candidate, got one for noun QueueAttributes")
	}
	if len(candidates) != len(want) {
		t.Errorf("got %d candidates, want %d: %+v", len(candidates), len(want), candidates)
	}
}

func TestDiscoverDataSources_EmptyWhenEveryReadIsClaimed(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#Svc": {
			Type:       "service",
			Operations: []ShapeRef{{Target: "x#CreateThing"}, {Target: "x#GetThing"}},
			Traits:     map[string]json.RawMessage{"aws.protocols#awsJson1_0": json.RawMessage(`{}`)},
		},
		"x#CreateThing": {Type: "operation"},
		"x#GetThing":    {Type: "operation"},
	}}
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoverDataSources(m, svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected zero candidates (Thing's own GetThing is claimed by its Create pairing), got %+v", candidates)
	}
}

// TestDiscoverDataSources_ReturnsUnclaimedAlongsideClaimed proves the
// pass discriminates correctly rather than returning empty by accident:
// Thing's own GetThing is claimed by CreateThing, but ListWidgets has no
// create counterpart at all and must survive as a real candidate.
func TestDiscoverDataSources_ReturnsUnclaimedAlongsideClaimed(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#Svc": {
			Type: "service",
			Operations: []ShapeRef{
				{Target: "x#CreateThing"}, {Target: "x#GetThing"}, {Target: "x#ListWidgets"},
			},
			Traits: map[string]json.RawMessage{"aws.protocols#awsJson1_0": json.RawMessage(`{}`)},
		},
		"x#CreateThing": {Type: "operation"},
		"x#GetThing":    {Type: "operation"},
		"x#ListWidgets": {Type: "operation"},
	}}
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoverDataSources(m, svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Noun != "Widgets" || candidates[0].OperationID != "x#ListWidgets" {
		t.Fatalf("expected exactly one candidate (Widgets/x#ListWidgets), got %+v", candidates)
	}
}

// TestDiscoverDataSources_NamespaceDisambiguatesRealCollision is UBI-186's
// own real proof for the aws_instance (EC2/SSO), aws_route (3 real
// services), aws_vpc_endpoint (EC2/OpenSearchServerless) collisions
// found this session: two distinct services whose own real Noun happens
// to be the identical bare word ("Instance") produce candidates that
// were, pre-UBI-98, forced through the SAME shared flat name -- here,
// each candidate carries its OWN real service's endpointPrefix, so
// "aws.data.ec2.Instance" and "aws.data.sso.Instance" are distinct
// namespaced identifiers by construction, never colliding at all.
func TestDiscoverDataSources_NamespaceDisambiguatesRealCollision(t *testing.T) {
	newSvcModel := func(svcID, endpointPrefix string) (*Model, *Service) {
		m := &Model{Shapes: map[string]Shape{
			svcID: {
				Type:       "service",
				Operations: []ShapeRef{{Target: svcID[:len(svcID)-len("#Svc")] + "#GetInstance"}},
				Traits: map[string]json.RawMessage{
					"aws.protocols#awsJson1_0": json.RawMessage(`{}`),
					"aws.api#service":          json.RawMessage(`{"endpointPrefix":"` + endpointPrefix + `"}`),
				},
			},
			svcID[:len(svcID)-len("#Svc")] + "#GetInstance": {Type: "operation"},
		}}
		svc, err := FindService(m)
		if err != nil {
			t.Fatalf("FindService(%s): %v", svcID, err)
		}
		return m, svc
	}

	ec2Model, ec2Svc := newSvcModel("com.amazonaws.ec2#Svc", "ec2")
	ssoModel, ssoSvc := newSvcModel("com.amazonaws.sso#Svc", "sso")

	ec2Candidates, err := DiscoverDataSources(ec2Model, ec2Svc)
	if err != nil {
		t.Fatal(err)
	}
	ssoCandidates, err := DiscoverDataSources(ssoModel, ssoSvc)
	if err != nil {
		t.Fatal(err)
	}

	if len(ec2Candidates) != 1 || ec2Candidates[0].Noun != "Instance" || ec2Candidates[0].Namespace != "ec2" {
		t.Fatalf("expected one ec2 Instance candidate with Namespace=ec2, got %+v", ec2Candidates)
	}
	if len(ssoCandidates) != 1 || ssoCandidates[0].Noun != "Instance" || ssoCandidates[0].Namespace != "sso" {
		t.Fatalf("expected one sso Instance candidate with Namespace=sso, got %+v", ssoCandidates)
	}
	if ec2Candidates[0].Namespace == ssoCandidates[0].Namespace {
		t.Fatalf("expected distinct namespaces for the same real Noun from two different real services, got the same one twice: %q", ec2Candidates[0].Namespace)
	}
}

// TestServiceNamespace_FallsBackToArnNamespace is UBI-186's own real
// finding: 93 of 430 real AWS Smithy service models (confirmed live,
// AccessAnalyzer among them) carry no endpointPrefix trait value at
// all. ArnNamespace covers 92 of those 93 -- naming.go's own doc
// comment already establishes it as the same real string as
// endpointPrefix in the common case (SQS: both "sqs").
func TestServiceNamespace_FallsBackToArnNamespace(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#Svc": {
			Type:       "service",
			Operations: []ShapeRef{{Target: "x#GetThing"}},
			Traits: map[string]json.RawMessage{
				"aws.protocols#awsJson1_0": json.RawMessage(`{}`),
				"aws.api#service":          json.RawMessage(`{"arnNamespace":"access-analyzer"}`),
			},
		},
		"x#GetThing": {Type: "operation"},
	}}
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := ServiceNamespace(svc); got != "access-analyzer" {
		t.Fatalf("ServiceNamespace with no endpointPrefix = %q, want the real arnNamespace fallback %q", got, "access-analyzer")
	}
}

// TestServiceNamespace_EmptyWhenBothMissing is the real, honest edge
// case: when neither trait is populated, Namespace stays empty rather
// than inventing a value -- confirmed live to affect exactly 1 of 430
// real AWS services.
func TestServiceNamespace_EmptyWhenBothMissing(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#Svc": {
			Type:       "service",
			Operations: []ShapeRef{{Target: "x#GetThing"}},
			Traits:     map[string]json.RawMessage{"aws.protocols#awsJson1_0": json.RawMessage(`{}`)},
		},
		"x#GetThing": {Type: "operation"},
	}}
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := ServiceNamespace(svc); got != "" {
		t.Fatalf("ServiceNamespace with neither trait set = %q, want empty", got)
	}
}
