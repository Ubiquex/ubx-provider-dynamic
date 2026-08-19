package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/ccapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// testQueueSchema is a small, real CFN-shaped fixture -- unmarshaled from
// real CFN JSON (rather than built via cloudformation's own unexported
// rawSchema type, which this package cannot reach), a real, minimal
// AWS::SQS::Queue with a nested RedrivePolicy object and a Tags array,
// matching the same real shape cloudformation's own package-internal
// fixture exercises.
const testQueueSchemaJSON = `{
  "typeName": "AWS::SQS::Queue",
  "properties": {
    "QueueName": {"type": "string"},
    "DelaySeconds": {"type": "integer"},
    "QueueUrl": {"type": "string"},
    "Arn": {"type": "string"},
    "RedrivePolicy": {"$ref": "#/definitions/RedrivePolicy"},
    "Tags": {"type": "array", "items": {"$ref": "#/definitions/Tag"}}
  },
  "definitions": {
    "RedrivePolicy": {
      "type": "object",
      "properties": {
        "deadLetterTargetArn": {"type": "string"},
        "maxReceiveCount": {"type": "integer"}
      }
    },
    "Tag": {
      "type": "object",
      "properties": {"Key": {"type": "string"}, "Value": {"type": "string"}}
    }
  },
  "required": ["QueueName"],
  "readOnlyProperties": ["/properties/QueueUrl", "/properties/Arn"],
  "primaryIdentifier": ["/properties/QueueUrl"],
  "createOnlyProperties": ["/properties/QueueName"]
}`

func testResource(t *testing.T) *cloudformation.BuiltResource {
	t.Helper()
	rs, err := cloudformation.ParseResourceSchema([]byte(testQueueSchemaJSON))
	if err != nil {
		t.Fatalf("ParseResourceSchema: %v", err)
	}
	files := map[string]*cloudformation.ResourceSchema{"AWS::SQS::Queue": rs}
	built, notes, err := cloudformation.Build(files, smithy.KnownNames{"aws_sqs_queue": true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	return built["aws_sqs_queue"]
}

// fakeCCAPIServer is a real, local, in-memory simulation of AWS Cloud
// Control API's own real, documented async contract (CreateResource
// returns IN_PROGRESS, GetResourceRequestStatus is polled until SUCCESS,
// then a real GetResource read supplies the final state; DeleteResource
// follows the identical real shape) -- proving this package's own real
// client/poll/read-after-write code against real HTTP requests, per
// CLAUDE.md's own standing rule that a real cloud apply is never run
// live (UBI-47's own incident); this is the hermetic substitute the
// founder explicitly chose for this verification.
type fakeCCAPIServer struct {
	mu                 sync.Mutex
	created            map[string]map[string]any // identifier -> real properties
	pollsBeforeSuccess int
	polls              map[string]int // requestToken -> real poll count so far
	deleted            map[string]bool
}

func newFakeCCAPIServer() *fakeCCAPIServer {
	return &fakeCCAPIServer{
		created:            map[string]map[string]any{},
		polls:              map[string]int{},
		deleted:            map[string]bool{},
		pollsBeforeSuccess: 2,
	}
}

func (f *fakeCCAPIServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")

		f.mu.Lock()
		defer f.mu.Unlock()

		switch target {
		case "CloudApiService.CreateResource":
			var props map[string]any
			_ = json.Unmarshal([]byte(body["DesiredState"].(string)), &props)
			identifier := "https://sqs.example.com/123456789012/" + props["QueueName"].(string)
			props["QueueUrl"] = identifier
			props["Arn"] = "arn:aws:sqs:us-east-1:123456789012:" + props["QueueName"].(string)
			f.created[identifier] = props
			token := "create-" + identifier
			f.polls[token] = 0
			writeProgressEvent(w, token, identifier, "IN_PROGRESS")

		case "CloudApiService.GetResourceRequestStatus":
			token := body["RequestToken"].(string)
			f.polls[token]++
			status := "IN_PROGRESS"
			var identifier string
			if f.polls[token] >= f.pollsBeforeSuccess {
				status = "SUCCESS"
			}
			for id := range f.created {
				if token == "create-"+id || token == "delete-"+id {
					identifier = id
				}
			}
			writeProgressEvent(w, token, identifier, status)

		case "CloudApiService.GetResource":
			identifier := body["Identifier"].(string)
			props, ok := f.created[identifier]
			if !ok || f.deleted[identifier] {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"__type":"ResourceNotFoundException","Message":"not found"}`))
				return
			}
			propsJSON, _ := json.Marshal(props)
			resp := map[string]any{
				"TypeName": "AWS::SQS::Queue",
				"ResourceDescription": map[string]any{
					"Identifier": identifier,
					"Properties": string(propsJSON),
				},
			}
			json.NewEncoder(w).Encode(resp)

		case "CloudApiService.DeleteResource":
			identifier := body["Identifier"].(string)
			f.deleted[identifier] = true
			token := "delete-" + identifier
			f.polls[token] = 0
			writeProgressEvent(w, token, identifier, "IN_PROGRESS")

		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}

func writeProgressEvent(w http.ResponseWriter, token, identifier, status string) {
	resp := map[string]any{
		"ProgressEvent": map[string]any{
			"RequestToken":    token,
			"Identifier":      identifier,
			"OperationStatus": status,
		},
	}
	json.NewEncoder(w).Encode(resp)
}

// TestCreateAndDestroy_RealAsyncPollCycle is this checkpoint's own real,
// hermetic end-to-end proof: PlanResourceChange -> ApplyResourceChange
// (create, IN_PROGRESS -> polled to SUCCESS -> a real read-after-write)
// -> ApplyResourceChange (destroy, IN_PROGRESS -> polled to SUCCESS),
// driven through the real Server exactly like ubx core's own real
// executor would, against a real HTTP server (only the AWS SIDE is
// faked -- every line of this package's own real client/poll/server
// code runs for real).
func TestCreateAndDestroy_RealAsyncPollCycle(t *testing.T) {
	fake := newFakeCCAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	rt := testResource(t)
	resources := map[string]*cloudformation.BuiltResource{"aws_sqs_queue": rt}
	client := ccapi.NewClient(srv.URL, nil)
	s := New("aws", resources, client)
	s.PollInterval = time.Millisecond
	s.PollTimeout = time.Second

	ctx := context.Background()

	configVal := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "my-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, nil),
		"arn":            tftypes.NewValue(tftypes.String, nil),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, nil),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(tftypes.List{ElementType: objAttrType(rt, "tags").(tftypes.List).ElementType}, nil),
	})
	nullVal := tftypes.NewValue(rt.ObjectType, nil)

	configDV, err := tfprotov6.NewDynamicValue(rt.ObjectType, configVal)
	if err != nil {
		t.Fatalf("NewDynamicValue(config): %v", err)
	}
	nullDV, err := tfprotov6.NewDynamicValue(rt.ObjectType, nullVal)
	if err != nil {
		t.Fatalf("NewDynamicValue(null): %v", err)
	}

	planResp, err := s.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "aws_sqs_queue",
		PriorState:       &nullDV,
		ProposedNewState: &configDV,
		Config:           &configDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	if len(planResp.Diagnostics) != 0 {
		t.Fatalf("PlanResourceChange diagnostics: %+v", planResp.Diagnostics)
	}

	createResp, err := s.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "aws_sqs_queue",
		PriorState:     &nullDV,
		PlannedState:   planResp.PlannedState,
		Config:         &configDV,
		PlannedPrivate: planResp.PlannedPrivate,
	})
	if err != nil {
		t.Fatalf("ApplyResourceChange(create): %v", err)
	}
	if len(createResp.Diagnostics) != 0 {
		t.Fatalf("create diagnostics: %+v", createResp.Diagnostics)
	}
	if createResp.NewState == nil {
		t.Fatal("create: NewState is nil")
	}

	newVal, err := createResp.NewState.Unmarshal(rt.ObjectType)
	if err != nil {
		t.Fatalf("unmarshal new state: %v", err)
	}
	var m map[string]tftypes.Value
	if err := newVal.As(&m); err != nil {
		t.Fatalf("new state as map: %v", err)
	}
	var queueURL string
	if err := m["queue_url"].As(&queueURL); err != nil {
		t.Fatalf("queue_url: %v", err)
	}
	if queueURL == "" {
		t.Fatal("expected a real, non-empty queue_url assigned by the fake CCAPI create")
	}
	fake.mu.Lock()
	_, created := fake.created[queueURL]
	fake.mu.Unlock()
	if !created {
		t.Fatalf("resource %q was not really created on the fake CCAPI side", queueURL)
	}

	destroyResp, err := s.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "aws_sqs_queue",
		PriorState:     createResp.NewState,
		PlannedState:   &nullDV,
		PlannedPrivate: planResp.PlannedPrivate,
	})
	if err != nil {
		t.Fatalf("ApplyResourceChange(destroy): %v", err)
	}
	if len(destroyResp.Diagnostics) != 0 {
		t.Fatalf("destroy diagnostics: %+v", destroyResp.Diagnostics)
	}

	fake.mu.Lock()
	deleted := fake.deleted[queueURL]
	fake.mu.Unlock()
	if !deleted {
		t.Fatal("resource was not really destroyed on the fake CCAPI side")
	}

	readResp, err := s.ReadResource(ctx, &tfprotov6.ReadResourceRequest{
		TypeName:     "aws_sqs_queue",
		CurrentState: createResp.NewState,
	})
	if err != nil {
		t.Fatalf("ReadResource after destroy: %v", err)
	}
	if readResp.NewState != nil {
		t.Fatal("expected a real nil NewState -- the resource no longer exists")
	}
}

func objAttrType(rt *cloudformation.BuiltResource, name string) tftypes.Type {
	obj := rt.ObjectType.(tftypes.Object)
	return obj.AttributeTypes[name]
}
