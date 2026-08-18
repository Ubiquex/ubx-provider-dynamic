package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	sigv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// aws_sigv4 is UBI-158 Phase 4 Checkpoint 2's own real signer, completing
// the interface Phase 2 stubbed. See sigV4Params' own doc comment for
// config shape; the interface itself needed no change (see the doc comment
// this file's own predecessor left, confirmed correct below).
//
// Real, confirmed finding, not assumed: restexec.Authenticator's own
// Apply(req *http.Request) error already carries everything a real SigV4
// signer needs -- AWS SigV4 signs over the method, URL, headers, and a
// SHA-256 hash of the body, all present on *http.Request by the time Apply
// runs. The one real wrinkle (reading req.Body to hash it consumes the
// reader) is solved via req.GetBody, already populated for every real
// request restexec.Client/wireexec build (a *bytes.Reader body), confirmed
// live in sigv4_test.go's own TestGetBodyIsPopulatedForBytesReaderBody
// (Phase 2) and re-verified this session against real signed requests
// (TestSigV4_RealListQueuesAgainstLiveAWS). No interface change was needed.
func init() {
	Register("aws_sigv4", buildSigV4)
}

// sigV4Params is [dynamic_providers.<name>.auth.params]'s own shape for
// this type:
//
//	type = "aws_sigv4"
//	[dynamic_providers.<name>.auth.params]
//	region = "us-east-1"
//	service = "sqs"
//	credential_source = "env"
//	# profile is only read when credential_source = "profile"; empty means
//	# the real, standard AWS_PROFILE env var / "default", exactly like
//	# every other real AWS SDK/CLI.
//	profile = ""
type sigV4Params struct {
	Region  string `json:"region"`
	Service string `json:"service"`
	// CredentialSource names where this signer gets its AWS credentials
	// from -- the same three real, standard sources every AWS SDK
	// supports: "env" (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/
	// AWS_SESSION_TOKEN, matching this package's own env-reference-only
	// discipline -- SigV4's env var NAMES are themselves the real, fixed
	// AWS standard, not a config-supplied name the way apikey.go's own
	// header value source is), "profile" (a named ~/.aws/credentials
	// profile, resolved via the real, standard aws-sdk-go-v2/config
	// loader), or "instance_role" (EC2/ECS/Lambda ambient credentials via
	// the real IMDS/container-credentials chain, no explicit secret at
	// all).
	CredentialSource string `json:"credential_source"`
	Profile          string `json:"profile"`
}

type sigV4Auth struct {
	params sigV4Params
	creds  awssdk.CredentialsProvider
	signer *sigv4.Signer
}

func buildSigV4(params map[string]any) (restexec.Authenticator, error) {
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

	creds, err := resolveCredentialsProvider(p)
	if err != nil {
		return nil, fmt.Errorf("aws_sigv4: %w", err)
	}

	return &sigV4Auth{params: p, creds: creds, signer: sigv4.NewSigner()}, nil
}

// resolveCredentialsProvider builds the real, standard AWS credentials
// chain for p.CredentialSource, wrapped in aws.CredentialsCache so a real
// IMDS/STS round-trip only happens once (and is refreshed automatically as
// it nears real expiry), not on every single Apply call.
func resolveCredentialsProvider(p sigV4Params) (awssdk.CredentialsProvider, error) {
	switch p.CredentialSource {
	case "", "env":
		akid, ok1 := os.LookupEnv("AWS_ACCESS_KEY_ID")
		secret, ok2 := os.LookupEnv("AWS_SECRET_ACCESS_KEY")
		if !ok1 || akid == "" || !ok2 || secret == "" {
			return nil, fmt.Errorf("credential_source=env requires AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY to be set (or empty)")
		}
		token := os.Getenv("AWS_SESSION_TOKEN")
		return awssdk.NewCredentialsCache(awscreds.NewStaticCredentialsProvider(akid, secret, token)), nil
	case "profile":
		opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(p.Region)}
		if p.Profile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(p.Profile))
		}
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
		if err != nil {
			return nil, fmt.Errorf("credential_source=profile: load AWS config: %w", err)
		}
		return cfg.Credentials, nil
	case "instance_role":
		// The real, standard default chain (env -> shared config -> real
		// EC2/ECS/Lambda IMDS/container-credentials) -- deliberately no
		// explicit profile/static override here, since "instance_role"
		// means "use whatever ambient role this process is already
		// running as," the AWS provider binary's own real default posture.
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(p.Region))
		if err != nil {
			return nil, fmt.Errorf("credential_source=instance_role: load AWS config: %w", err)
		}
		return cfg.Credentials, nil
	default:
		return nil, fmt.Errorf("unrecognized credential_source %q (want one of: env, profile, instance_role)", p.CredentialSource)
	}
}

// Apply signs req in place with real AWS SigV4 -- canonical request
// construction, body hashing, and the header set AWS's own signature
// verification requires (Authorization, X-Amz-Date, and X-Amz-Security-Token
// when a session token is present), all performed by
// aws-sdk-go-v2/aws/signer/v4's own real, standard Signer -- this package
// does not hand-roll canonicalization.
func (a *sigV4Auth) Apply(req *http.Request) error {
	ctx := req.Context()
	creds, err := a.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("aws_sigv4: retrieve credentials: %w", err)
	}

	payloadHash, err := hashBody(req)
	if err != nil {
		return fmt.Errorf("aws_sigv4: hash request body: %w", err)
	}
	// SigV4 requires the payload hash on its own real header too --
	// AWS's own signature verification recomputes and compares it, not
	// just trusts what went into the canonical request string.
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	if err := a.signer.SignHTTP(ctx, creds, req, payloadHash, a.params.Service, a.params.Region, time.Now()); err != nil {
		return fmt.Errorf("aws_sigv4: sign request: %w", err)
	}
	return nil
}

// hashBody computes the real, hex-encoded SHA-256 the signer needs,
// restoring req.Body afterward via req.GetBody so the actual send still
// gets the full, unconsumed body -- see this file's own doc comment for why
// req.GetBody is always populated here (restexec/wireexec always build
// request bodies from a *bytes.Reader). A nil body (GET/DELETE, or a
// JSON-RPC/Query request with an empty real payload) hashes to the same
// well-known real empty-string SHA-256 AWS itself expects.
func hashBody(req *http.Request) (string, error) {
	if req.Body == nil {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:]), nil
	}
	if req.GetBody == nil {
		return "", fmt.Errorf("request body has no GetBody -- not a *bytes.Reader-backed request")
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	sum := sha256.Sum256(raw)
	fresh, err := req.GetBody()
	if err != nil {
		return "", fmt.Errorf("restore body via GetBody: %w", err)
	}
	req.Body = fresh
	return hex.EncodeToString(sum[:]), nil
}
