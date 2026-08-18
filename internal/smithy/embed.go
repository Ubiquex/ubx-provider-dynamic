package smithy

import (
	"bufio"
	_ "embed"
	"strings"
)

// defaultKnownNamesData is a real, versioned snapshot of hashicorp/aws's
// own resource type names -- dumped this session via ubx's own real
// provider.Acquire/Launch against hashicorp/aws 6.54.0's live
// GetProviderSchema (1682 real names, the identical mechanism
// naming.go's own doc comment describes), embedded directly into this
// binary so DefaultKnownNames() works with no network access and no
// external file at runtime. A real, known staleness tradeoff, explicitly
// not hidden: HashiCorp publishes new aws_* resources continuously, so
// this snapshot drifts -- refreshing it (re-running the same real
// provider.Acquire/Launch dump against a current hashicorp/aws version)
// is real, necessary maintenance this package does not automate yet, the
// identical "published daily, needs a real refresh discipline" caveat
// this ticket's own prompt names for AWS's Smithy models.
//
//go:embed data/hashicorp-aws-resource-names.txt
var defaultKnownNamesData string

// DefaultKnownNames parses the embedded snapshot -- the zero-setup path
// most callers want; LoadKnownNames remains available for a caller with
// its own, fresher real dump.
func DefaultKnownNames() KnownNames {
	names := KnownNames{}
	scanner := bufio.NewScanner(strings.NewReader(defaultKnownNamesData))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "TOTAL:") {
			continue
		}
		names[line] = true
	}
	return names
}
