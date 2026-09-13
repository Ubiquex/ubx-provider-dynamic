package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixture_permissiveness_test.go asserts that the fake CCAPI server is
// no easier to satisfy than the real API (UBI-252).
//
// Every other test here uses the fake to check this package's code. These
// check the fake itself, and they exist because the fake being generous
// is what hid a real bug for the entire life of the feature: it answered
// a missing resource with a REST-shaped HTTP 404, real CCAPI answers with
// HTTP 400 carrying a "__type" of ResourceNotFoundException, and
// restexec.IsNotFound matched the fake and never the real thing. So
// ReadResource's "this resource is gone" branch was reachable in tests
// and unreachable in production, and every out-of-band deletion was
// invisible to ubx on every AWS resource.
//
// That is fixed. What was missing is anything stopping it coming back,
// and the gap is not hypothetical: ccapi.IsNotFound deliberately accepts
// a plain 404 as well, defensively, so a fake that drifted back to
// returning 404 would still satisfy every existing test in this
// repository. Verified by doing exactly that before writing these: the
// whole cloudformation suite passes with the fake regressed to the
// original bug's own shape.
//
// A fake that is easier to satisfy than the real API is worse than no
// fake, because it converts an unverified path into an apparently
// verified one.

// callFake drives the fake CCAPI server directly over HTTP, the way the
// real client does, and returns the raw status and body so the SHAPE of
// the answer can be asserted rather than only its classification.
func callFake(t *testing.T, target string, payload map[string]any) (int, string) {
	t.Helper()
	fake := newFakeCCAPIServer()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Target", target)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.String()
}

// assertRealNotFoundShape pins the exact answer real CCAPI gives, not
// merely "some error a not-found classifier happens to accept".
func assertRealNotFoundShape(t *testing.T, status int, body string) {
	t.Helper()
	if status == http.StatusNotFound {
		t.Fatalf("the fake answered HTTP 404, which is the REST convention and precisely the shape that hid this bug. Real CCAPI answers HTTP 400 with a typed body; a 404 here makes restexec.IsNotFound match the fake and never the real thing")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: real CCAPI carries almost nothing in the HTTP status and puts the error identity in the body", status)
	}
	var payload struct {
		Type string `json:"__type"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("the body is not the awsJson1_0 shape real CCAPI returns: %v\nbody: %s", err, body)
	}
	name := payload.Type
	if i := strings.LastIndex(name, "#"); i >= 0 {
		name = name[i+1:]
	}
	if name != "ResourceNotFoundException" {
		t.Errorf("__type = %q, want a ResourceNotFoundException: that name is where the meaning lives, and it is what the classifier matches on", payload.Type)
	}
}

// A missing resource. This is the case that shipped broken.
func TestFakeCCAPI_GetResourceOnAMissingResourceUsesTheRealNotFoundShape(t *testing.T) {
	status, body := callFake(t, "CloudApiService.GetResource", map[string]any{
		"TypeName":   "AWS::SQS::Queue",
		"Identifier": "https://sqs.example.com/123456789012/never-created",
	})
	assertRealNotFoundShape(t, status, body)
}

// Real CCAPI refuses to delete what it cannot find. The fake used to
// succeed unconditionally, which hid the destroy half of the same
// classification gap: a destroy against an already-absent resource
// looked like it worked.
func TestFakeCCAPI_DeleteResourceRefusesWhatItCannotFind(t *testing.T) {
	status, body := callFake(t, "CloudApiService.DeleteResource", map[string]any{
		"TypeName":   "AWS::SQS::Queue",
		"Identifier": "https://sqs.example.com/123456789012/never-created",
	})
	if status == http.StatusOK {
		t.Fatalf("the fake deleted a resource that was never created. Real CCAPI refuses, and a fake that succeeds unconditionally makes the destroy path look verified when it is not.\nbody: %s", body)
	}
	assertRealNotFoundShape(t, status, body)
}
