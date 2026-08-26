// Command ubx-provider-dynamic is UBI-158's own Dynamic Provider: a real
// tfplugin v6 provider binary, launched by ubx exactly like any HashiCorp
// provider (provider.Launch, zero special-casing), that derives its own
// resource schema and CRUD behavior at runtime from a real OpenAPI 3.x
// spec instead of shipping hand-written, per-service Go code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/ccapi"
	cfnserver "github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/server"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/discoverydoc"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
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

// generateSnapshotFlag is a real, plain CLI mode mirroring
// dumpSignalsFlag's own established shape: fetch this [dynamic_providers.
// <name>] entry's real, live schema_url ONE time, verify it needs no
// further network access, write a real internal/snapshot.Snapshot to the
// given path, and exit -- never serves a tfplugin6 provider. UBI-182:
// wired to all four real schema sources (was openapi only) --
// runGenerateSnapshot's own switch dispatches to the source-appropriate
// Generate<Source>.
var generateSnapshotFlag = flag.String("generate-snapshot", "", "generate a real, frozen schema snapshot to this path instead of serving a tfplugin6 provider, and exit")

// prevSnapshotFlag is generateSnapshotFlag's own real, optional sibling:
// the PRIOR real snapshot (if any) to diff the freshly-fetched spec
// against, so the new snapshot's own Version is mechanically derived
// (internal/snapshot.DiffLevel/NextVersion) rather than left for a human
// to guess. Omit for a provider's first-ever snapshot.
var prevSnapshotFlag = flag.String("prev-snapshot", "", "path to the prior real snapshot to diff against when deriving the new one's own version (omit for a first-ever snapshot)")

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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ubx-provider-dynamic:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()

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

	if *generateSnapshotFlag != "" {
		return runGenerateSnapshot(name, cfg, *generateSnapshotFlag, *prevSnapshotFlag)
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

		if *dumpSignalsFlag {
			// Real, honest, named gap: Smithy's own real trait system
			// (smithy.api#length/#range/#pattern, its own enum trait) is
			// a genuinely separate real constraint/enum source from
			// OpenAPI's -- internal/schema/signals.go's own CollectSignals
			// only ever walks *openapi3.Schema. Not yet built for Smithy;
			// an empty, real (not error) JSON object is the honest
			// "no signal available for this source yet" answer, matching
			// this whole file's own "skip, don't fail" discipline for a
			// genuinely partial-coverage case.
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
			// implementation, not two that could drift apart.
			out := make(map[string]string, len(built))
			for hcName := range built {
				out[hcName] = smithy.ServiceNamespace(svc)
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

		if *dumpSignalsFlag {
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
			// result, not a guess dressed up as one.
			return json.NewEncoder(os.Stdout).Encode(map[string]string{})
		}

		resources := make(map[string]*dynserver.ResourceType, len(built))
		for typeName, br := range built {
			resources[typeName] = &dynserver.ResourceType{Schema: br.Schema}
		}
		server := &dynserver.Server{ProviderName: name, Resources: resources}
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

	doc, err := openapi.Load(cfg.SchemaURL)
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

	if *dumpSignalsFlag {
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
		// service segment, not a re-split of a foreign legacy name).
		return json.NewEncoder(os.Stdout).Encode(map[string]string{})
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

	server := &dynserver.Server{
		ProviderName: name,
		Resources:    resources,
		Client:       client,
	}

	return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
		return server
	})
}

// runServeSnapshot is snapshotPathEnvVar's own real implementation -- the
// literal fix for the problem this whole package exists to solve. Loads
// a real, already-generated Snapshot from snapPath (snapshot.Load
// already runs CheckFormat, so an out-of-range schema_format refuses
// loudly right here, before any RPC serving begins), re-derives its real
// resource map via the source-appropriate Load<Source> (zero network --
// the same real translation the live-fetch path runs, just fed frozen
// RawSpec bytes), and serves it through the IDENTICAL real server
// construction run()'s own matching live-fetch branch uses for that
// source -- UBI-182 generalized this from openapi-only to all four,
// since each source's own real execution model genuinely differs
// (dynserver.Server for openapi/discoverydoc, cfnserver.Server for
// cloudformation, smithyserver.Server for smithy -- confirmed by reading
// each live-fetch branch's own server construction, not assumed
// uniform). The only real difference from each source's own live-fetch
// branch is where Auth/BaseURL/Retry/Resources/WireName/TargetPrefix
// come from (the snapshot's own fields, not a live .ubx/config fetch).
func runServeSnapshot(name, snapPath string) error {
	snap, err := snapshot.Load(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}

	authenticator, err := auth.Build(snap.Auth.Type, snap.Auth.Params)
	if err != nil {
		return fmt.Errorf("build authenticator: %w", err)
	}
	retryPolicy, err := dynserver.ResolveRetryPolicy(snap.Retry)
	if err != nil {
		return fmt.Errorf("resolve retry policy: %w", err)
	}
	restClient := restexec.NewClient(snap.BaseURL, authenticator)
	restClient.Retry = retryPolicy

	var server tfprotov6.ProviderServer
	switch snap.SchemaSource {
	case snapshot.SchemaSourceOpenAPI:
		resources, err := snapshot.LoadOpenAPI(name, snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		if len(resources) == 0 {
			return fmt.Errorf("no CRUD-shaped resources in snapshot %s -- nothing to serve", snapPath)
		}
		server = &dynserver.Server{ProviderName: name, Resources: resources, Client: restClient}

	case snapshot.SchemaSourceDiscoveryDoc:
		// Schema-layer only, matching run()'s own real discoverydoc
		// live-fetch branch exactly (that branch's own doc comment:
		// dynserver.Server is reused UNCHANGED for GetProviderSchema,
		// since that RPC reads only ResourceType.Schema -- real REST
		// wire execution against a live GCP endpoint is a separate,
		// deliberately-unattempted future checkpoint, not something
		// this snapshot path invents beyond what the live path already
		// does).
		built, err := snapshot.LoadDiscoveryDoc(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		if len(built) == 0 {
			return fmt.Errorf("no CRUD-shaped resources in snapshot %s -- nothing to serve", snapPath)
		}
		resources := make(map[string]*dynserver.ResourceType, len(built))
		for typeName, br := range built {
			resources[typeName] = &dynserver.ResourceType{Schema: br.Schema}
		}
		server = &dynserver.Server{ProviderName: name, Resources: resources}

	case snapshot.SchemaSourceCloudFormation:
		built, err := snapshot.LoadCloudFormation(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		if len(built) == 0 {
			return fmt.Errorf("no resources in snapshot %s -- nothing to serve", snapPath)
		}
		server = cfnserver.New(name, built, &ccapi.Client{Rest: restClient})

	case snapshot.SchemaSourceSmithy:
		var smithyDoc smithy.Model
		if err := json.Unmarshal(snap.RawSpec, &smithyDoc); err != nil {
			return fmt.Errorf("parse snapshot's own raw_spec: %w", err)
		}
		svc, err := smithy.FindService(&smithyDoc)
		if err != nil {
			return fmt.Errorf("find Smithy service in snapshot %s: %w", snapPath, err)
		}
		built, err := snapshot.LoadSmithy(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		if len(built) == 0 {
			return fmt.Errorf("no CRUD-shaped resources in snapshot %s -- nothing to serve", snapPath)
		}
		wireClient := &wireexec.Client{Rest: restClient, Model: &smithyDoc, Service: svc, TargetPrefix: snap.TargetPrefix}
		server = &smithyserver.Server{ProviderName: name, Resources: built, Model: &smithyDoc, Wire: wireClient}

	default:
		return fmt.Errorf("snapshot %s: schema_source %q is not a real, known schema source", snapPath, snap.SchemaSource)
	}

	fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: serving %q from real snapshot %s (version %s, schema_format %d), zero network at schema resolution time\n",
		name, snapPath, snap.Version, snap.SchemaFormat)

	return tf6server.Serve("registry.terraform.io/ubiquex/"+name, func() tfprotov6.ProviderServer {
		return server
	})
}

// runDumpSignalsFromSnapshot is --dump-signals' own real, snapshot-driven
// counterpart to each live-fetch branch's own identical `if
// *dumpSignalsFlag` check -- UBI-182's real fix for the gap
// ubiquex's own cli/dynamicprovider.go named. Mirrors each source's own
// real, already-established signal availability exactly: openapi and
// discoverydoc both carry real, per-field signals on their own built
// resource (rt.Signals/br.Signals, populated by internal/schema's own
// CollectSignals during Build); cloudformation and smithy both emit a
// real, honest empty map, matching their own live-fetch branches'
// already-documented "not yet implemented for this source" answer --
// this function doesn't invent capability the live path doesn't have,
// it only makes what already exists reachable from a pinned snapshot.
func runDumpSignalsFromSnapshot(name, snapPath string) error {
	snap, err := snapshot.Load(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}

	switch snap.SchemaSource {
	case snapshot.SchemaSourceOpenAPI:
		resources, err := snapshot.LoadOpenAPI(name, snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		out := make(map[string]map[string]*uschema.FieldSignal, len(resources))
		for typeName, rt := range resources {
			out[typeName] = rt.Signals
		}
		return json.NewEncoder(os.Stdout).Encode(out)

	case snapshot.SchemaSourceDiscoveryDoc:
		built, err := snapshot.LoadDiscoveryDoc(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		out := make(map[string]map[string]*uschema.FieldSignal, len(built))
		for typeName, br := range built {
			out[typeName] = br.Signals
		}
		return json.NewEncoder(os.Stdout).Encode(out)

	case snapshot.SchemaSourceCloudFormation, snapshot.SchemaSourceSmithy:
		fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: --dump-signals: not yet implemented for schema_source = %q -- emitting an empty, real result, not an error\n", snap.SchemaSource)
		return json.NewEncoder(os.Stdout).Encode(map[string]map[string]*uschema.FieldSignal{})

	default:
		return fmt.Errorf("snapshot %s: schema_source %q is not a real, known schema source", snapPath, snap.SchemaSource)
	}
}

// runDumpNamespacesFromSnapshot is --dump-namespaces' own real,
// snapshot-driven counterpart -- same real reasoning as
// runDumpSignalsFromSnapshot. openapi and discoverydoc both emit a real,
// honest empty map (their own live-fetch branches' own documented
// "already correct by construction" finding -- nothing for this
// override to add). cloudformation and smithy both compute a real
// namespace per resource, mirroring their own live-fetch branches
// exactly (cloudformation.SplitTypeName; smithy.ServiceNamespace, which
// needs the real Smithy service shape -- re-derived here via
// smithy.FindService against the snapshot's own frozen RawSpec, zero
// network).
func runDumpNamespacesFromSnapshot(name, snapPath string) error {
	snap, err := snapshot.Load(snapPath)
	if err != nil {
		return fmt.Errorf("load snapshot %s: %w", snapPath, err)
	}

	switch snap.SchemaSource {
	case snapshot.SchemaSourceOpenAPI, snapshot.SchemaSourceDiscoveryDoc:
		return json.NewEncoder(os.Stdout).Encode(map[string]string{})

	case snapshot.SchemaSourceCloudFormation:
		built, err := snapshot.LoadCloudFormation(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		out := make(map[string]string, len(built))
		for resourceTypeName, br := range built {
			ns, _ := cloudformation.SplitTypeName(br.TypeName)
			out[resourceTypeName] = strings.ToLower(ns)
		}
		return json.NewEncoder(os.Stdout).Encode(out)

	case snapshot.SchemaSourceSmithy:
		var smithyDoc smithy.Model
		if err := json.Unmarshal(snap.RawSpec, &smithyDoc); err != nil {
			return fmt.Errorf("parse snapshot's own raw_spec: %w", err)
		}
		svc, err := smithy.FindService(&smithyDoc)
		if err != nil {
			return fmt.Errorf("find Smithy service in snapshot %s: %w", snapPath, err)
		}
		built, err := snapshot.LoadSmithy(snap)
		if err != nil {
			return fmt.Errorf("rebuild resource schemas from snapshot %s: %w", snapPath, err)
		}
		out := make(map[string]string, len(built))
		for hcName := range built {
			out[hcName] = smithy.ServiceNamespace(svc)
		}
		return json.NewEncoder(os.Stdout).Encode(out)

	default:
		return fmt.Errorf("snapshot %s: schema_source %q is not a real, known schema source", snapPath, snap.SchemaSource)
	}
}

// runGenerateSnapshot is generateSnapshotFlag's own real implementation --
// UBI-182 opened this to all four real schema sources (was openapi
// only). Loads prevPath (if given) for real, mechanical version
// derivation, fetches cfg's own real, live schema ONE time via the
// source-appropriate Generate<Source>, and writes a real, complete
// Snapshot to outPath.
func runGenerateSnapshot(name string, cfg config.Provider, outPath, prevPath string) error {
	wireName := name
	if cfg.WireName != "" {
		wireName = cfg.WireName
	}

	var prev *snapshot.Snapshot
	if prevPath != "" {
		p, err := snapshot.Load(prevPath)
		if err != nil {
			return fmt.Errorf("load --prev-snapshot %s: %w", prevPath, err)
		}
		prev = p
	}

	var snap *snapshot.Snapshot
	var err error
	switch cfg.SchemaSource {
	case config.SchemaSourceOpenAPI:
		snap, err = snapshot.GenerateOpenAPI(name, cfg.SchemaURL, cfg, prev)
	case config.SchemaSourceCloudFormation:
		snap, err = snapshot.GenerateCloudFormation(name, cfg.SchemaURL, cfg, prev)
	case config.SchemaSourceSmithy:
		snap, err = snapshot.GenerateSmithy(name, wireName, cfg.SchemaURL, cfg.TargetPrefix, cfg, prev)
	case config.SchemaSourceDiscoveryDoc:
		snap, err = snapshot.GenerateDiscoveryDoc(name, cfg.SchemaURL, cfg.VersionQualifier, cfg, prev)
	default:
		return fmt.Errorf("--generate-snapshot: [dynamic_providers.%s]'s own schema_source %q is not a real, known schema source", name, cfg.SchemaSource)
	}
	if err != nil {
		return fmt.Errorf("generate snapshot for %q: %w", name, err)
	}

	if err := snapshot.Save(outPath, snap); err != nil {
		return fmt.Errorf("write snapshot to %s: %w", outPath, err)
	}

	fmt.Fprintf(os.Stderr, "ubx-provider-dynamic: wrote real snapshot for %q, version %s, schema_format %d -> %s\n",
		name, snap.Version, snap.SchemaFormat, outPath)
	return nil
}
