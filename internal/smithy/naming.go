// Naming compatibility layer -- UBI-158 Phase 4's own highest-risk piece:
// every existing ledger address, every published SDK binding, and all
// 1684 AWS documentation pages depend on HashiCorp's own real
// aws_<service>_<resource> naming, never on anything Smithy or
// CloudFormation would derive independently.
//
// Real, confirmed-live evidence this session (dumped via ubx's own real
// provider.Acquire/Launch against hashicorp/aws 6.54.0's own real
// GetProviderSchema -- 1682 real resource type names, the identical
// "schema-fetch, safe, no credentials" mechanism this repo's own
// docs/executor.md and STATE.md history already establish as the correct
// way to get ground truth, not memory) shows the real naming relationship
// is NOT one uniform formula:
//
//   - SQS, DynamoDB, Lambda: exact formulaic match. aws_<endpointPrefix>_
//     <snake(noun)> -- "sqs"+"Queue" -> aws_sqs_queue, "dynamodb"+"Table"
//     -> aws_dynamodb_table, "lambda"+"Function" -> aws_lambda_function.
//     Confirmed against all three real models' own real endpointPrefix
//     trait and the real hashicorp name list.
//   - S3: also exact formulaic match (aws_s3_bucket) for the resource this
//     phase actually discovers from S3's own real model.
//   - EC2: genuinely NOT formulaic. Confirmed live: aws_instance, aws_vpc,
//     aws_subnet, and aws_security_group carry NO "ec2_" infix at all,
//     while aws_ebs_volume/aws_ebs_snapshot use a real, DIFFERENT prefix
//     ("ebs", not "ec2") for operations that live in the exact same
//     Smithy/API model -- yet dozens of other real EC2-API resources
//     (aws_ec2_transit_gateway, aws_ec2_fleet, aws_ec2_host, ...) DO carry
//     the "ec2_" prefix as expected. HashiCorp's own naming here tracks
//     AWS's real, human marketing/conceptual service boundaries (EC2 core
//     compute, EBS storage, VPC networking), not the literal API/Smithy
//     service boundary -- something no algorithm operating on the Smithy
//     model alone can derive, because the model itself doesn't encode
//     that distinction anywhere. This is a real, permanent limitation,
//     not a bug to fix: see Resolve's own doc comment for how it's
//     handled -- try the formula, try the bare (no-prefix) fallback
//     (covers instance/vpc/subnet/security_group), and honestly report
//     "unresolved, needs a real curated alias" for the rest (ebs_* and
//     any future irregular case) rather than guessing.
package smithy

import (
	"bufio"
	"os"
	"strings"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// KnownNames is the real, confirmed set of HashiCorp AWS provider
// resource type names to check candidates against -- callers load this
// once (LoadKnownNames) from a real source (this session's own live
// GetProviderSchema dump, or any future equivalent), never hardcoded here.
type KnownNames map[string]bool

// LoadKnownNames reads a real, newline-separated resource-type-name list
// (this repo's own testdata/hashicorp-aws-resource-names.txt is exactly
// this shape -- one real name per line, produced by dumping
// hashicorp/aws's own live GetProviderSchema via ubx's real
// provider.Acquire/Launch, confirmed 1682 real names this session).
func LoadKnownNames(path string) (KnownNames, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	names := KnownNames{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "TOTAL:") {
			continue
		}
		names[line] = true
	}
	return names, scanner.Err()
}

// Strategy names which real naming rule produced a Resolve match --
// reported alongside the name itself so a caller (or this package's own
// validation tests) can distinguish a confirmed-by-formula result from a
// confirmed-by-fallback one, and both from a genuine gap.
type Strategy string

const (
	StrategyPrefixed   Strategy = "prefixed" // aws_<endpointPrefix>_<noun>
	StrategyBare       Strategy = "bare"     // aws_<noun>, no service infix
	StrategyUnresolved Strategy = "unresolved"
)

// Resolve computes the real HashiCorp-compatible resource type name for a
// Smithy-sourced resource, given the owning service's own real
// endpointPrefix and the resource's own real noun (resourcemap.go's own
// Resource.Noun). Tries, in order: (1) the formulaic prefixed name, (2)
// the bare (no-prefix) name -- both checked against known, a real,
// confirmed name set, never assumed correct just because it was
// computable. Neither matching is a genuine, honestly-reported gap
// (StrategyUnresolved) -- Resolve still returns the prefixed candidate as
// its own best real guess in that case (never empty), but callers MUST
// check Strategy, not just trust the returned name, before treating it as
// ledger/SDK/docs-compatible.
func Resolve(svc *Service, noun string, known KnownNames) (name string, strategy Strategy) {
	snake := uschema.ToSnakeCase(noun)
	prefixed := "aws_" + svc.Traits.EndpointPrefix + "_" + snake
	bare := "aws_" + snake

	switch {
	case known[prefixed]:
		return prefixed, StrategyPrefixed
	case known[bare]:
		return bare, StrategyBare
	default:
		return prefixed, StrategyUnresolved
	}
}
