// Package smithy loads and parses AWS's own real, official Smithy 2.0 JSON
// AST service models -- UBI-158 Phase 4's schema_source = "smithy" tier.
//
// Real, current source, confirmed directly this session (not assumed):
// https://github.com/aws/api-models-aws ("API Models for all public AWS
// Services," a real, public, AWS-owned repo), one JSON file per service at
// models/<service>/service/<api-version>/<service>-<api-version>.json --
// e.g. models/sqs/service/2012-11-05/sqs-2012-11-05.json. A provider's own
// schema_url for schema_source = "smithy" points directly at one such
// resolved file's own raw.githubusercontent.com URL, the identical
// single-file-URL shape schema_source = "openapi" already uses -- no config
// schema change needed.
//
// The Smithy 2.0 JSON AST is genuinely simpler to parse than OpenAPI: one
// flat map of shapeId -> shape, every cross-shape reference (a structure
// member's own target, an operation's input/output, a service's own
// operation list) is a plain string key into that SAME map -- no $ref
// resolution, no external documents, no loader step beyond decoding one
// JSON file. Confirmed directly against five real, structurally different
// services this session (SQS, S3, DynamoDB, EC2, Lambda) before writing
// this package, not assumed from the Smithy spec alone.
package smithy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Model is one service's own real Smithy 2.0 JSON AST document.
type Model struct {
	SmithyVersion string           `json:"smithy"`
	Metadata      map[string]any   `json:"metadata,omitempty"`
	Shapes        map[string]Shape `json:"shapes"`
}

// Shape is one Smithy shape -- deliberately one Go struct for every real
// shape type (service/operation/structure/union/list/map/enum/primitive),
// rather than a type-per-kind hierarchy: the JSON AST itself is exactly
// this shape (a `type` discriminator plus a fixed, mostly-disjoint set of
// optional fields per type), confirmed directly against five real,
// structurally different service models rather than assumed from the
// Smithy spec's own prose.
type Shape struct {
	Type string `json:"type"`

	// structure/union
	Members map[string]Member `json:"members,omitempty"`

	// list/set
	Member *Member `json:"member,omitempty"`

	// map
	Key   *Member `json:"key,omitempty"`
	Value *Member `json:"value,omitempty"`

	// operation
	Input  *ShapeRef  `json:"input,omitempty"`
	Output *ShapeRef  `json:"output,omitempty"`
	Errors []ShapeRef `json:"errors,omitempty"`

	// service
	Version    string     `json:"version,omitempty"`
	Operations []ShapeRef `json:"operations,omitempty"`
	Resources  []ShapeRef `json:"resources,omitempty"`

	Traits map[string]json.RawMessage `json:"traits,omitempty"`
}

// Member is a structure/union field, or a list/map's own element
// reference -- Smithy's own `required`/`http`/`enumValue`/... traits live
// here, at the MEMBER level, not on a parent-level "required" list the way
// OpenAPI/JSON-Schema puts it -- see toschema.go's own doc comment for
// where this real format difference gets reconciled.
type Member struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits,omitempty"`
}

// ShapeRef is a bare shapeId reference with no traits of its own (an
// operation's input/output, a service's operations/resources list, an
// error list entry).
type ShapeRef struct {
	Target string `json:"target"`
}

// HasTrait reports whether name (a real, fully-qualified Smithy trait
// shapeId, e.g. "smithy.api#required") is present -- true even for a
// trait whose own value is an empty object ({}), the real, common shape
// boolean-ish traits like smithy.api#required use.
func (m Member) HasTrait(name string) bool {
	_, ok := m.Traits[name]
	return ok
}

// HasTrait mirrors Member.HasTrait for a Shape's own traits (used for
// operation-level smithy.api#http, service-level aws.protocols#*, ...).
func (s Shape) HasTrait(name string) bool {
	_, ok := s.Traits[name]
	return ok
}

// TraitInto decodes a member-level trait's own JSON value into out --
// Member's own real counterpart to Shape.Trait, needed by wireexec
// (Checkpoint 2) for member-level traits whose value carries real content
// (smithy.api#httpQuery/httpHeader's own string value, the real wire query
// parameter/header name to use), not just presence.
func (m Member) TraitInto(name string, out any) (present bool, err error) {
	raw, ok := m.Traits[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return true, fmt.Errorf("trait %s: %w", name, err)
	}
	return true, nil
}

// Trait decodes trait name's own JSON value into out, reporting whether it
// was present at all -- err is only non-nil if the trait WAS present but
// didn't decode into the requested shape (a real, honest failure, never
// silently ignored).
func (s Shape) Trait(name string, out any) (present bool, err error) {
	raw, ok := s.Traits[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return true, fmt.Errorf("trait %s: %w", name, err)
	}
	return true, nil
}

// HTTPTrait is smithy.api#http's own real, decoded shape -- method/uri
// (an operation's real REST binding, restJson1/restXml protocols only)
// and code (the real success status code the operation returns, default
// 200 if the trait itself is absent, per the Smithy spec).
type HTTPTrait struct {
	Method string `json:"method"`
	URI    string `json:"uri"`
	Code   int    `json:"code"`
}

// HTTPTrait decodes op's own smithy.api#http trait, ok=false if absent
// (awsJson1_x/awsQuery/ec2Query operations never carry one -- those
// protocols have no per-operation REST binding at all, the whole request
// always goes to the service root).
func (s Shape) HTTPTrait() (HTTPTrait, bool, error) {
	var h HTTPTrait
	present, err := s.Trait("smithy.api#http", &h)
	if err != nil {
		return HTTPTrait{}, false, err
	}
	if !present {
		return HTTPTrait{}, false, nil
	}
	if h.Code == 0 {
		h.Code = 200
	}
	return h, true, nil
}

// ServiceTraits is aws.api#service's own real, decoded shape -- the
// naming layer's own primary real signal (naming.go): endpointPrefix is
// the same real string HashiCorp's own aws_<prefix>_<resource> convention
// uses (confirmed live against SQS: both endpointPrefix and
// arnNamespace are "sqs", HashiCorp's own resource type is
// "aws_sqs_queue").
type ServiceTraits struct {
	SDKID              string `json:"sdkId"`
	ArnNamespace       string `json:"arnNamespace"`
	CloudFormationName string `json:"cloudFormationName"`
	EndpointPrefix     string `json:"endpointPrefix"`
}

// Protocol reports which real AWS wire protocol trait the service shape
// declares -- exactly one of these five is present on every real service
// this session confirmed (SQS/DynamoDB: awsJson1_0; S3: restXml; EC2:
// ec2Query; Lambda: restJson1) -- never guessed per-service, always read
// directly from the model's own declaration.
type Protocol string

const (
	ProtocolRestJSON1 Protocol = "aws.protocols#restJson1"
	ProtocolRestXML   Protocol = "aws.protocols#restXml"
	ProtocolAWSJSON10 Protocol = "aws.protocols#awsJson1_0"
	ProtocolAWSJSON11 Protocol = "aws.protocols#awsJson1_1"
	ProtocolAWSQuery  Protocol = "aws.protocols#awsQuery"
	ProtocolEC2Query  Protocol = "aws.protocols#ec2Query"
)

// Service is doc's own service shape, resolved and decoded -- the entry
// point every other real fact (operations, protocol, naming traits) hangs
// off of.
type Service struct {
	ShapeID  string
	Shape    Shape
	Traits   ServiceTraits
	Protocol Protocol
}

// FindService returns doc's own single service shape -- a real Smithy
// model file always declares exactly one (confirmed across all five real
// models this session read), so more or less than one is a genuine error,
// not a case to guess at.
func FindService(doc *Model) (*Service, error) {
	var found []string
	for id, s := range doc.Shapes {
		if s.Type == "service" {
			found = append(found, id)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("smithy model has no service shape")
	}
	if len(found) > 1 {
		return nil, fmt.Errorf("smithy model has %d service shapes (%v), expected exactly one", len(found), found)
	}
	id := found[0]
	shape := doc.Shapes[id]

	var traits ServiceTraits
	if _, err := shape.Trait("aws.api#service", &traits); err != nil {
		return nil, fmt.Errorf("service %s: %w", id, err)
	}

	proto, err := resolveProtocol(shape)
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", id, err)
	}

	return &Service{ShapeID: id, Shape: shape, Traits: traits, Protocol: proto}, nil
}

func resolveProtocol(s Shape) (Protocol, error) {
	candidates := []Protocol{ProtocolRestJSON1, ProtocolRestXML, ProtocolAWSJSON10, ProtocolAWSJSON11, ProtocolAWSQuery, ProtocolEC2Query}
	var found []Protocol
	for _, p := range candidates {
		if s.HasTrait(string(p)) {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no recognized protocol trait present (want one of %v)", candidates)
	}
	// A real service can legitimately declare more than one protocol
	// trait (e.g. SQS also carries aws.protocols#awsQueryCompatible
	// alongside its real, primary awsJson1_0) -- the first recognized
	// match, in this fixed preference order, is the one this package
	// actually implements against; awsQueryCompatible itself is a real,
	// distinct opt-in wire variant this phase does not implement, so it's
	// deliberately not in the candidates list at all rather than silently
	// mis-selected.
	return found[0], nil
}

// Load fetches and parses source (an http(s) URL or local file path,
// mirroring internal/openapi.Load's own real convention) as a Smithy 2.0
// JSON AST document.
func Load(source string) (*Model, error) {
	raw, err := fetch(source)
	if err != nil {
		return nil, fmt.Errorf("load Smithy model from %s: %w", source, err)
	}
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse Smithy model from %s: %w", source, err)
	}
	if m.SmithyVersion == "" {
		return nil, fmt.Errorf("%s does not look like a Smithy JSON AST document (no top-level \"smithy\" version field)", source)
	}
	return &m, nil
}

// smithyHTTPClient bounds a real model fetch -- AWS's own SQS model alone
// is ~270KB, EC2's is ~6.6MB (confirmed live this session), never
// http.DefaultClient's unbounded wait.
var smithyHTTPClient = &http.Client{Timeout: 60 * time.Second}

func fetch(source string) ([]byte, error) {
	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		resp, err := smithyHTTPClient.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, source)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(source)
}
