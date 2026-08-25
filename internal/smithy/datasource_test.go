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
	for _, c := range candidates {
		byNoun[c.Noun] = c.OperationID
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
