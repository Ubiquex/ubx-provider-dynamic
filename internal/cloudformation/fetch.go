package cloudformation

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Fetch downloads AWS's real, published CloudFormation resource-provider
// schema registry ZIP (zipURL -- the real, public, unauthenticated
// https://schema.cloudformation.<region>.amazonaws.com/CloudformationSchema.zip)
// and parses every real "aws-*.json" entry into a ResourceSchema, keyed
// by its own real typeName. Real, confirmed filter (same registry this
// session's own earlier AWS coverage investigation already fetched and
// inspected): the registry carries exactly one real non-"AWS::"-namespaced
// entry (Alexa::ASK::Skill) -- skipped here, not silently included, since
// this provider's own real naming/precedence story (naming.go's Resolve)
// is scoped to AWS's own resource surface.
func Fetch(zipURL string) (map[string]*ResourceSchema, error) {
	req, err := http.NewRequest(http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudformation registry %q: %w", zipURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch cloudformation registry %q: %w", zipURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch cloudformation registry %q: HTTP %d", zipURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read cloudformation registry %q: %w", zipURL, err)
	}
	return parseRegistryZip(body)
}

func parseRegistryZip(zipBytes []byte) (map[string]*ResourceSchema, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open cloudformation registry zip: %w", err)
	}

	out := map[string]*ResourceSchema{}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		rs, err := ParseResourceSchema(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f.Name, err)
		}
		if !strings.HasPrefix(rs.TypeName, "AWS::") {
			continue
		}
		out[rs.TypeName] = rs
	}
	return out, nil
}
