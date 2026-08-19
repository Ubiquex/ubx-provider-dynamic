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

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
	smithyserver "github.com/ubiquex/ubx-provider-dynamic/internal/smithy/server"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy/wireexec"
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

// nameEnvVar is how a launched process learns which [dynamic_providers.<name>]
// table in .ubx/config is its own -- see internal/config's own doc comment
// for why this can't come from the ConfigureProvider RPC. provider.Launch's
// own WithEnv option (already generic, already exists, not added for this
// binary specifically) is Phase 5's real integration mechanism for setting
// this; standalone/validation runs (this ticket's own Phase 1 proof, and
// any manual invocation) set it directly.
const nameEnvVar = "UBX_DYNAMIC_PROVIDER_NAME"

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

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}

	cfg, err := config.LoadNamed(dir, name)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
		built, notes, err := smithy.Build(smithyDoc, name, smithy.DefaultKnownNames())
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

	doc, err := openapi.Load(cfg.SchemaURL)
	if err != nil {
		return fmt.Errorf("load OpenAPI spec: %w", err)
	}

	resources, notes, err := dynserver.Build(doc, name, cfg)
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
