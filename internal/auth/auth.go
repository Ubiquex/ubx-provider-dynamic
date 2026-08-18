// Package auth builds restexec.Authenticator implementations from a
// [dynamic_providers.<name>.auth] table (config.Auth) -- UBI-158 Phase 2.
//
// Pluggable by construction, not a fixed enum: each real auth type
// registers its own Factory in this package's own registry (the same
// self-registering-driver shape database/sql, image, and net/http/pprof
// all use in the standard library -- adding a new type means adding a new
// file that calls Register in its own init(), never touching a switch
// statement anywhere else in this package or its callers). Build is the
// only entry point a caller (main.go) needs.
package auth

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// Factory builds a real restexec.Authenticator from one auth type's own
// params (config.Auth.Params, already TOML-decoded into a generic
// map[string]any -- each Factory owns decoding that into its own typed
// shape, see paramsAs).
type Factory func(params map[string]any) (restexec.Authenticator, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register adds typeName to the registry. Called from each real auth
// type's own init() (apikey.go, oauth2.go, sigv4.go) -- never called
// directly by Build's own callers. Panics on a duplicate registration
// (a real programming error, the same standard-library convention
// image.RegisterFormat/sql.Register use, not a runtime condition a caller
// could sensibly recover from).
func Register(typeName string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[typeName]; exists {
		panic(fmt.Sprintf("auth: type %q already registered", typeName))
	}
	registry[typeName] = f
}

// Build resolves typeName against the registry and constructs a real
// Authenticator from params. typeName empty means "no authentication" --
// Build returns (nil, nil), the same nil-Authenticator meaning
// restexec.Client already treats as unauthenticated (Phase 1's own
// behavior, unchanged for a provider that genuinely needs none).
func Build(typeName string, params map[string]any) (restexec.Authenticator, error) {
	if typeName == "" {
		return nil, nil
	}
	registryMu.Lock()
	f, ok := registry[typeName]
	registryMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("auth: unrecognized type %q (registered: %v)", typeName, registeredTypes())
	}
	a, err := f(params)
	if err != nil {
		return nil, fmt.Errorf("auth: build %q: %w", typeName, err)
	}
	return a, nil
}

func registeredTypes() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
