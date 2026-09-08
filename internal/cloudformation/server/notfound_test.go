package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/ccapi"
)

// A resource deleted out of band must read as gone, not as an error.
//
// ReadResource has always had the right branch for this, returning an
// empty response when readFromAPI yields no value, which is every
// provider's convention for "this resource no longer exists". It was
// unreachable in production: the classification used restexec.IsNotFound,
// which matches HTTP 404, while CCAPI answers a missing resource with
// HTTP 400 and a "__type" of ResourceNotFoundException. So a real
// deletion surfaced as a hard provider error, `ubx scan` failed instead
// of recording the disappearance, and drift detection could not see it
// at all. Found after deleting a real SQS queue out of band and being
// unable to reconcile the ledger through any ubx command.

func readDeletedResource(t *testing.T) *tfprotov6.ReadResourceResponse {
	t.Helper()
	fake := newFakeCCAPIServer()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	rt := testResource(t)
	s := New("aws", map[string]*cloudformation.BuiltResource{"aws_sqs_queue": rt}, ccapi.NewClient(srv.URL, nil))
	s.PollInterval = time.Millisecond
	s.PollTimeout = time.Second

	// Nothing was ever created in the fake, so this identifier is absent,
	// which is the same answer AWS gives for a resource deleted behind
	// ubx's back.
	state := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "ubx-demo-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, "https://sqs.example.com/123456789012/ubx-demo-queue"),
		"arn":            tftypes.NewValue(tftypes.String, "arn:aws:sqs:::ubx-demo-queue"),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, nil),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, state)
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	resp, err := s.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName:     "aws_sqs_queue",
		CurrentState: &dv,
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	return resp
}

func TestReadResource_DeletedOutOfBand_ReadsAsGone(t *testing.T) {
	resp := readDeletedResource(t)

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("a deleted resource surfaced as a provider error instead of reading as gone: %+v", resp.Diagnostics)
	}
	if resp.NewState != nil {
		t.Fatalf("expected no state for a resource that no longer exists, got: %+v", resp.NewState)
	}
}

// The destroy side of the same classification. ubx ship is idempotent by
// contract, so destroying something already gone has to converge rather
// than wedge the proposal, whether that is a retry after a delete that
// already succeeded or a race with an out-of-band deletion.
func TestApplyDestroy_AlreadyGone_Succeeds(t *testing.T) {
	fake := newFakeCCAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	rt := testResource(t)
	s := New("aws", map[string]*cloudformation.BuiltResource{"aws_sqs_queue": rt}, ccapi.NewClient(srv.URL, nil))
	s.PollInterval = time.Millisecond
	s.PollTimeout = time.Second

	prior := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "ubx-demo-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, "https://sqs.example.com/123456789012/ubx-demo-queue"),
		"arn":            tftypes.NewValue(tftypes.String, "arn:aws:sqs:::ubx-demo-queue"),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, nil),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})
	priorDV, err := tfprotov6.NewDynamicValue(rt.ObjectType, prior)
	if err != nil {
		t.Fatalf("NewDynamicValue(prior): %v", err)
	}
	nullDV, err := tfprotov6.NewDynamicValue(rt.ObjectType, tftypes.NewValue(rt.ObjectType, nil))
	if err != nil {
		t.Fatalf("NewDynamicValue(null): %v", err)
	}

	// Through a real PlanResourceChange first: this provider refuses a
	// destroy that arrives without PlannedPrivate, deliberately, so a test
	// that fabricated the marker would be testing a shape ubx never sends.
	planResp, err := s.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "aws_sqs_queue",
		PriorState:       &priorDV,
		ProposedNewState: &nullDV,
		Config:           &nullDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}

	resp, err := s.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "aws_sqs_queue",
		PriorState:     &priorDV,
		PlannedState:   planResp.PlannedState,
		PlannedPrivate: planResp.PlannedPrivate,
		Config:         &nullDV,
	})
	if err != nil {
		t.Fatalf("ApplyResourceChange(destroy): %v", err)
	}
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("destroying an already-deleted resource failed instead of converging: %s / %s", resp.Diagnostics[0].Summary, resp.Diagnostics[0].Detail)
	}
}
