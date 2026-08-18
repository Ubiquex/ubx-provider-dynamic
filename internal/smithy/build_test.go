package smithy

import "testing"

// TestBuild_RealSQSEndToEnd is Checkpoint 1's own complete proof in one
// test: load a real Smithy model, discover a real resource, translate its
// schema (reusing Phase 1's translator unchanged), and resolve its real
// HashiCorp-compatible name -- the full pipeline, against real data, with
// no step mocked or simulated.
func TestBuild_RealSQSEndToEnd(t *testing.T) {
	m := loadFixture(t, "testdata/sqs.json")
	built, notes, err := Build(m, "aws", DefaultKnownNames())
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}

	queue, ok := built["aws_sqs_queue"]
	if !ok {
		names := make([]string, 0, len(built))
		for n := range built {
			names = append(names, n)
		}
		t.Fatalf("expected aws_sqs_queue in the built resources, got %v", names)
	}
	if queue.NameStrategy != StrategyPrefixed {
		t.Fatalf("expected StrategyPrefixed, got %q", queue.NameStrategy)
	}
	if queue.Schema == nil || queue.Schema.Block == nil || len(queue.Schema.Block.Attributes) == 0 {
		t.Fatalf("expected a real, non-empty translated schema, got %+v", queue.Schema)
	}

	byName := map[string]bool{}
	for _, a := range queue.Schema.Block.Attributes {
		byName[a.Name] = true
	}
	if !byName["queue_name"] {
		t.Fatalf("expected a queue_name attribute, got %v", byName)
	}
	if !byName["owner"] && !byName["queue_arn"] {
		// Not a hard requirement -- just documents that real, read-only
		// (GetQueueAttributes-only) fields are expected to appear too.
		t.Logf("attributes: %v", byName)
	}
}

func TestDefaultKnownNames_IsRealAndNonEmpty(t *testing.T) {
	known := DefaultKnownNames()
	if len(known) < 1000 {
		t.Fatalf("expected the real, embedded snapshot to carry over 1000 names, got %d", len(known))
	}
	if !known["aws_sqs_queue"] || !known["aws_instance"] || !known["aws_s3_bucket"] {
		t.Fatalf("expected well-known real names to be present, got a snapshot of %d names missing them", len(known))
	}
}
