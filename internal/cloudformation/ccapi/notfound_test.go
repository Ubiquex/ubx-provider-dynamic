package ccapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// realCCAPINotFoundBody is the verbatim body AWS returned for a
// GetResource against a queue that had just been deleted out of band,
// captured from a real call rather than composed here. The HTTP status
// alongside it was 400, not 404, which is the whole reason this
// classification exists.
const realCCAPINotFoundBody = `{"__type":"com.amazon.cloudapiservice#ResourceNotFoundException","Message":"AWS::SQS::Queue Handler returned status FAILED: Resource of type 'AWS::SQS::Queue' with identifier 'https://sqs.us-east-1.amazonaws.com/839333509514/ubx-demo-queue' was not found. (HandlerErrorCode: NotFound, RequestToken: 0bd51fc8-0987-451d-aafc-3079f76f4186)"}`

func TestIsNotFound_RealCCAPIShape(t *testing.T) {
	// Wrapped exactly the way Client.call wraps it, so the test exercises
	// the real errors.As unwrapping and not a bare APIError.
	err := fmt.Errorf("ccapi: %s: %w", "GetResource", &restexec.APIError{
		Method:     "POST",
		Path:       "/",
		StatusCode: 400,
		Body:       realCCAPINotFoundBody,
	})

	if restexec.IsNotFound(err) {
		t.Fatal("restexec.IsNotFound matched a 400; if it now handles awsJson1_0, this package's own IsNotFound is redundant and should be reconsidered rather than left duplicating it")
	}
	if !IsNotFound(err) {
		t.Fatalf("CCAPI's real not-found answer was not recognised: %v", err)
	}
}

// A 404 still counts, so the CCAPI check is a superset rather than a
// replacement.
func TestIsNotFound_PlainHTTP404(t *testing.T) {
	err := fmt.Errorf("ccapi: GetResource: %w", &restexec.APIError{
		Method: "POST", Path: "/", StatusCode: 404, Body: `{}`,
	})
	if !IsNotFound(err) {
		t.Fatal("a plain 404 must still be treated as not-found")
	}
}

// Every other CCAPI failure must stay a real error. Misclassifying one as
// "gone" would be worse than the bug being fixed: ubx would record a
// resource as destroyed because the call was throttled or denied.
func TestIsNotFound_OtherErrorsAreNotSwallowed(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"throttling", `{"__type":"com.amazon.cloudapiservice#ThrottlingException","Message":"Rate exceeded"}`},
		{"access denied", `{"__type":"com.amazon.cloudapiservice#UnauthorizedException","Message":"not authorized"}`},
		{"handler failure", `{"__type":"com.amazon.cloudapiservice#HandlerFailureException","Message":"boom"}`},
		{"empty body", ``},
		{"not json", `<html>502</html>`},
		{"no __type", `{"Message":"something"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("ccapi: GetResource: %w", &restexec.APIError{
				Method: "POST", Path: "/", StatusCode: 400, Body: tc.body,
			})
			if IsNotFound(err) {
				t.Fatalf("%s was misread as not-found, which would record a live resource as destroyed", tc.name)
			}
		})
	}
}

func TestIsNotFound_NilAndUnrelated(t *testing.T) {
	if IsNotFound(nil) {
		t.Error("nil is not a not-found")
	}
	if IsNotFound(errors.New("connection reset")) {
		t.Error("a transport error is not a not-found")
	}
}

// The service prefix before "#" is AWS's to change; the exception name
// after it is what carries the meaning.
func TestIsNotFound_MatchesOnExceptionNameNotPrefix(t *testing.T) {
	for _, typ := range []string{
		"com.amazon.cloudapiservice#ResourceNotFoundException",
		"com.amazonaws.cloudcontrol#ResourceNotFoundException",
		"ResourceNotFoundException",
	} {
		err := fmt.Errorf("ccapi: GetResource: %w", &restexec.APIError{
			StatusCode: 400, Body: fmt.Sprintf(`{"__type":%q}`, typ),
		})
		if !IsNotFound(err) {
			t.Errorf("%q was not recognised as not-found", typ)
		}
	}
}
