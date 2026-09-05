// Command ubx-provider-dynamic is UBI-158's own Dynamic Provider: a real
// tfplugin v6 provider binary, launched by ubx exactly like any HashiCorp
// provider (provider.Launch, zero special-casing), that derives its own
// resource schema and CRUD behavior at runtime from a real OpenAPI 3.x
// spec instead of shipping hand-written, per-service Go code.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/ccapi"
	cfnserver "github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/server"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/discoverydoc"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/mixedserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/resourcemap"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
	smithyserver "github.com/ubiquex/ubx-provider-dynamic/internal/smithy/server"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy/wireexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/snapshot"
)

// dumpSignalsFlag is a real, plain CLI mode, deliberately NOT part of the
// tfplugin6 RPC surface at all -- go-plugin's own real handshake protocol
// already owns this process's stdout the moment tf6server.Serve starts,
// so a second, real, structured data channel (the constraint/enum signal
// data tfprotov6.SchemaAttribute has no wire field for -- see
// internal/schema/signals.go's own doc comment) can't ride the same
// connection. ubiquex's own cli/dynamicprovider.go launches this exact
// same binary a SECOND time, as a plain subprocess (not through
// provider.Launch's go-plugin handshake), with this flag set, and reads
// its real JSON stdout directly -- the identical real, already-written
// .ubx/config a normal schema-fetch launch uses, just a different real
// entrypoint into the same schema-loading code.
var dumpSignalsFlag = flag.Bool("dump-signals", false, "print real per-resource field enum/constraint signal data as JSON to stdout, instead of serving a tfplugin6 provider, and exit")

// dumpNamespacesFlag is dumpSignalsFlag's own real sibling for a
// genuinely different KIND of signal -- not a per-field constraint (that
// stays FieldSignal's own job), but a per-RESOURCE-TYPE real service
// identity the wire type name alone doesn't carry (UBI-98's own root
// cause: sdk/codegen/ir.ServiceAndLocalName only ever sees the flat
// wire type string, e.g. "aws_instance", and has to mechanically GUESS
// at a namespace by splitting it -- guessing wrong for AWS's own
// historical EC2/VPC bare-name resources and any multi-word real
// service name, both confirmed live this session). Real, authoritative
// per-source-type identity, not derived: CloudFormation's own real
// namespace field (schema_source = "cloudformation", what's actually
// live in production) and Smithy's own real endpointPrefix trait
// (schema_source = "smithy", for the future data-source side) --
// verified live this session to agree for 178 of 181 real overlapping
// resources (98.3%), the 3 exceptions being the same real cross-service
// collisions already found by other means, not a new disagreement.
// OpenAPI/Discovery-Doc sourced providers (Azure/GCP/Kubernetes/GitHub/
// Datadog) never hit this problem at all -- confirmed live, zero true
// mismatches across all 1,096 real Azure and 1,543 real Google wire
// types -- so they emit a real, honest empty map, the identical "not
// needed here" answer dumpSignalsFlag already gives for a genuinely
// out-of-scope source.
var dumpNamespacesFlag = flag.Bool("dump-namespaces", false, "print real per-resource-type service-identity data (CloudFormation's own namespace field, Smithy's own endpointPrefix trait) as JSON to stdout, instead of serving a tfplugin6 provider, and exit")

// generateSnapshotGroupFlag is a real, plain CLI mode mirroring
// dumpSignalsFlag's own established shape: fetch every real
// [dynamic_providers.<name>] member group-members names, ONE real time
// each, verify each needs no further network access, and write ONE real,
// frozen, whole-group internal/snapshot.Snapshot (its own Members map,
// UBI-182's container format) to the given path -- never serves a
// tfplugin6 provider. UBI-182: replaces the old single-member
// --generate-snapshot entirely -- a provider's real published identity
// (ubiquex's own [dynamic_provider_groups.<x>]'s repo_name) is a GROUP,
// almost never one table, confirmed directly against the live config
// before this change (see internal/snapshot's own package doc comment).
var generateSnapshotGroupFlag = flag.String("generate-snapshot-group", "", "generate a real, frozen GROUP schema snapshot to this DIRECTORY (manifest.json plus one members/<name>.json per real member -- SaveSplit) instead of serving a tfplugin6 provider, and exit -- requires --group-repo-name and --group-members")

// groupRepoNameFlag is generateSnapshotGroupFlag's own required sibling:
// the group's own real published identity (ubiquex's own
// [dynamic_provider_groups.<x>]'s own repo_name, e.g. "kubernetes",
// "aws") -- becomes the written Snapshot's own Provider field, matching
// the real github.com/ubiquex/ubx-schema-<Provider> repo this snapshot
// IS.
var groupRepoNameFlag = flag.String("group-repo-name", "", "the group's own real published identity (e.g. \"kubernetes\") -- required with --generate-snapshot-group")

// groupMembersFlag is generateSnapshotGroupFlag's own other required
// sibling: a comma-separated list of real [dynamic_providers.<name>]
// table names this group bundles (e.g. "kubernetes,kubernetes_ds") --
// each name is looked up in THIS process's own .ubx/config (config.Load,
// not LoadNamed -- every named member needs its own real table, not just
// one active name).
var groupMembersFlag = flag.String("group-members", "", "comma-separated [dynamic_providers.<name>] table names this group bundles -- required with --generate-snapshot-group")

// groupExcludeFlag is generateSnapshotGroupFlag's own optional sibling
// carrying UBI-182's own real precedence record: a JSON object, member
// name -> real wire type names that member's own copy loses a
// collision for (the SAME real shape and the SAME real judgment
// ubiquex's own [dynamic_provider_groups.<x>.exclude] table already
// records for codegen -- e.g. Datadog's own real v1/v2 collisions,
// where v1's richer version wins both times). Recorded verbatim onto
// the written Snapshot's own Exclude field (internal/snapshot.AssembleGroup)
// -- this flag is where a caller who already knows a real collision
// (from that SAME config) passes the judgment through, not where one
// gets invented. Omit for a group with no known real collisions.
var groupExcludeFlag = flag.String("group-exclude", "", "JSON object of member name -> real wire type names that member's own copy loses a collision for (mirrors ubiquex's own dynamic_provider_groups exclude table) -- optional with --generate-snapshot-group")

// prevSnapshotFlag is generateSnapshotGroupFlag's own real, optional
// sibling: the PRIOR real group container (if any) to diff the freshly-
// fetched members against, so the new group's own Version is
// mechanically derived (internal/snapshot.AssembleGroup) rather than
// left for a human to guess. Omit for a group's first-ever snapshot.
var prevSnapshotFlag = flag.String("prev-snapshot", "", "DIRECTORY of the prior real GROUP snapshot (manifest.json plus members/) to diff against when deriving the new one's own version (omit for a group's first-ever snapshot)")

// allowDevBinaryFlag is generateSnapshotGroupFlag's own real safety
// gate: refuses to write a snapshot whose own GeneratedByBinaryVersion would
// be the unstamped default "dev" unless explicitly overridden. UBI-229
// follow-up -- four of this org's seven published group snapshots
// (azure, google, aws, datadog) reached "dev" in their own real,
// committed, merged manifest.json with zero friction, because a
// locally-built (unreleased) binary produces a snapshot that is
// byte-for-byte indistinguishable in review from one built by a real,
// tagged release, aside from this one field nobody was checking.
// "dev" is not a real version -- it cannot be resolved by
// provider.AcquireDynamicProviderBinary, and it defeats the entire
// real, unforgeable link GeneratedByBinaryVersion exists to record (see
// BinaryVersion's own doc comment). Local, unreleased builds remain a
// real, legitimate way to iterate on generation before a release
// exists -- this flag is the deliberate, visible escape hatch for
// that case, not a ban on it: passing it is a conscious, grep-able
// choice, unlike today's silent default.
var allowDevBinaryFlag = flag.Bool("allow-dev-binary", false, "allow writing a snapshot whose own min_binary_version would be the unstamped \"dev\" default -- refused by default, since a dev-stamped snapshot cannot be traced to any real, acquirable binary")

// dumpGroupSummaryFlag is a real, plain CLI mode for one real, narrow
// question: how many real resource/data-source types does this GROUP's
// own real merge actually produce, right now, without ever printing a
// single one of its own internal member names. Built for a real, live
// finding, not speculatively: this org's own ubx-schema-* repos'
// publish.yml workflows used to bake manifest.json's own member NAMES
// (e.g. "kubernetes, kubernetes_ds") directly into a real, published
// release's own notes -- inviting a reader to pin one of those internal
// names directly (kubernetes_ds, datadog_v2_ds), exactly the two-pin/
// four-pin shape the whole collapse exists to make unnecessary. Real
// counts, mechanically computed from the SAME Merge<Source>Group
// functions runServeSnapshot already uses, are the honest, correct
// thing to publish instead -- this flag exists so a workflow can ask
// for them without re-implementing any real translation logic in a
// shell script.
var dumpGroupSummaryFlag = flag.String("dump-group-summary", "", "print {\"provider\":..,\"version\":..,\"resources\":N,\"data_sources\":N} as JSON for the real GROUP snapshot at this DIRECTORY, and exit -- never names an internal member")

// nameEnvVar is how a launched process learns which [dynamic_providers.<name>]
// table in .ubx/config is its own -- see internal/config's own doc comment
// for why this can't come from the ConfigureProvider RPC. provider.Launch's
// own WithEnv option (already generic, already exists, not added for this
// binary specifically) is Phase 5's real integration mechanism for setting
// this; standalone/validation runs (this ticket's own Phase 1 proof, and
// any manual invocation) set it directly.
const nameEnvVar = "UBX_DYNAMIC_PROVIDER_NAME"

// snapshotPathEnvVar, when set, is THE real fix for the problem this
// whole package exists to solve: a launched process serves a real,
// already-generated snapshot instead of fetching schema_url live -- zero
// network calls at schema resolution time. Checked BEFORE .ubx/config is
// even loaded (unlike every other real schema_source branch below):
// snapshot-driven serving needs none of that table's own
// schema_source/schema_url fields, only Auth/BaseURL/Retry/Timeouts,
// which the snapshot itself already carries (Snapshot's own doc comment
// covers why). UBI-182: servable this way for all four real schema
// sources (was openapi only) -- runServeSnapshot's own switch dispatches
// to the source-appropriate Load<Source> and server construction.
const snapshotPathEnvVar = "UBX_SNAPSHOT_PATH"

// versionFlag prints snapshot.BinaryVersion (this exact build's own
// real, released version -- "dev" for a local, unreleased build) and
// exits -- UBI-194's own real, minimal operational visibility: the same
// real value every snapshot this build generates now stamps into its
// own GeneratedByBinaryVersion, so a real release always has a way to confirm
// what it actually is without decoding a snapshot's own manifest.json.
var versionFlag = flag.Bool("version", false, "print this build's own real, released version (snapshot.BinaryVersion) and exit")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()

	if *versionFlag {
		fmt.Println(snapshot.BinaryVersion)
		return nil
	}

	// Group generation is checked BEFORE the single-name requirement
	// below: it bundles MULTIPLE real [dynamic_providers.<name>] tables
	// into one group container, so it has no single active
	// UBX_DYNAMIC_PROVIDER_NAME to require at all.
	if *generateSnapshotGroupFlag != "" {
		return runGenerateSnapshotGroup(*generateSnapshotGroupFlag, *groupRepoNameFlag, *groupMembersFlag, *prevSnapshotFlag, *groupExcludeFlag, *allowDevBinaryFlag)
	}

	// Same real reason as group generation above -- a group summary
	// inspects the WHOLE group container directly, no single active
	// UBX_DYNAMIC_PROVIDER_NAME to require.
	if *dumpGroupSummaryFlag != "" {
		return runDumpGroupSummary(*dumpGroupSummaryFlag)
	}

	name := os.Getenv(nameEnvVar)
	if name == "" {
		return fmt.Errorf("%s must be set to the [dynamic_providers.<name>] table this process represents", nameEnvVar)
	}

	if snapPath := os.Getenv(snapshotPathEnvVar); snapPath != "" {
		// UBI-182: --dump-signals/--dump-namespaces against a pinned
		// snapshot -- previously unreachable (this branch always fell
		// straight to runServeSnapshot, which only ever opens a real
		// tfplugin6 serve, never checks either flag) -- confirmed the
		// real, root gap ubiquex's own cli/dynamicprovider.go named
		// (dynamicProviderSignals' own doc comment: "--dump-signals'
		// own plain-subprocess mode was never wired to snapshot
		// loading"). Checked here, before runServeSnapshot, mirroring
		// every live-fetch branch's own real ordering (build resources,
		// THEN check either flag, THEN fall through to real serving).
		if *dumpSignalsFlag {
			return runDumpSignalsFromSnapshot(name, snapPath)
		}
		if *dumpNamespacesFlag {
			return runDumpNamespacesFromSnapshot(name, snapPath)
		}
		return runServeSnapshot(name, snapPath)
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}

	cfg, err := config.LoadNamed(dir, name)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// wireName is what gets baked into every resource type name this
	// process produces -- config.Provider.WireName's own doc comment has
	// the full real reason this can differ from name (the table's own
	// key, used for --only targeting and process identity below,
	// unchanged). Defaults to name, matching every prior phase's
	// behavior for the common case where no entry needs the two decoupled.
	wireName := name
	if cfg.WireName != "" {
		wireName = cfg.WireName
	}

	// UBI-158 Phase 4 Checkpoint 2: real per-protocol wire execution
	// (internal/smithy/wireexec) and real SigV4 signing (internal/auth)
	// now both exist and are verified against real AWS -- this binary
	// actually serves a Smithy-sourced provider, completing what
	// Checkpoint 1 discovered/translated/named but deliberately left
	// refusing to serve.
	if cfg.SchemaSource == config.SchemaSourceSmithy {
		smithyDoc, err := smithy.Load(cfg.SchemaURL)
		if err != nil {
			return fmt.Errorf("load Smithy model: %w", err)
		}
		svc, err := smithy.FindService(smithyDoc)
		if err != nil {
			return fmt.Errorf("find Smithy service: %w", err)
		}

		// UBI-186's own real "later step" -- see smithy.BuildDataSources'
		// own doc comment -- a data-source-mode entry never falls through
		// to the resource path below: it's schema-only (GetProviderSchema
		// is the only real RPC `ubx sdk gen` calls), needs no real wire
		// execution/auth/target_prefix at all, and returns before any of
		// that gets built.
		if cfg.DataSources {
			builtDS, dsNotes, err := smithy.BuildDataSources(smithyDoc, wireName, svc, cfg.DataSourceNamespace)
			if err != nil {
				return fmt.Errorf("build Smithy data source schemas: %w", err)
			}
			for _, n := range dsNotes {
				fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", n)
			}
			// Real, deliberate divergence from the resource branch's own
			// identical-shaped check just below (len(built) == 0 is a
			// hard error there): zero real, unclaimed read-shaped
			// operations is a normal, expected outcome for plenty of
			// real, small AWS services -- every one of their own real
			// Get/Describe/List operations already claimed as some
			// resource's own ReadOperationID, nothing left over -- not a
			// sign anything is wrong the way zero CRUD-shaped resources
			// at all would be. Found live, not assumed: this session's
			// own real, full 430-service group sweep hard-failed on the
			// very first small service that legitimately had none
			// (account), aborting the ENTIRE group launch
			// (mergeDynamicProviderGroupMembers' own fail-loud-on-one-
			// member-error behavior, ubiquex's cli/sdk.go) before this
			// fix. Serves an empty DataSources map instead -- a real,
			// honest "this service has none," not a launch failure.
			if len(builtDS) == 0 {
				fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [smithy] no real, unclaimed read-shaped operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
			} else {
				fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [smithy] discovered %d data source(s) from %s (protocol: %s)\n", len(builtDS), cfg.SchemaURL, svc.Protocol)
			}

			if *dumpSignalsFlag {
				fmt.Fprintln(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = \"smithy\" data sources -- emitting an empty, real result, not an error")
				return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
			}

			if *dumpNamespacesFlag {
				// BuiltDataSource.RealNamespace, not the raw
				// DataSourceCandidate.Namespace -- real, live-found bug
				// (this session's own full 429-service AWS sweep) fixed
				// here: reporting the raw, un-sanitized value let a real
				// hyphen+dot-carrying namespace (a2i-runtime.sagemaker)
				// flow straight into an invalid generated Go package
				// name. RealNamespace is the exact same sanitized string
				// already folded into each entry's own WireType, so
				// ir.ServiceAndLocalNameForType's own token-match logic
				// (ubiquex) actually finds it.
				out := make(map[string]string, len(builtDS))
				for wireType, ds := range builtDS {
					out[wireType] = ds.RealNamespace
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}

			server := &smithyserver.Server{
				ProviderName: name,
				DataSources:  builtDS,
				Model:        smithyDoc,
			}
			return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
				return server
			})
		}

		built, notes, err := smithy.Build(smithyDoc, wireName, smithy.DefaultKnownNames())
		if err != nil {
			return fmt.Errorf("build Smithy resource schemas: %w", err)
		}
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", n)
		}
		if len(built) == 0 {
			return fmt.Errorf("no CRUD-shaped resources discovered in %s -- nothing to serve", cfg.SchemaURL)
		}
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: discovered %d resources from %s (protocol: %s)\n", len(built), cfg.SchemaURL, svc.Protocol)
		for hcName, res := range built {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic:   %s (naming: %s)\n", hcName, res.NameStrategy)
		}

		// UBI-182's own real collapse: this entry's own data_sources was
		// NOT set to restrict it to data-sources-only above, so build its
		// real data sources too, from the SAME already-loaded model --
		// see config.Provider.DataSources' own doc comment for the full
		// real reasoning (every real source's own DiscoverDataSources
		// already derives its "unclaimed" set independently, no second
		// live fetch, nothing in the wire protocol ever required a
		// second entry). Zero real, unclaimed read-shaped operations is
		// a normal, expected outcome, matching the data-source-only
		// branch's own identical real finding above -- not an error.
		builtDS, dsNotes, err := smithy.BuildDataSources(smithyDoc, wireName, svc, cfg.DataSourceNamespace)
		if err != nil {
			return fmt.Errorf("build Smithy data source schemas: %w", err)
		}
		for _, n := range dsNotes {
			fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", n)
		}
		if len(builtDS) == 0 {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [smithy] no real, unclaimed read-shaped operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
		} else {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [smithy] discovered %d data source(s) from %s (protocol: %s)\n", len(builtDS), cfg.SchemaURL, svc.Protocol)
		}

		if *dumpSignalsFlag {
			// Real, honest, named gap: Smithy's own real trait system
			// (smithy.api#length/#range/#pattern, its own enum trait) is
			// a genuinely separate real constraint/enum source from
			// OpenAPI's -- internal/schema/signals.go's own CollectSignals
			// only ever walks *openapi3.Schema. Not yet built for Smithy;
			// an empty, real (not error) JSON object is the honest
			// "no signal available for this source yet" answer, matching
			// this whole file's own "skip, don't fail" discipline for a
			// genuinely partial-coverage case. Unaffected by builtDS --
			// data-source signal collection was never implemented either
			// (the now-removed, data-sources-only branch's own identical
			// stub above), so there is nothing real to fold in here.
			fmt.Fprintln(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = \"smithy\" -- emitting an empty, real result, not an error")
			return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
		}

		if *dumpNamespacesFlag {
			// smithy.ServiceNamespace is the real, single per-service
			// identity every resource this Build call discovered shares --
			// the same real string naming.go's own Resolve doc comment
			// already establishes as HashiCorp's own real aws_<prefix>_*
			// convention's source of truth. Falls back to ArnNamespace
			// when EndpointPrefix is blank (93 of 430 real services,
			// confirmed live) -- the identical real fallback
			// DiscoverDataSources' own Namespace field uses, one shared
			// implementation, not two that could drift apart. Data
			// sources fold in their own real, already-sanitized
			// RealNamespace (matching the data-sources-only branch's
			// own identical logic above) -- a resource and a data
			// source sharing the identical wire type name is real and
			// expected (aws_instance is both, see DataSourceCandidate's
			// own doc comment), so this only fails loud on a genuine
			// disagreement between the two, never on the shared-key case
			// itself.
			out := make(map[string]string, len(built)+len(builtDS))
			for hcName := range built {
				out[hcName] = smithy.ServiceNamespace(svc)
			}
			for wireType, ds := range builtDS {
				if existing, ok := out[wireType]; ok && existing != ds.RealNamespace {
					return fmt.Errorf("wire type %q claimed by both a resource (namespace %q) and a data source (namespace %q) with disagreeing namespaces -- a real ambiguity, not resolved silently", wireType, existing, ds.RealNamespace)
				}
				out[wireType] = ds.RealNamespace
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		}

		if (svc.Protocol == smithy.ProtocolAWSJSON10 || svc.Protocol == smithy.ProtocolAWSJSON11) && cfg.TargetPrefix == "" {
			return fmt.Errorf("schema_source = %q: service protocol %s requires target_prefix in [dynamic_providers.%s] config -- see config.Provider.TargetPrefix's own doc comment for why AWS's real Smithy model carries no such field itself", cfg.SchemaSource, svc.Protocol, name)
		}

		authenticator, err := auth.Build(cfg.Auth.Type, cfg.Auth.Params)
		if err != nil {
			return fmt.Errorf("build authenticator: %w", err)
		}
		retryPolicy, err := dynserver.ResolveRetryPolicy(cfg.Retry)
		if err != nil {
			return fmt.Errorf("resolve retry policy: %w", err)
		}
		restClient := restexec.NewClient(cfg.BaseURL, authenticator)
		restClient.Retry = retryPolicy

		wireClient := &wireexec.Client{
			Rest:         restClient,
			Model:        smithyDoc,
			Service:      svc,
			TargetPrefix: cfg.TargetPrefix,
		}
		server := &smithyserver.Server{
			ProviderName: name,
			Resources:    built,
			DataSources:  builtDS,
			Model:        smithyDoc,
			Wire:         wireClient,
		}
		return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
			return server
		})
	}

	// discoverydoc: the third real schema-source format, GCP's own
	// Discovery Documents (that package's own doc comment has the full
	// real research). Schema-layer only, the identical real precedent
	// this binary's own Kubernetes checkpoint set (discover + translate,
	// prove it live via GetProviderSchema; real REST wire execution --
	// Configure/Create/Read/Update/Delete against a real GCP endpoint --
	// is separate, deliberately not attempted here, the same staging
	// Smithy's own real Phase 1 -> Phase 4 already used). dynserver.Server
	// is reused UNCHANGED for GetProviderSchema (confirmed by direct
	// inspection: that RPC reads ONLY ResourceType.Schema, nothing else);
	// every OTHER real RPC on this Server would need Client/ObjectType/
	// PathParamAttr this branch never populates -- a real, honest gap for
	// the future execution checkpoint, not exercised by this binary's own
	// real schema-fetch/--dump-signals usage today.
	if cfg.SchemaSource == config.SchemaSourceDiscoveryDoc {
		ddoc, err := discoverydoc.Load(cfg.SchemaURL)
		if err != nil {
			return fmt.Errorf("load discovery document: %w", err)
		}

		// UBI-186's own real "later step" for discoverydoc -- see
		// discoverydoc.BuildDataSources' own doc comment. Mirrors the
		// Smithy data-source branch above: schema-only, no real wire
		// execution needed at all, returns before any of that gets
		// built.
		if cfg.DataSources {
			// wireName, not bare name -- real, live-found bug this
			// session: the resource branch below uses bare name because
			// every existing real [dynamic_providers.google_<api>]
			// entry's own table key already IS its real, correct
			// provider identity (no separate data-source-mode sibling
			// entry existed to need a distinct key from). A
			// data-source-mode entry needs a TOML key DISTINCT from its
			// own resource-mode sibling (TOML tables require unique
			// keys) -- using that distinct key as providerName directly
			// leaks it into the wire type's own second token
			// (ir.ServiceAndLocalNameForType's own mechanical fallback
			// split reads tokens[1] as "service" positionally, with no
			// awareness of what the token actually means), corrupting
			// every data source's own derived service/local split. Real,
			// live-verified against accesscontextmanager: a
			// "google_data_accesscontextmanager" table key produced
			// wire type "google_data_accesscontextmanager_operation",
			// mis-splitting as service="data" instead of the real,
			// intended "accesscontextmanager". wireName lets the config
			// set wire_name = "google_accesscontextmanager" (the real,
			// correct identity, matching the resource sibling's own
			// table key exactly) independently of whatever distinct key
			// TOML uniqueness requires.
			builtDS, dsNotes, err := discoverydoc.BuildDataSources(ddoc, wireName, cfg.VersionQualifier)
			if err != nil {
				return fmt.Errorf("build discovery-doc data source schemas: %w", err)
			}
			for _, n := range dsNotes {
				fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] %s: %s\n", n.Path, n.Detail)
			}
			if len(builtDS) == 0 {
				// Real, expected outcome for plenty of small real APIs
				// (every GET already claimed by a resource) -- see the
				// Smithy branch's own identical, real, live-found
				// finding above; not an error.
				fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] no real, unclaimed GET operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
			} else {
				fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] discovered %d data source(s) from %s\n", len(builtDS), cfg.SchemaURL)
			}

			if *dumpSignalsFlag {
				fmt.Fprintln(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = \"discovery_docs\" data sources -- emitting an empty, real result, not an error")
				return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
			}
			if *dumpNamespacesFlag {
				// Identical real reason the resource branch below
				// returns empty here: every discoverydoc-sourced wire
				// type (data source or resource alike) already carries
				// its own real service identity directly in the wire
				// type itself (typename.Combine) -- nothing for this
				// override to add.
				return json.NewEncoder(os.Stdout).Encode(map[string]string{})
			}

			dsResources := make(map[string]*dynserver.ResourceType, len(builtDS))
			for typeName, ds := range builtDS {
				dsResources[typeName] = &dynserver.ResourceType{Schema: ds.Schema}
			}
			server := &dynserver.Server{ProviderName: name, DataSources: dsResources}
			return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
				return server
			})
		}

		built, notes, err := discoverydoc.Build(ddoc, name, cfg.VersionQualifier)
		if err != nil {
			return fmt.Errorf("build resource schemas: %w", err)
		}
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] %s: %s\n", n.Path, n.Detail)
		}
		if len(built) == 0 {
			return fmt.Errorf("no CRUD-shaped resources discovered in %s -- nothing to serve", cfg.SchemaURL)
		}

		// UBI-182's own real collapse -- see the Smithy branch's own
		// identical comment above for the full real reasoning. wireName,
		// not bare name -- matches the data-sources-only branch's own
		// real, documented reason above (a data-source-mode build needs
		// the SHARED identity, which this entry's own wireName already
		// is now that no distinct sibling table exists to force a
		// different TOML key).
		builtDS, dsNotes, err := discoverydoc.BuildDataSources(ddoc, wireName, cfg.VersionQualifier)
		if err != nil {
			return fmt.Errorf("build discovery-doc data source schemas: %w", err)
		}
		for _, n := range dsNotes {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] %s: %s\n", n.Path, n.Detail)
		}
		if len(builtDS) == 0 {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] no real, unclaimed GET operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
		} else {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [discoverydoc] discovered %d data source(s) from %s\n", len(builtDS), cfg.SchemaURL)
		}

		if *dumpSignalsFlag {
			// Resource signals only -- data-source signal collection was
			// never implemented for discoverydoc either (the now-removed,
			// data-sources-only branch's own identical stub above), so
			// there is nothing real to fold in from builtDS.
			out := make(map[string]map[string]*uschema.FieldSignal, len(built))
			for typeName, br := range built {
				out[typeName] = br.Signals
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		}

		if *dumpNamespacesFlag {
			// UBI-98: real, checked, confirmed live this session -- every
			// Discovery-Doc-sourced wire type already carries its own real
			// service identity directly in the wire type itself (each
			// [dynamic_providers.google_<api>] entry's own name feeds
			// typename.Combine), so sdk/codegen/ir's own mechanical split
			// already recovers it correctly (zero true mismatches across
			// all 1,543 real Google wire types, verified live). Nothing
			// for this override to add here -- a real, honest empty
			// result, not a guess dressed up as one -- true for a
			// data-source-mode wire type too (typename.Combine is the
			// identical real construction on both sides).
			return json.NewEncoder(os.Stdout).Encode(map[string]string{})
		}

		resources := make(map[string]*dynserver.ResourceType, len(built))
		for typeName, br := range built {
			resources[typeName] = &dynserver.ResourceType{Schema: br.Schema}
		}
		dataSources := make(map[string]*dynserver.ResourceType, len(builtDS))
		for typeName, ds := range builtDS {
			dataSources[typeName] = &dynserver.ResourceType{Schema: ds.Schema}
		}
		server := &dynserver.Server{ProviderName: name, Resources: resources, DataSources: dataSources}
		return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
			return server
		})
	}

	// cloudformation: AWS's real, published CloudFormation resource-
	// provider schema registry (internal/cloudformation's own doc comment
	// has the full real research) -- real, full execution, not
	// schema-layer-only like discoverydoc above: Cloud Control API's own
	// real, fixed operation set (internal/cloudformation/ccapi) makes
	// Create/Read/Update/Delete/List real against ANY real CFN-registered
	// resource type, generic across all of them (no per-service wire
	// protocol work needed, unlike Smithy).
	if cfg.SchemaSource == config.SchemaSourceCloudFormation {
		files, err := cloudformation.Fetch(cfg.SchemaURL)
		if err != nil {
			return fmt.Errorf("fetch CloudFormation registry: %w", err)
		}
		built, notes, err := cloudformation.Build(files, smithy.DefaultKnownNames())
		if err != nil {
			return fmt.Errorf("build CloudFormation resource schemas: %w", err)
		}
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [cloudformation] %s: %s\n", n.TypeName, n.Detail)
		}
		if len(built) == 0 {
			return fmt.Errorf("no resources discovered in %s -- nothing to serve", cfg.SchemaURL)
		}
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: discovered %d real CFN resources from %s\n", len(built), cfg.SchemaURL)

		if *dumpSignalsFlag {
			// Real, honest, named gap mirroring Smithy's own identical one
			// above: CFN's own real schema dialect has no separate
			// constraint/enum trait system this package extracts signals
			// from yet.
			fmt.Fprintln(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = \"cloudformation\" -- emitting an empty, real result, not an error")
			return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
		}

		if *dumpNamespacesFlag {
			// br.TypeName is the real, full "AWS::<Namespace>::<Type>" CFN
			// typeName -- splitTypeName is this same package's own real,
			// already-tested extraction (cloudformation.go), never a
			// separate reimplementation. Lowercased: CFN's own real
			// namespace strings are PascalCase compounds ("ApiGateway",
			// "AmazonMQ") with no internal separator, and sdk/codegen/ir's
			// own token-accumulation match (UBI-98) compares against a
			// wire type's own already-lowercase snake_case tokens.
			out := make(map[string]string, len(built))
			for resourceTypeName, br := range built {
				ns, _ := cloudformation.SplitTypeName(br.TypeName)
				out[resourceTypeName] = strings.ToLower(ns)
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		}

		authenticator, err := auth.Build(cfg.Auth.Type, cfg.Auth.Params)
		if err != nil {
			return fmt.Errorf("build authenticator: %w", err)
		}
		retryPolicy, err := dynserver.ResolveRetryPolicy(cfg.Retry)
		if err != nil {
			return fmt.Errorf("resolve retry policy: %w", err)
		}
		restClient := restexec.NewClient(cfg.BaseURL, authenticator)
		restClient.Retry = retryPolicy

		server := cfnserver.New(name, built, &ccapi.Client{Rest: restClient})
		return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
			return server
		})
	}

	var doc *openapi3.T
	if cfg.RedoclyBundle {
		doc, err = openapi.LoadWithRedoclyBundle(cfg.SchemaURL, cfg.Name)
	} else {
		doc, err = openapi.Load(cfg.SchemaURL)
	}
	if err != nil {
		return fmt.Errorf("load OpenAPI spec: %w", err)
	}

	// UBI-186's own real "later step" for the openapi source (Kubernetes/
	// GitHub/Datadog) -- see resourcemap.BuildDataSources' own doc
	// comment. Mirrors the Smithy/discoverydoc data-source branches
	// above: schema-only, no real wire execution needed, returns before
	// any of that gets built. wireName (not bare name), matching the
	// resource branch's own dynserver.Build(doc, wireName, cfg) call
	// just below -- see the discoverydoc branch's own doc comment for
	// the real, live-found bug using bare name here would reproduce.
	if cfg.DataSources {
		builtDS, dsNotes, err := resourcemap.BuildDataSources(doc, wireName)
		if err != nil {
			return fmt.Errorf("build data source schemas: %w", err)
		}
		for _, n := range dsNotes {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] %s: %s\n", n.Path, n.Detail)
		}
		if len(builtDS) == 0 {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] no real, unclaimed GET operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
		} else {
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] discovered %d data source(s) from %s\n", len(builtDS), cfg.SchemaURL)
		}

		if *dumpSignalsFlag {
			fmt.Fprintln(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = \"openapi\" data sources -- emitting an empty, real result, not an error")
			return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
		}
		if *dumpNamespacesFlag {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{})
		}

		dsResources := make(map[string]*dynserver.ResourceType, len(builtDS))
		for typeName, ds := range builtDS {
			dsResources[typeName] = &dynserver.ResourceType{Schema: ds.Schema}
		}
		server := &dynserver.Server{ProviderName: name, DataSources: dsResources}
		return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
			return server
		})
	}

	resources, notes, err := dynserver.Build(doc, wireName, cfg)
	if err != nil {
		return fmt.Errorf("build resource schemas: %w", err)
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", n)
	}
	if len(resources) == 0 {
		return fmt.Errorf("no CRUD-shaped resources discovered in %s -- nothing to serve", cfg.SchemaURL)
	}

	// UBI-182's own real collapse -- see the Smithy branch's own
	// identical comment above for the full real reasoning.
	builtDS, dsNotes, err := resourcemap.BuildDataSources(doc, wireName)
	if err != nil {
		return fmt.Errorf("build data source schemas: %w", err)
	}
	for _, n := range dsNotes {
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] %s: %s\n", n.Path, n.Detail)
	}
	if len(builtDS) == 0 {
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] no real, unclaimed GET operations discovered in %s -- serving zero data sources, not an error\n", cfg.SchemaURL)
	} else {
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: [openapi] discovered %d data source(s) from %s\n", len(builtDS), cfg.SchemaURL)
	}

	if *dumpSignalsFlag {
		// Resource signals only -- data-source signal collection was
		// never implemented for openapi either (the now-removed,
		// data-sources-only branch's own identical stub above), so
		// there is nothing real to fold in from builtDS.
		out := make(map[string]map[string]*uschema.FieldSignal, len(resources))
		for typeName, rt := range resources {
			out[typeName] = rt.Signals
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	if *dumpNamespacesFlag {
		// UBI-98: the identical real "already correct by construction"
		// finding as the discoverydoc branch above, verified live this
		// session against all 1,096 real Azure wire types (zero true
		// mismatches) and structurally true for Kubernetes/GitHub/Datadog
		// too (deriveNoun's own real, schema-qualified-name-derived
		// service segment, not a re-split of a foreign legacy name) --
		// true for a data-source-mode wire type too (deriveNoun is the
		// identical real construction on both sides).
		//
		// UBI-222: this unconditional empty result was real for every
		// provider that never set NamespaceFromTags (true above), but
		// wrong for one that does -- confirmed live, Cloudflare: this
		// branch is the ONLY real code path a genuinely new, still-live
		// (unpinned) OpenAPI provider's own onboarding ever exercises
		// before a real ubx-schema-<name> snapshot exists, yet
		// NamespaceFromTags was only ever wired into
		// snapshot.namespacesFromTags -- the snapshot-GENERATION path
		// (GenerateOpenAPIMember, --generate-snapshot-group), never this
		// one. hop 3 of onboard-provider.md asks a session to check real
		// grouping quality "before moving on" to creating that schema
		// repo -- unreachable for an OpenAPI-sourced provider as this
		// branch stood, since the one flag meant to fix bad grouping had
		// no effect until a snapshot already existed, several hops later
		// than the check it was meant to inform. Mirrors
		// snapshot.namespacesFromTags' own real algorithm exactly (first
		// tag wins, resources processed before data sources, never
		// overwrite an entry already placed) rather than duplicating it
		// by hand, calling resourcemap.Discover/DiscoverDataSources a
		// second time here since dynserver.Build/resourcemap.BuildDataSources
		// already discarded each candidate's own Tags by the time
		// `resources`/`builtDS` reached this point.
		out := map[string]string{}
		if cfg.NamespaceFromTags {
			liveResources, _, err := resourcemap.Discover(doc, wireName)
			if err != nil {
				return fmt.Errorf("resource mapping for namespace derivation: %w", err)
			}
			for _, r := range liveResources {
				if r.ReadOperation == nil || len(r.ReadOperation.Tags) == 0 {
					continue
				}
				if _, exists := out[r.TypeName]; exists {
					continue
				}
				out[r.TypeName] = snapshot.TagToNamespace(r.ReadOperation.Tags[0])
			}
			liveDataSources, _, err := resourcemap.DiscoverDataSources(doc, wireName)
			if err != nil {
				return fmt.Errorf("data source mapping for namespace derivation: %w", err)
			}
			for _, ds := range liveDataSources {
				if ds.Operation == nil || len(ds.Operation.Tags) == 0 {
					continue
				}
				if _, exists := out[ds.TypeName]; exists {
					continue
				}
				out[ds.TypeName] = snapshot.TagToNamespace(ds.Operation.Tags[0])
			}
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	authenticator, err := auth.Build(cfg.Auth.Type, cfg.Auth.Params)
	if err != nil {
		return fmt.Errorf("build authenticator: %w", err)
	}

	retryPolicy, err := dynserver.ResolveRetryPolicy(cfg.Retry)
	if err != nil {
		return fmt.Errorf("resolve retry policy: %w", err)
	}
	client := restexec.NewClient(cfg.BaseURL, authenticator)
	client.Retry = retryPolicy

	// UBI-193: Client is now per-resource-type (dynserver.ResourceType's
	// own field, not Server's) -- a live, non-pinned launch always
	// serves exactly ONE [dynamic_providers.<name>] entry, so every
	// real resource type here genuinely does share this one client,
	// unlike a pinned GROUP's own real members, which may not.
	for _, rt := range resources {
		rt.Client = client
	}

	server := &dynserver.Server{
		ProviderName: name,
		Resources:    resources,
		DataSources:  dataSourcesToResourceTypes(builtDS),
	}

	return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
		return server
	})
}

// buildMemberClient is UBI-193's own real, per-member client
// construction -- the exact same real steps runServeSnapshot used to
// perform ONCE, group-wide, before this fix (auth.Build, resolve the
// real retry policy, restexec.NewClient), now callable per real
// member so each one's own real BaseURL/Auth is used, never a
// different member's. A real member with Auth.Type == "" (Azure's own
// real 301-of-302 case) builds a real, legitimate "no authentication"
// client -- not an error; that member only fails for real once
// something actually tries to execute against it without real
// credentials, the correct, later failure point.
func buildMemberClient(m *snapshot.MemberSnapshot) (*restexec.Client, error) {
	authenticator, err := auth.Build(m.Auth.Type, m.Auth.Params)
	if err != nil {
		return nil, fmt.Errorf("build authenticator: %w", err)
	}
	retryPolicy, err := dynserver.ResolveRetryPolicy(m.Retry)
	if err != nil {
		return nil, fmt.Errorf("resolve retry policy: %w", err)
	}
	client := restexec.NewClient(m.BaseURL, authenticator)
	client.Retry = retryPolicy
	return client, nil
}

// runServeSnapshot is snapshotPathEnvVar's own real implementation -- the
// literal fix for the problem this whole package exists to solve. Loads
// a real, already-generated GROUP Snapshot from snapPath (snapshot.Load
// already runs CheckFormat, so an out-of-range schema_format refuses
// loudly right here, before any RPC serving begins), picks THIS
// process's own real member back out of the container by name (the
// SAME name every launch already receives via UBX_DYNAMIC_PROVIDER_NAME
// -- Snapshot.Member's own doc comment), re-derives that member's real
// resource-or-data-source map via the source-and-mode-appropriate
// Load<Source>Member (zero network -- the same real translation the
// live-fetch path runs, just fed frozen RawSpec bytes), and serves it
// through the IDENTICAL real server construction run()'s own matching
// live-fetch branch uses for that source+mode combination (confirmed by
// reading each live-fetch branch's own server construction, not assumed
// uniform). The only real difference from each source's own live-fetch
// branch is where Auth/BaseURL/Retry/WireName/TargetPrefix come from
// (the member's own fields, not a live .ubx/config fetch).
func runServeSnapshot(name, snapPath string) error {
	snap, err := snapshot.LoadSplit(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}
	// UBI-182 real, follow-up fix: a pinned launch always serves the
	// WHOLE real group under its own real published identity -- the
	// resource/data-source split (kubernetes vs kubernetes_ds) is a real,
	// internal discovery-time detail, never something a user should need
	// to know or write two separate pins for (see mergegroup.go's own
	// doc comment for the full real account of why nothing in the wire
	// protocol or this binary's own server types ever required
	// otherwise). name here is expected to equal snap.Provider exactly
	// -- a real mismatch means a real misconfiguration (a stray old-style
	// sub-member reference), caught here, loudly, rather than silently
	// resolving a partial or wrong schema.
	if name != snap.Provider {
		return fmt.Errorf("snapshot %s is for group %q, but %s is %q -- a pinned entry always serves the whole real group under its own published identity, not one internal member", snapPath, snap.Provider, nameEnvVar, name)
	}

	// UBI-193's own real fix: no group-wide exec config is resolved here,
	// eagerly, before any RPC is even served -- GetProviderSchema (every
	// RPC schema-fetch/pinning ever calls) never touches a REST client
	// at all, so requiring one real, single, agreed-upon config across
	// EVERY member before serving it was an artificial precondition, not
	// a genuine requirement. Confirmed live: Google's own real 262 members
	// span 163 distinct real base_urls (one shared control-plane endpoint
	// is not a thing GCP has); Azure's own real 302 members have real
	// auth on exactly 1 (the other 301 carry a real, legitimate "no
	// authentication configured" state, not an error). Each real source
	// builder below resolves its own real member's own real exec config
	// directly, per resource type where that's meaningful (openapi), at
	// the point real wire execution is actually attempted -- never at
	// schema-serve time.
	//
	// UBI-193's own second real fix: GroupSchemaSource's own refusal
	// (ErrMixedSchemaSourceGroup) is still real and still correct -- it
	// protects buildServerForSource/every Merge<Source>Group from ever
	// needing to know about a real MIXED group (AWS's own real shape:
	// one CloudFormation resource member, 429 Smithy data-source
	// members -- confirmed the ONLY real group among this org's six real
	// providers spanning more than one real source). A mixed group is
	// served by internal/mixedserver's own real dispatch layer instead,
	// built from one real sub-server PER real source present, routed by
	// resource/data-source type.
	src, err := snap.GroupSchemaSource()
	if err == nil {
		server, _, _, err := buildServerForSource(name, snap, src)
		if err != nil {
			return fmt.Errorf("build server for group %q in snapshot %s: %w", name, snapPath, err)
		}
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: serving %q (%s) from real group snapshot %s (version %s, schema_format %d), zero network at schema resolution time\n",
			name, src, snapPath, snap.Version, snap.SchemaFormat)
		return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
			return server
		})
	}
	if !errors.Is(err, snapshot.ErrMixedSchemaSourceGroup) {
		return fmt.Errorf("snapshot %s: %w", snapPath, err)
	}

	server, err := buildMixedSourceServer(name, snap)
	if err != nil {
		return fmt.Errorf("build mixed-source server for group %q in snapshot %s: %w", name, snapPath, err)
	}
	sources := snap.DistinctSources()
	fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: serving %q (mixed: %v) from real group snapshot %s (version %s, schema_format %d), zero network at schema resolution time\n",
		name, sources, snapPath, snap.Version, snap.SchemaFormat)
	return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
		return server
	})
}

// buildServerForSource builds the real tfprotov6.ProviderServer for a
// snapshot ALREADY confirmed single-source (either the whole real group
// genuinely is, or it's SubsetBySource's own real single-source
// subset), plus that source's own real resource/data-source schema
// maps -- returned alongside the server so buildMixedSourceServer can
// merge them across real sources without re-deriving anything.
func buildServerForSource(name string, snap *snapshot.Snapshot, src snapshot.SchemaSource) (server tfprotov6.ProviderServer, resourceSchemas, dataSourceSchemas map[string]*tfprotov6.Schema, err error) {
	switch src {
	case snapshot.SchemaSourceOpenAPI:
		return buildOpenAPIServer(name, snap)
	case snapshot.SchemaSourceDiscoveryDoc:
		return buildDiscoveryDocServer(name, snap)
	case snapshot.SchemaSourceCloudFormation:
		server, resourceSchemas, err := buildCloudFormationServer(name, snap)
		return server, resourceSchemas, nil, err
	case snapshot.SchemaSourceSmithy:
		return buildSmithyServer(name, snap)
	default:
		return nil, nil, nil, fmt.Errorf("group %q's own schema_source %q is not a real, known schema source", name, src)
	}
}

// buildOpenAPIServer builds a real dynserver.Server for an
// openapi-sourced (sub)snapshot -- each real resource type resolves its
// own real client from its own real originating member (UBI-193),
// never one client shared across a whole group whose real members may
// genuinely disagree on BaseURL/Auth (Azure/Google, confirmed live).
// clientCache avoids building the same real member's client twice when
// many resource types share one origin.
func buildOpenAPIServer(name string, snap *snapshot.Snapshot) (tfprotov6.ProviderServer, map[string]*tfprotov6.Schema, map[string]*tfprotov6.Schema, error) {
	resources, dataSources, resourceMemberOf, err := snapshot.MergeOpenAPIGroup(snap)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(resources) == 0 && len(dataSources) == 0 {
		return nil, nil, nil, fmt.Errorf("no resources or data sources -- nothing to serve")
	}
	clientCache := map[string]*restexec.Client{}
	for typeName, rt := range resources {
		memberName := resourceMemberOf[typeName]
		client, ok := clientCache[memberName]
		if !ok {
			client, err = buildMemberClient(snap.Members[memberName])
			if err != nil {
				return nil, nil, nil, fmt.Errorf("member %q: %w", memberName, err)
			}
			clientCache[memberName] = client
		}
		rt.Client = client
	}
	server := &dynserver.Server{ProviderName: name, Resources: resources, DataSources: dataSourcesToResourceTypes(dataSources)}
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, rt := range resources {
		resourceSchemas[typeName] = rt.Schema
	}
	dataSourceSchemas := make(map[string]*tfprotov6.Schema, len(dataSources))
	for typeName, ds := range dataSources {
		dataSourceSchemas[typeName] = ds.Schema
	}
	return server, resourceSchemas, dataSourceSchemas, nil
}

// buildDiscoveryDocServer builds a real dynserver.Server for a
// discoverydoc-sourced (sub)snapshot -- schema-layer only, matching
// run()'s own real discoverydoc live-fetch branch exactly (real REST
// wire execution against a live GCP endpoint is a separate,
// deliberately-unattempted future checkpoint).
func buildDiscoveryDocServer(name string, snap *snapshot.Snapshot) (tfprotov6.ProviderServer, map[string]*tfprotov6.Schema, map[string]*tfprotov6.Schema, error) {
	resources, dataSources, err := snapshot.MergeDiscoveryDocGroup(snap)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(resources) == 0 && len(dataSources) == 0 {
		return nil, nil, nil, fmt.Errorf("no resources or data sources -- nothing to serve")
	}
	built := make(map[string]*dynserver.ResourceType, len(resources))
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, br := range resources {
		built[typeName] = &dynserver.ResourceType{Schema: br.Schema}
		resourceSchemas[typeName] = br.Schema
	}
	out := make(map[string]*dynserver.ResourceType, len(dataSources))
	dataSourceSchemas := make(map[string]*tfprotov6.Schema, len(dataSources))
	for typeName, ds := range dataSources {
		out[typeName] = &dynserver.ResourceType{Schema: ds.Schema}
		dataSourceSchemas[typeName] = ds.Schema
	}
	server := &dynserver.Server{ProviderName: name, Resources: built, DataSources: out}
	return server, resourceSchemas, dataSourceSchemas, nil
}

// buildCloudFormationServer builds a real cfnserver.Server for a
// cloudformation-sourced (sub)snapshot. MergeCloudFormationGroup itself
// already refuses any ModeDataSource member (CloudFormation has no such
// concept) -- nothing further to check here.
//
// cfnserver.Server still carries one real CCAPI client for the whole
// group (unchanged) -- CloudFormation genuinely has no per-resource-type
// divergence TODAY, since every real CFN member is resource-mode and
// this org's own real config has never had more than one real CFN
// member. Resolved from the group's own real resource-mode member(s)
// directly, sorted, first -- NOT the removed group-wide ExecConfig,
// which used to scan every member indiscriminately. If a real group
// ever does carry more than one real CFN member with genuinely
// different exec config, this would need the same per-type treatment
// dynserver already got -- untested today, confirmed not assumed
// (UBI-193's own scope note).
func buildCloudFormationServer(name string, snap *snapshot.Snapshot) (tfprotov6.ProviderServer, map[string]*tfprotov6.Schema, error) {
	resources, err := snapshot.MergeCloudFormationGroup(snap)
	if err != nil {
		return nil, nil, err
	}
	if len(resources) == 0 {
		return nil, nil, fmt.Errorf("no resources -- nothing to serve")
	}
	cfnMemberNames := snap.MemberNamesByMode(snapshot.ModeResource)
	cfnClient, err := buildMemberClient(snap.Members[cfnMemberNames[0]])
	if err != nil {
		return nil, nil, fmt.Errorf("member %q: %w", cfnMemberNames[0], err)
	}
	server := cfnserver.New(name, resources, &ccapi.Client{Rest: cfnClient})
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, br := range resources {
		resourceSchemas[typeName] = br.Schema
	}
	return server, resourceSchemas, nil
}

// buildSmithyServer builds a real smithyserver.Server for a
// smithy-sourced (sub)snapshot. BaseURL/Auth/TargetPrefix all come from
// the SAME real member MergeSmithyGroup already anchored its own single
// real resource-mode model to -- MergeSmithyGroup itself refuses a
// group with more than one real resource-mode Smithy member, so there
// is never a real case where a different member's own config could
// apply here.
//
// UBI-193's own real relaxation: a data-source-ONLY Smithy group (AWS's
// own real shape -- 429 real members, zero real resource-mode ones) has
// no real Model to anchor a Wire client to, but doesn't need one
// either -- GetProviderSchema only reads .Schema, and real wire
// execution against a data source was never built for ANY real source
// (openapi/discoverydoc/smithy all agree a data source needs no real
// wire execution at all) -- Model/Wire simply stay nil rather than
// refusing to serve schema-only content that's genuinely real.
// smithyserver.Server.GetProviderSchema is confirmed, by direct
// reading, to never touch Model/Wire -- safe.
func buildSmithyServer(name string, snap *snapshot.Snapshot) (tfprotov6.ProviderServer, map[string]*tfprotov6.Schema, map[string]*tfprotov6.Schema, error) {
	resources, dataSources, model, err := snapshot.MergeSmithyGroup(snap)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(resources) == 0 && len(dataSources) == 0 {
		return nil, nil, nil, fmt.Errorf("no resources or data sources -- nothing to serve")
	}
	var wireClient *wireexec.Client
	if len(resources) > 0 {
		svc, err := smithy.FindService(model)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("find Smithy service: %w", err)
		}
		resourceMemberNames := snap.MemberNamesByMode(snapshot.ModeResource)
		resourceMember := snap.Members[resourceMemberNames[0]]
		smithyClient, err := buildMemberClient(resourceMember)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: %w", resourceMemberNames[0], err)
		}
		wireClient = &wireexec.Client{Rest: smithyClient, Model: model, Service: svc, TargetPrefix: resourceMember.TargetPrefix}
	}
	server := &smithyserver.Server{ProviderName: name, Resources: resources, DataSources: dataSources, Model: model, Wire: wireClient}
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, br := range resources {
		resourceSchemas[typeName] = br.Schema
	}
	dataSourceSchemas := make(map[string]*tfprotov6.Schema, len(dataSources))
	for typeName, ds := range dataSources {
		dataSourceSchemas[typeName] = ds.Schema
	}
	return server, resourceSchemas, dataSourceSchemas, nil
}

// buildMixedSourceServer is UBI-193's own real dispatch-layer
// construction: for a real group whose own members span more than one
// real SchemaSource (AWS's own real, and today only, shape), build one
// real sub-server PER real source present (Snapshot.SubsetBySource +
// buildServerForSource, completely unchanged, no knowledge of mixing
// needed), then merge every sub-server's own real resource/data-source
// schemas across sources using the identical collision discipline
// mergeWithExclude already provides within one source
// (snapshot.MergeMixedSourceSchemas) -- a type owned by two real
// sources fails loud (ErrDuplicateWireType), exactly like a
// same-source collision already does. The routing table
// (mixedserver.Server.ResourceOwner/DataSourceOwner) is built from the
// SAME real placement decisions, so a type can never be routed to a
// different sub-server than the one whose schema was actually placed
// for it.
func buildMixedSourceServer(name string, snap *snapshot.Snapshot) (tfprotov6.ProviderServer, error) {
	resourceSchemas := map[string]*tfprotov6.Schema{}
	resourcePlacedBy := map[string]string{}
	dataSourceSchemas := map[string]*tfprotov6.Schema{}
	dataSourcePlacedBy := map[string]string{}
	resourceOwner := map[string]tfprotov6.ProviderServer{}
	dataSourceOwner := map[string]tfprotov6.ProviderServer{}

	for _, src := range snap.DistinctSources() {
		sourceName := string(src)
		subSnap := snap.SubsetBySource(src)
		subServer, subResourceSchemas, subDataSourceSchemas, err := buildServerForSource(name, subSnap, src)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", sourceName, err)
		}
		if err := snapshot.MergeMixedSourceSchemas(resourceSchemas, resourcePlacedBy, subResourceSchemas, sourceName, snap.Exclude, "resource"); err != nil {
			return nil, err
		}
		if err := snapshot.MergeMixedSourceSchemas(dataSourceSchemas, dataSourcePlacedBy, subDataSourceSchemas, sourceName, snap.Exclude, "data source"); err != nil {
			return nil, err
		}
		for typeName, placedSource := range resourcePlacedBy {
			if placedSource == sourceName {
				resourceOwner[typeName] = subServer
			}
		}
		for typeName, placedSource := range dataSourcePlacedBy {
			if placedSource == sourceName {
				dataSourceOwner[typeName] = subServer
			}
		}
	}

	if len(resourceSchemas) == 0 && len(dataSourceSchemas) == 0 {
		return nil, fmt.Errorf("no resources or data sources across any real source -- nothing to serve")
	}

	return &mixedserver.Server{
		ProviderName:      name,
		ResourceOwner:     resourceOwner,
		DataSourceOwner:   dataSourceOwner,
		ResourceSchemas:   resourceSchemas,
		DataSourceSchemas: dataSourceSchemas,
	}, nil
}

// dataSourcesToResourceTypes adapts resourcemap.BuiltDataSource's own
// shape into dynserver.Server.DataSources' own map[string]*ResourceType
// -- the identical real adaptation run()'s own openapi data-source
// live-fetch branch already does inline, factored out here since
// runServeSnapshot's own openapi branch needs the same conversion.
func dataSourcesToResourceTypes(dataSources map[string]*resourcemap.BuiltDataSource) map[string]*dynserver.ResourceType {
	out := make(map[string]*dynserver.ResourceType, len(dataSources))
	for typeName, ds := range dataSources {
		out[typeName] = &dynserver.ResourceType{Schema: ds.Schema}
	}
	return out
}

// runDumpSignalsFromSnapshot is --dump-signals' own real, snapshot-driven
// counterpart to each live-fetch branch's own identical `if
// *dumpSignalsFlag` check -- UBI-182's real fix for the gap
// ubiquex's own cli/dynamicprovider.go named. Picks name's own member out
// of the real group container first (Snapshot.Member), then mirrors that
// member's own source's real, already-established signal availability
// exactly: a RESOURCE-mode openapi/discoverydoc member carries real,
// per-field signals (rt.Signals/br.Signals, populated by internal/schema's
// own CollectSignals during Build); every data-source-mode member, and
// every cloudformation/smithy member regardless of mode, emits a real,
// honest empty map, matching each live-fetch branch's own already-
// documented "not yet implemented" answer -- this function doesn't
// invent capability the live path doesn't have, it only makes what
// already exists reachable from a pinned group snapshot.
func runDumpSignalsFromSnapshot(name, snapPath string) error {
	snap, err := snapshot.LoadSplit(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}
	if name != snap.Provider {
		return fmt.Errorf("snapshot %s is for group %q, but %s is %q -- a pinned entry always serves the whole real group under its own published identity, not one internal member", snapPath, snap.Provider, nameEnvVar, name)
	}
	src, err := snap.GroupSchemaSource()
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", snapPath, err)
	}

	if src == snapshot.SchemaSourceOpenAPI {
		resources, _, _, err := snapshot.MergeOpenAPIGroup(snap)
		if err != nil {
			return fmt.Errorf("merge group %q in snapshot %s: %w", name, snapPath, err)
		}
		out := make(map[string]map[string]*uschema.FieldSignal, len(resources))
		for typeName, rt := range resources {
			out[typeName] = rt.Signals
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	if src == snapshot.SchemaSourceDiscoveryDoc {
		resources, _, err := snapshot.MergeDiscoveryDocGroup(snap)
		if err != nil {
			return fmt.Errorf("merge group %q in snapshot %s: %w", name, snapPath, err)
		}
		out := make(map[string]map[string]*uschema.FieldSignal, len(resources))
		for typeName, br := range resources {
			out[typeName] = br.Signals
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for group %q (%s) -- emitting an empty, real result, not an error\n", name, src)
	return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})
}

// runDumpNamespacesFromSnapshot is --dump-namespaces' own real,
// snapshot-driven entry point -- picks name's own member out of the
// group container, then defers the real per-source (and, for a real
// mixed group like AWS, cross-source) computation to
// snapshot.Namespaces, the same real function Namespaces' own doc
// comment explains, moved there specifically so it's hermetically
// testable (see UBI-199).
func runDumpNamespacesFromSnapshot(name, snapPath string) error {
	snap, err := snapshot.LoadSplit(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}
	if name != snap.Provider {
		return fmt.Errorf("snapshot %s is for group %q, but %s is %q -- a pinned entry always serves the whole real group under its own published identity, not one internal member", snapPath, snap.Provider, nameEnvVar, name)
	}
	out, err := snapshot.Namespaces(snap)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", snapPath, err)
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// groupSummary is dumpGroupSummaryFlag's own real output shape --
// deliberately carries no internal member names at all, only the
// group's own real published identity (Provider/Version) and real,
// mechanically-computed counts. See dumpGroupSummaryFlag's own doc
// comment for the real, live finding this exists to fix.
type groupSummary struct {
	Provider    string `json:"provider"`
	Version     string `json:"version"`
	Resources   int    `json:"resources"`
	DataSources int    `json:"data_sources"`
}

// runDumpGroupSummary loads snapPath (no UBX_DYNAMIC_PROVIDER_NAME
// needed -- this inspects the whole group directly, the same real
// reason runGenerateSnapshotGroup needs none), dispatches to the
// source-appropriate Merge<Source>Group (the SAME real merge every
// pinned resolution already goes through, not a separate count), and
// prints only Provider/Version/counts -- never a member name.
func runDumpGroupSummary(snapPath string) error {
	snap, err := snapshot.LoadSplit(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}
	resources, dataSources, err := snapshot.Summarize(snap)
	if err != nil {
		return fmt.Errorf("summarize group in snapshot %s: %w", snapPath, err)
	}
	return json.NewEncoder(os.Stdout).Encode(groupSummary{
		Provider:    snap.Provider,
		Version:     snap.Version,
		Resources:   resources,
		DataSources: dataSources,
	})
}

// runGenerateSnapshotGroup is generateSnapshotGroupFlag's own real
// implementation -- UBI-182's group container replacement for the old,
// single-member --generate-snapshot. Loads THIS process's own real
// .ubx/config in full (config.Load, every real [dynamic_providers.<name>]
// table at once -- not LoadNamed's single-active-name lookup, since a
// group genuinely needs several), generates each real named member ONE
// real time via the source-and-mode-appropriate Generate<Source>Member,
// and assembles them into ONE real, versioned group container
// (AssembleGroup) written to outPath.
func runGenerateSnapshotGroup(outPath, repoName, membersCSV, prevPath, excludeJSON string, allowDevBinary bool) error {
	if repoName == "" {
		return fmt.Errorf("--generate-snapshot-group requires --group-repo-name")
	}
	if membersCSV == "" && prevPath == "" {
		return fmt.Errorf("--generate-snapshot-group requires --group-members (comma-separated [dynamic_providers.<name>] table names), or --prev-snapshot alone to re-derive every member's own generation input from the previous snapshot directly")
	}

	var exclude map[string][]string
	if excludeJSON != "" {
		if err := json.Unmarshal([]byte(excludeJSON), &exclude); err != nil {
			return fmt.Errorf("--group-exclude: invalid JSON: %w", err)
		}
	}

	var prev *snapshot.Snapshot
	if prevPath != "" {
		p, err := snapshot.LoadSplit(prevPath)
		if err != nil {
			return fmt.Errorf("load --prev-snapshot %s: %w", prevPath, err)
		}
		prev = p
	}

	var members map[string]*snapshot.MemberSnapshot
	var levels map[string]snapshot.ChangeLevel
	var err error
	if membersCSV != "" {
		dir, dirErr := os.Getwd()
		if dirErr != nil {
			return fmt.Errorf("determine working directory: %w", dirErr)
		}
		allProviders, loadErr := config.Load(dir)
		if loadErr != nil {
			return fmt.Errorf("load config: %w", loadErr)
		}
		rawNames := strings.Split(membersCSV, ",")
		members, levels, err = generateGroupMembers(allProviders, rawNames, prev)
	} else {
		// UBI-229: --group-members omitted, --prev-snapshot alone drives
		// generation -- see generateGroupMembersFromSnapshot's own doc
		// comment for the full real reasoning (this is the mode
		// hash-watch.yml uses once it stops carrying its own embedded
		// copy of the live per-member config).
		members, levels, err = generateGroupMembersFromSnapshot(prev)
	}
	if err != nil {
		return err
	}

	group, err := snapshot.AssembleGroup(repoName, prev, members, levels, exclude)
	if err != nil {
		return fmt.Errorf("assemble group %q: %w", repoName, err)
	}

	if group.GeneratedByBinaryVersion == "dev" && !allowDevBinary {
		return fmt.Errorf("refusing to write a snapshot with min_binary_version \"dev\" (this build has no real, released version -- pass --allow-dev-binary to write it anyway, a deliberate choice for local iteration, never for anything meant to be committed)")
	}

	if err := snapshot.SaveSplit(outPath, group); err != nil {
		return fmt.Errorf("write snapshot to %s: %w", outPath, err)
	}

	fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: wrote real group snapshot for %q, version %s, schema_format %d, %d member(s) -> %s\n",
		repoName, group.Version, group.SchemaFormat, len(group.Members), outPath)
	return nil
}

// generateGroupMembers generates every real named member for a group,
// collecting every real generation failure instead of stopping at the
// first one -- UBI-229's own real incident (one moved upstream spec
// path blocked a real 302-member cut on a single named failure,
// forcing a separate, out-of-band check of the other 301 to know
// whether more than one had moved) is exactly what this exists to
// shorten: one real run now names every bad member at once, rather
// than a fix-one-rerun-hit-the-next cycle. Still a hard failure
// overall -- a non-nil error here means the caller must not assemble
// or write anything, matching the never-silently-skip rule every
// other real gap-surfacing mechanism in this codebase already follows
// (hash-watch's own error state, ErrDuplicateWireType's fail-loud
// default). A member whose own [dynamic_providers.<name>] table is
// missing from allProviders is collected as a real failure too, not
// treated differently from a live generation error.
func generateGroupMembers(allProviders map[string]config.Provider, rawNames []string, prev *snapshot.Snapshot) (map[string]*snapshot.MemberSnapshot, map[string]snapshot.ChangeLevel, error) {
	members := make(map[string]*snapshot.MemberSnapshot, len(rawNames)*2)
	levels := make(map[string]snapshot.ChangeLevel, len(rawNames)*2)
	var errs []error

	for _, rawName := range rawNames {
		memberName := strings.TrimSpace(rawName)
		cfg, ok := allProviders[memberName]
		if !ok {
			errs = append(errs, fmt.Errorf("--group-members: no [dynamic_providers.%s] table in this process's own .ubx/config", memberName))
			continue
		}
		wireName := memberName
		if cfg.WireName != "" {
			wireName = cfg.WireName
		}

		// UBI-182's own real collapse -- see snapshot.ExpandMemberModes'
		// own doc comment for the full real reasoning.
		modes, memberNames := snapshot.ExpandMemberModes(memberName, cfg)

		for i, mode := range modes {
			mn := memberNames[i]
			var prevMember *snapshot.MemberSnapshot
			if prev != nil {
				prevMember = prev.Members[mn]
			}

			member, level, genErr := generateOneMember(mn, wireName, cfg, mode, prevMember)
			if genErr != nil {
				errs = append(errs, fmt.Errorf("generate member %q: %w", mn, genErr))
				continue
			}
			members[mn] = member
			levels[mn] = level
			fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: generated member %q (%s, %s), own change level: %s\n", mn, cfg.SchemaSource, mode, level)
		}
	}

	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return members, levels, nil
}

// generateOneMember is the one real place every Generate<Source>Member
// dispatch happens -- shared by generateGroupMembers (config-driven,
// reads a live .ubx/config) and generateGroupMembersFromSnapshot
// (snapshot-driven, reads a previously committed group directly). Both
// callers reconstruct the identical config.Provider shape from their
// own real source before calling this; this function does not care
// which source that was.
func generateOneMember(mn, wireName string, cfg config.Provider, mode snapshot.Mode, prevMember *snapshot.MemberSnapshot) (*snapshot.MemberSnapshot, snapshot.ChangeLevel, error) {
	switch cfg.SchemaSource {
	case config.SchemaSourceOpenAPI:
		member, _, level, err := snapshot.GenerateOpenAPIMember(mn, wireName, cfg.SchemaURL, mode, cfg, prevMember)
		return member, level, err
	case config.SchemaSourceCloudFormation:
		member, _, level, err := snapshot.GenerateCloudFormationMember(mn, cfg.SchemaURL, mode, cfg, prevMember)
		return member, level, err
	case config.SchemaSourceSmithy:
		member, _, level, err := snapshot.GenerateSmithyMember(mn, wireName, cfg.SchemaURL, cfg.TargetPrefix, mode, cfg.DataSourceNamespace, cfg, prevMember)
		return member, level, err
	case config.SchemaSourceDiscoveryDoc:
		member, _, level, err := snapshot.GenerateDiscoveryDocMember(mn, wireName, cfg.SchemaURL, cfg.VersionQualifier, mode, cfg, prevMember)
		return member, level, err
	default:
		return nil, snapshot.NoChange, fmt.Errorf("schema_source %q is not a real, known schema source", cfg.SchemaSource)
	}
}

// generateGroupMembersFromSnapshot regenerates a group directly from
// its own previously committed snapshot, with no live .ubx/config
// involved at all (UBI-229). Every field a live [dynamic_providers.
// <name>] table would have carried is already present on each
// committed member's own MemberSnapshot -- SchemaSource, SchemaURL
// (UBI-222), BaseURL, Auth, Retry, Timeouts, Resources, WireName,
// VersionQualifier, TargetPrefix, DataSourceNamespace,
// NamespaceFromTags -- exactly the set UBI-182's own doc comment on
// MemberSnapshot says was carried through so a pinned member could be
// "a real, complete replacement" for that table. Before this existed,
// hash-watch.yml for every group-based provider (azure, google, aws)
// had no choice but to carry its own full, separately maintained copy
// of that live config just to know what to re-fetch -- a copy that
// can silently drift from what is actually committed (this is
// precisely what happened: azure_newrelic's own path moved upstream,
// the copy was never updated, and the real committed snapshot was
// never consulted to catch it), and, independent of drift, grew large
// enough on its own to exceed GitHub Actions' own 21000-character
// expression-length limit and fail to parse at all (confirmed
// directly: azure's own embedded config's run block measured 31033
// characters, google's 75678, aws's 164713, all three failing
// workflow_dispatch outright with the identical parser error).
// Iterates prev.Members in sorted key order -- determinism, not
// incidental map order, the same discipline this project's own
// hashing rules already require everywhere else.
func generateGroupMembersFromSnapshot(prev *snapshot.Snapshot) (map[string]*snapshot.MemberSnapshot, map[string]snapshot.ChangeLevel, error) {
	if prev == nil {
		return nil, nil, fmt.Errorf("generateGroupMembersFromSnapshot: prev is nil")
	}

	names := make([]string, 0, len(prev.Members))
	for name := range prev.Members {
		names = append(names, name)
	}
	sort.Strings(names)

	members := make(map[string]*snapshot.MemberSnapshot, len(names))
	levels := make(map[string]snapshot.ChangeLevel, len(names))
	var errs []error

	for _, mn := range names {
		prevMember := prev.Members[mn]
		wireName := mn
		if prevMember.WireName != "" {
			wireName = prevMember.WireName
		}
		cfg := config.Provider{
			Name:                mn,
			SchemaSource:        config.SchemaSourceType(prevMember.SchemaSource),
			SchemaURL:           prevMember.SchemaURL,
			BaseURL:             prevMember.BaseURL,
			Auth:                prevMember.Auth,
			TargetPrefix:        prevMember.TargetPrefix,
			WireName:            prevMember.WireName,
			VersionQualifier:    prevMember.VersionQualifier,
			DataSourceNamespace: prevMember.DataSourceNamespace,
			NamespaceFromTags:   prevMember.NamespaceFromTags,
			Retry:               prevMember.Retry,
			Timeouts:            prevMember.Timeouts,
			Resources:           prevMember.Resources,
		}

		member, level, genErr := generateOneMember(mn, wireName, cfg, prevMember.Mode, prevMember)
		if genErr != nil {
			errs = append(errs, fmt.Errorf("regenerate member %q from its own committed snapshot: %w", mn, genErr))
			continue
		}
		members[mn] = member
		levels[mn] = level
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: regenerated member %q (%s, %s) from its own committed snapshot, own change level: %s\n", mn, cfg.SchemaSource, prevMember.Mode, level)
	}

	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return members, levels, nil
}
