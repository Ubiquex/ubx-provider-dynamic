package wireexec

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// decodeXML is a generic, schema-blind XML->Go decoder: an element with
// only text content decodes to a string; an element with child elements
// decodes to a map[string]any, repeated child tags collapsing into a []any
// (AWS's own real restXml/Query response convention: a repeated "<member>"
// tag under a list-typed parent). The decoded map's own top-level (and
// nested) keys are the response's real Smithy PascalCase member names --
// reKeyToSnakeCase (called by the caller, Client.Do) converts them to the
// snake_case attribute names wire.FromJSON needs, the identical real
// resolution restJSON/JSON-RPC's own JSON-decoded response already goes
// through.
func decodeXML(raw []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	root, err := decodeXMLValue(dec)
	if err != nil {
		return nil, err
	}
	m, _ := root.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func decodeXMLValue(dec *xml.Decoder) (any, error) {
	var stack []map[string]any
	var textStack []string
	var root any

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("wireexec: decode XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, map[string]any{})
			textStack = append(textStack, "")
		case xml.CharData:
			if len(textStack) > 0 {
				textStack[len(textStack)-1] += string(t)
			}
		case xml.EndElement:
			m := stack[len(stack)-1]
			text := textStack[len(textStack)-1]
			stack = stack[:len(stack)-1]
			textStack = textStack[:len(textStack)-1]

			var val any
			if len(m) == 0 {
				val = trimXMLText(text)
			} else {
				val = m
			}

			if len(stack) == 0 {
				root = val
				continue
			}
			parent := stack[len(stack)-1]
			if existing, ok := parent[t.Name.Local]; ok {
				switch ex := existing.(type) {
				case []any:
					parent[t.Name.Local] = append(ex, val)
				default:
					parent[t.Name.Local] = []any{ex, val}
				}
			} else {
				parent[t.Name.Local] = val
			}
		}
	}
	return root, nil
}

func trimXMLText(s string) string {
	start, end := 0, len(s)
	for start < end && isXMLSpace(s[start]) {
		start++
	}
	for end > start && isXMLSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isXMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// reKeyToSnakeCase converts an XML/Query-decoded map's own real Smithy
// PascalCase keys (or, recursively, any nested map's keys) into the
// snake_case attribute names uschema.ToSnakeCase produces -- wire.FromJSON's
// own required input shape, matching restJSON/JSON-RPC's identical
// requirement after their own JSON decode.
func reKeyToSnakeCase(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[uschema.ToSnakeCase(k)] = reKeyToSnakeCase(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = reKeyToSnakeCase(e)
		}
		return out
	default:
		return v
	}
}
