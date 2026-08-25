// Package typename holds the one real, shared type-name-combining rule
// every schema-source package needs -- extracted here (UBI-185) after
// the identical bug was found living twice, independently, in
// internal/resourcemap (OpenAPI/Swagger) and internal/discoverydoc (GCP
// Discovery Documents): a naive providerName+service+noun concatenation
// doubles a sub-domain's own token whenever a spec names its primary
// resource after the sub-domain itself. The Azure fix (UBI-158-era,
// resourcemap-only) verified clean against Azure/Kubernetes/GitHub/
// Datadog but never touched GCP, because GCP's Discovery-Docs path never
// called it -- it had its own, separate, still-doubling concatenation.
// One shared package, one real implementation, both real call sites
// import it -- the bug existing twice in two files is exactly how it
// survived the first fix.
package typename

import "strings"

// Combine joins providerName with service (optional) and noun into the
// final real type name, trimming a real, live-found overlap first:
// whenever providerName's own trailing token(s) already equal the
// leading token(s) of what would otherwise be appended, that repeat is
// dropped rather than concatenated -- a real, common REST/API pattern
// where the "primary" resource under a sub-domain is named after the
// sub-domain itself (confirmed live against Azure ARM specs: the
// response schema for the config entry azure_hdinsight_cluster is
// itself literally named "Cluster", so the naive
// providerName+"_"+noun join produced azure_hdinsight_cluster_cluster
// instead of the real, correct azure_hdinsight_cluster; GCP Discovery
// Documents hit the identical pattern one segment earlier -- the config
// entry google_billingbudgets combined with a doc.Name that is ITSELF
// "billingbudgets" produced google_billingbudgets_billingbudgets_budget
// instead of the real, correct google_billingbudgets_budget).
//
// Only a real, exact token-run overlap is trimmed -- a provider whose
// own noun happens to merely start with a similar-looking but DIFFERENT
// word is untouched; this is intentionally the same discipline
// splitQualifiedRefName/apiVersionPattern already apply elsewhere in
// this codebase (a real structural signal, never a fuzzy guess). When
// the overlap consumes the tail entirely (the Cluster/Workspace-style
// full-word case above), the real type name is providerName alone, no
// trailing separator.
func Combine(providerName, service, noun string) string {
	var tail []string
	if service != "" {
		tail = append(tail, strings.Split(service, "_")...)
	}
	tail = append(tail, strings.Split(noun, "_")...)

	providerTokens := strings.Split(providerName, "_")
	overlap := 0
	for k := 1; k <= len(providerTokens) && k <= len(tail); k++ {
		matches := true
		for i := 0; i < k; i++ {
			if providerTokens[len(providerTokens)-k+i] != tail[i] {
				matches = false
				break
			}
		}
		if matches {
			overlap = k
		}
	}
	tail = tail[overlap:]

	if len(tail) == 0 {
		return providerName
	}
	return providerName + "_" + strings.Join(tail, "_")
}
