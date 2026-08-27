package snapshot

import (
	"reflect"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

func TestExpandMemberModes_DefaultOpenAPI_ProducesBoth(t *testing.T) {
	modes, names := ExpandMemberModes("kubernetes", config.Provider{SchemaSource: config.SchemaSourceOpenAPI})
	if !reflect.DeepEqual(modes, []Mode{ModeResource, ModeDataSource}) {
		t.Fatalf("modes = %v, want [resource data_source]", modes)
	}
	if !reflect.DeepEqual(names, []string{"kubernetes", "kubernetes_ds"}) {
		t.Fatalf("names = %v, want [kubernetes kubernetes_ds]", names)
	}
}

func TestExpandMemberModes_DefaultSmithy_ProducesBoth(t *testing.T) {
	modes, names := ExpandMemberModes("aws_ec2", config.Provider{SchemaSource: config.SchemaSourceSmithy})
	if !reflect.DeepEqual(modes, []Mode{ModeResource, ModeDataSource}) {
		t.Fatalf("modes = %v, want [resource data_source]", modes)
	}
	if !reflect.DeepEqual(names, []string{"aws_ec2", "aws_ec2_ds"}) {
		t.Fatalf("names = %v, want [aws_ec2 aws_ec2_ds]", names)
	}
}

func TestExpandMemberModes_DefaultDiscoveryDoc_ProducesBoth(t *testing.T) {
	modes, names := ExpandMemberModes("google_compute", config.Provider{SchemaSource: config.SchemaSourceDiscoveryDoc})
	if !reflect.DeepEqual(modes, []Mode{ModeResource, ModeDataSource}) {
		t.Fatalf("modes = %v, want [resource data_source]", modes)
	}
	if !reflect.DeepEqual(names, []string{"google_compute", "google_compute_ds"}) {
		t.Fatalf("names = %v, want [google_compute google_compute_ds]", names)
	}
}

// TestExpandMemberModes_DataSourcesTrue_RealAWSDataOnlyShape proves the
// real, live AWS case (429 real Smithy data-source-only entries, no
// resource-mode sibling at all -- AWS's resource surface comes from
// CloudFormation exclusively) keeps its exact prior meaning: ONE
// data-source-mode member, keyed by the entry's own name, unaffected by
// the collapse.
func TestExpandMemberModes_DataSourcesTrue_RealAWSDataOnlyShape(t *testing.T) {
	modes, names := ExpandMemberModes("aws_data_ec2", config.Provider{SchemaSource: config.SchemaSourceSmithy, DataSources: true})
	if !reflect.DeepEqual(modes, []Mode{ModeDataSource}) {
		t.Fatalf("modes = %v, want [data_source]", modes)
	}
	if !reflect.DeepEqual(names, []string{"aws_data_ec2"}) {
		t.Fatalf("names = %v, want [aws_data_ec2]", names)
	}
}

// TestExpandMemberModes_CloudFormation_AlwaysResourcesOnly proves
// CloudFormation never gets an implied data-source member even under
// the new "default means both" rule -- it has no real data-source
// concept at all (validate() already refuses data_sources = true on a
// CloudFormation entry, so this is the unconditional, structural case,
// not a fallback for a config mistake).
func TestExpandMemberModes_CloudFormation_AlwaysResourcesOnly(t *testing.T) {
	modes, names := ExpandMemberModes("aws", config.Provider{SchemaSource: config.SchemaSourceCloudFormation})
	if !reflect.DeepEqual(modes, []Mode{ModeResource}) {
		t.Fatalf("modes = %v, want [resource]", modes)
	}
	if !reflect.DeepEqual(names, []string{"aws"}) {
		t.Fatalf("names = %v, want [aws]", names)
	}
}
