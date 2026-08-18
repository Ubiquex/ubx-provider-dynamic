// Package wireexec performs the real HTTP calls a Smithy-sourced resource's
// CRUD lifecycle needs -- UBI-158 Phase 4 Checkpoint 2's own "wire
// protocols" scope item. restexec (Phase 1) already does this for a single
// real protocol shape (JSON body in, JSON body out, over a real OpenAPI
// path template); a Smithy-sourced AWS service's own real wire protocol is
// one of six declared by its own model (see smithy.Protocol), only one of
// which (restJson1) matches that shape directly. This package dispatches on
// the real, model-declared protocol -- never guessed per-service, matching
// this checkpoint's own explicit instruction -- and reuses restexec.Client's
// own real retry/backoff/auth logic via its DoWithCodec extension point
// (restexec/codec.go) for every one of them, rather than reimplementing
// that machinery four times.
//
// Real, confirmed finding this session (not assumed): the wire member name
// a real AWS request/response body uses is the Smithy shape's own member
// name (PascalCase, e.g. "QueueUrl"), never the snake_case attribute name
// (uschema.ToSnakeCase's own output, "queue_url") a translated tfplugin
// schema exposes. This package re-derives that mapping directly from the
// Smithy model at request/response time via the SAME uschema.ToSnakeCase
// function the translator itself used to build the schema in the first
// place (internal/schema/translate.go), rather than needing BuiltResource
// to carry a separate, parallel name map -- one real source of truth,
// consulted twice.
package wireexec

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// binding is one real operation's fully-resolved REST request shape --
// httpLabel/httpQuery/httpHeader members already extracted per their own
// real Smithy trait, everything else left for the body.
type binding struct {
	Method      string
	Path        string
	Query       url.Values
	Headers     map[string]string
	BodyMembers map[string]any // real Smithy member name -> value
}

// bindREST resolves opShapeID's own smithy.api#http trait plus its real
// input shape's httpLabel/httpQuery/httpHeader member traits against input
// (a snake_case-keyed map -- planned/prior resource state, already run
// through wire.ToJSON) -- restJson1 and restXml's own shared real request
// shape (both are REST-bound protocols; only the body's own wire encoding
// differs, handled by the caller).
func bindREST(model *smithy.Model, opShapeID string, input map[string]any) (binding, error) {
	op, ok := model.Shapes[opShapeID]
	if !ok {
		return binding{}, fmt.Errorf("wireexec: unknown operation shape %q", opShapeID)
	}
	httpTrait, hasHTTP, err := op.HTTPTrait()
	if err != nil {
		return binding{}, fmt.Errorf("wireexec: %s: %w", opShapeID, err)
	}
	if !hasHTTP {
		return binding{}, fmt.Errorf("wireexec: operation %s has no smithy.api#http trait -- not REST-bound", opShapeID)
	}

	b := binding{Method: httpTrait.Method, Query: url.Values{}, Headers: map[string]string{}, BodyMembers: map[string]any{}}
	if op.Input == nil {
		b.Path = httpTrait.URI
		return b, nil
	}
	inputShape, ok := model.Shapes[op.Input.Target]
	if !ok {
		return binding{}, fmt.Errorf("wireexec: %s: input shape %s not found", opShapeID, op.Input.Target)
	}

	pathValues := map[string]string{}
	for memberName, member := range inputShape.Members {
		snake := uschema.ToSnakeCase(memberName)
		val, present := input[snake]
		if !present || val == nil {
			continue
		}
		switch {
		case member.HasTrait("smithy.api#httpLabel"):
			s, err := scalarToString(val)
			if err != nil {
				return binding{}, fmt.Errorf("wireexec: member %s: %w", memberName, err)
			}
			pathValues[memberName] = s
		case member.HasTrait("smithy.api#httpQuery"):
			var queryName string
			if _, err := member.TraitInto("smithy.api#httpQuery", &queryName); err != nil {
				return binding{}, fmt.Errorf("wireexec: member %s: %w", memberName, err)
			}
			s, err := scalarToString(val)
			if err != nil {
				return binding{}, fmt.Errorf("wireexec: member %s: %w", memberName, err)
			}
			b.Query.Set(queryName, s)
		case member.HasTrait("smithy.api#httpHeader"):
			var headerName string
			if _, err := member.TraitInto("smithy.api#httpHeader", &headerName); err != nil {
				return binding{}, fmt.Errorf("wireexec: member %s: %w", memberName, err)
			}
			s, err := scalarToString(val)
			if err != nil {
				return binding{}, fmt.Errorf("wireexec: member %s: %w", memberName, err)
			}
			b.Headers[headerName] = s
		default:
			b.BodyMembers[memberName] = val
		}
	}

	path, err := substituteLabels(httpTrait.URI, pathValues)
	if err != nil {
		return binding{}, fmt.Errorf("wireexec: %s: %w", opShapeID, err)
	}
	b.Path = path
	return b, nil
}

// substituteLabels fills uri's own real Smithy httpLabel segments --
// "{Name}" (a single path segment, real value URL-escaped) and "{Name+}"
// (Smithy's own real "greedy label" syntax, confirmed live against S3's own
// GetObject: "/{Bucket}/{Key+}" -- Key can itself legitimately contain "/"
// characters representing a real S3 "folder" hierarchy, so a greedy label's
// own value is substituted RAW, never per-segment-escaped, matching what a
// real S3 key requires).
func substituteLabels(uri string, values map[string]string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(uri) {
		if uri[i] != '{' {
			b.WriteByte(uri[i])
			i++
			continue
		}
		end := strings.IndexByte(uri[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("malformed URI template %q: unclosed '{'", uri)
		}
		label := uri[i+1 : i+end]
		greedy := strings.HasSuffix(label, "+")
		name := strings.TrimSuffix(label, "+")
		val, ok := values[name]
		if !ok || val == "" {
			return "", fmt.Errorf("missing required path label %q for URI template %q", name, uri)
		}
		if greedy {
			b.WriteString(val)
		} else {
			b.WriteString(url.PathEscape(val))
		}
		i += end + 1
	}
	return b.String(), nil
}

func scalarToString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("value of type %T is not a valid scalar for a path/query/header binding", v)
	}
}

// sortedMemberNames is a small determinism helper -- CLAUDE.md's own
// standing rule against map-iteration-ordered output -- used by both the
// JSON-RPC and Query codecs when they need to walk BodyMembers/an input map
// in a fixed order (JSON's own encoding/json already sorts object keys on
// its own; this exists for the Query codec's own form-encoding, which does
// not).
func sortedMemberNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
