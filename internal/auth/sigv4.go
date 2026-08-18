package auth

import (
	"fmt"
	"net/http"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// aws_sigv4 is Phase 4's own real type (UBI-158's own three schema_source
// tiers: aws_ccapi needs real AWS SigV4 request signing, not a simple
// header). Registered now, in Phase 2, so the config SHAPE
// (region/service/credential_source) is settled before Phase 4 needs to
// build against it -- a config naming this type parses and validates
// cleanly today; Apply itself is a real, deliberate refusal, not a silent
// no-op, so a stack that names "aws_sigv4" today fails loudly the first
// time it's actually used rather than sending real requests unsigned.
//
// Real, confirmed finding, not assumed: the existing restexec.Authenticator
// interface -- Apply(req *http.Request) error -- already carries everything
// a real SigV4 signer needs. No interface change is required for Phase 4.
// AWS SigV4 signs over the method, URL, headers, and a SHA-256 hash of the
// body, all already present on *http.Request by the time Apply runs
// (restexec.Client.Do builds the complete request, headers included,
// before calling Authenticator.Apply -- see restexec.go). The one real
// wrinkle -- reading req.Body to hash it consumes the reader -- is also
// already solved: restexec.Client.Do always builds the request body from a
// *bytes.Reader, and net/http.NewRequestWithContext's own documented
// behavior (confirmed directly against go1.26's net/http/request.go, not
// assumed) special-cases *bytes.Buffer/*bytes.Reader/*strings.Reader to
// populate req.GetBody automatically. A real SigV4 Apply can call
// req.GetBody() to get a fresh reader for hashing, then call it again to
// reset req.Body afterward -- confirmed live in sigv4_test.go's own
// TestGetBodyIsPopulatedForBytesReaderBody, not just cited from docs.
func init() {
	Register("aws_sigv4", buildSigV4Stub)
}

// sigV4Params is [dynamic_providers.<name>.auth.params]'s own shape for
// this type:
//
//	type = "aws_sigv4"
//	[dynamic_providers.<name>.auth.params]
//	region = "us-east-1"
//	service = "execute-api"
//	credential_source = "env"
type sigV4Params struct {
	Region  string `json:"region"`
	Service string `json:"service"`
	// CredentialSource names where Phase 4's real signer should get its
	// AWS credentials from -- the same three real, standard sources every
	// AWS SDK supports: "env" (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY,
	// matching this package's own env-reference-only discipline),
	// "profile" (a named ~/.aws/credentials profile), or "instance_role"
	// (EC2/ECS/Lambda ambient credentials, no explicit secret at all --
	// the AWS provider binary's own real default posture). Accepted now,
	// implemented in Phase 4.
	CredentialSource string `json:"credential_source"`
}

type sigV4StubAuth struct {
	params sigV4Params
}

func buildSigV4Stub(params map[string]any) (restexec.Authenticator, error) {
	var p sigV4Params
	if err := paramsAs(params, &p); err != nil {
		return nil, fmt.Errorf("aws_sigv4: parse params: %w", err)
	}
	if p.Region == "" {
		return nil, fmt.Errorf("aws_sigv4: region is required")
	}
	if p.Service == "" {
		return nil, fmt.Errorf("aws_sigv4: service is required")
	}
	switch p.CredentialSource {
	case "", "env", "profile", "instance_role":
		// Accepted now (a config author may write ahead of Phase 4), even
		// though only Apply's own refusal below is real today.
	default:
		return nil, fmt.Errorf("aws_sigv4: unrecognized credential_source %q (want one of: env, profile, instance_role)", p.CredentialSource)
	}
	return &sigV4StubAuth{params: p}, nil
}

func (a *sigV4StubAuth) Apply(*http.Request) error {
	return fmt.Errorf("aws_sigv4: not yet implemented (UBI-158 Phase 4) -- configured for region=%s service=%s, refusing to send an unsigned request rather than silently skipping signing", a.params.Region, a.params.Service)
}
