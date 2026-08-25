package typename

import "testing"

func TestCombine_TrimsRealOverlap(t *testing.T) {
	// Real, live-found Azure ARM cases (ubiquex-docs UBI: the
	// "azure_hdinsight_cluster_cluster" investigation) -- the response
	// schema for a sub-domain's own primary resource is often named
	// after the sub-domain itself. Full-word overlap collapses the
	// noun entirely; partial (leading-token-only) overlap keeps the
	// non-overlapping remainder.
	cases := []struct {
		provider, service, noun, want string
	}{
		{"azure_hdinsight_cluster", "", "cluster", "azure_hdinsight_cluster"},
		{"azure_synapse_workspace", "", "workspace", "azure_synapse_workspace"},
		{"azure_servicefabric_cluster", "", "cluster", "azure_servicefabric_cluster"},
		{"azure_servicefabric_application", "", "application_resource", "azure_servicefabric_application_resource"},
		{"azure_devtestlabs_dtl", "", "dtl_environment", "azure_devtestlabs_dtl_environment"},
		{"azure_loadtestservice_playwright", "", "playwright_workspace", "azure_loadtestservice_playwright_workspace"},
		// no real overlap -- untouched, matches the old, pre-fix behavior exactly.
		{"azure_datadog", "", "monitor_resource", "azure_datadog_monitor_resource"},
		{"kubernetes", "apps", "deployment", "kubernetes_apps_deployment"},
		// a real service-qualified case whose service ALSO overlaps the
		// provider's own trailing token.
		{"github_repos", "repos", "commit", "github_repos_commit"},
		// the real GCP Discovery Docs case this package was extracted
		// for (UBI-185): google_billingbudgets combined with a doc.Name
		// that is itself "billingbudgets" must collapse the same way
		// Azure's sub-domain-named-after-itself case does.
		{"google_billingbudgets", "billingbudgets", "budget", "google_billingbudgets_budget"},
	}
	for _, c := range cases {
		got := Combine(c.provider, c.service, c.noun)
		if got != c.want {
			t.Errorf("Combine(%q, %q, %q) = %q, want %q", c.provider, c.service, c.noun, got, c.want)
		}
	}
}

func TestCombine_NoFalsePositiveOnSimilarButDifferentWord(t *testing.T) {
	// A noun that merely LOOKS similar to the provider's own trailing
	// token, without an exact token match, must never be trimmed --
	// only a real, exact token-run overlap counts.
	got := Combine("azure_cluster", "", "clusters")
	want := "azure_cluster_clusters"
	if got != want {
		t.Errorf("Combine should not trim a non-exact token match, got %q, want %q", got, want)
	}
}
