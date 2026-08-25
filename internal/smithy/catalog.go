// AWS-wide catalog enumeration -- UBI-186's own real answer to
// model.go's own documented gap: schema_url for schema_source = "smithy"
// resolves one service file at a time, and AWS-wide coverage needs all
// of them. Confirmed live this session against the real, public
// github.com/aws/api-models-aws repo: 430 real service directories, one
// Smithy JSON AST model file each.
package smithy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// CatalogEntry is one real AWS service's own model file location --
// ServiceSlug is the repo's own directory name (e.g. "sqs", "ec2"),
// SchemaURL is the real, resolved raw.githubusercontent.com URL to that
// service's own Smithy JSON AST file, the identical single-file shape a
// [dynamic_providers.<name>] table's own schema_url already expects (see
// model.go's own doc comment) -- a CatalogEntry can be dropped straight
// into a real provider config unchanged, no further resolution needed.
type CatalogEntry struct {
	ServiceSlug string
	SchemaURL   string
}

const (
	apiModelsAWSTreeURL = "https://api.github.com/repos/aws/api-models-aws/git/trees/main?recursive=1"
	apiModelsAWSRawBase = "https://raw.githubusercontent.com/aws/api-models-aws/main/"
)

var catalogHTTPClient = &http.Client{Timeout: 60 * time.Second}

// FetchCatalog enumerates every real AWS service model file in
// github.com/aws/api-models-aws. Uses the git Trees API's own
// recursive=1 mode -- one real HTTP call returns the whole real file
// tree (confirmed live this session: 430 real matching paths,
// truncated: false) -- rather than one real per-directory listing call
// per service, the same "don't do N round trips when the provider's own
// API answers in one" discipline this pipeline already applies elsewhere
// (CloudFormation's own single real registry zip, not a per-type CFN
// call).
//
// Deliberately returns only the catalog (service slug + real schema_url)
// -- fetching and validating all 430 real model files against this
// package's own Load/FindService/Discover/DiscoverDataSources is real,
// separate, callable work a caller does per entry, not bundled into
// enumeration itself, mirroring discoverydoc.go's own Load-one-at-a-time
// shape and this repo's own established config-expansion precedent
// (sdk/providers/.ubx/config's real, individually-verified per-service
// entries for GCP/Azure -- "128 new google_<api> entries... 288 new
// azure_<rp>_<file> entries... live-verified"): enumerate once, verify
// each real candidate before it becomes a committed config entry, never
// trust enumeration alone as proof a file is usable.
func FetchCatalog() ([]CatalogEntry, error) {
	resp, err := catalogHTTPClient.Get(apiModelsAWSTreeURL)
	if err != nil {
		return nil, fmt.Errorf("fetch api-models-aws tree: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch api-models-aws tree: HTTP %d", resp.StatusCode)
	}

	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, fmt.Errorf("parse api-models-aws tree: %w", err)
	}
	// GitHub's own Trees API truncates past ~100,000 entries or ~7MB of
	// response -- real, documented behavior, not this function's own
	// assumption. api-models-aws's own real tree is nowhere near that
	// (430 real service files, confirmed live), but a silently truncated
	// catalog would look like a real, complete list while actually
	// missing an unknown number of real AWS services -- a real, honest
	// failure here beats that.
	if tree.Truncated {
		return nil, fmt.Errorf("api-models-aws tree response was truncated by GitHub's own API -- the single recursive=1 call this function depends on no longer covers the whole real repo, needs a real paginated/per-directory walk instead of silently returning a partial catalog")
	}

	var out []CatalogEntry
	for _, e := range tree.Tree {
		if e.Type != "blob" || !strings.HasSuffix(e.Path, ".json") {
			continue
		}
		// Real, confirmed live shape: models/<service>/service/
		// <api-version>/<service>-<api-version>.json (model.go's own doc
		// comment has the full account). Anything else under models/ --
		// there is nothing else today, confirmed live, but a future repo
		// change adding a non-service file must not silently become a
		// bogus catalog entry -- is skipped, not guessed at.
		parts := strings.Split(e.Path, "/")
		if len(parts) != 5 || parts[0] != "models" || parts[2] != "service" {
			continue
		}
		out = append(out, CatalogEntry{
			ServiceSlug: parts[1],
			SchemaURL:   apiModelsAWSRawBase + e.Path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceSlug < out[j].ServiceSlug })
	return out, nil
}
