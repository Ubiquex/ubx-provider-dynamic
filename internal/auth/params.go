package auth

import (
	"encoding/json"
	"fmt"
	"os"
)

// paramsAs decodes params (config.Auth.Params, TOML's own generic
// map[string]any tree) into a typed struct via one JSON round-trip -- the
// identical idiom internal/config's own doc comment already establishes
// for the same reason (cli/config.go's own real, existing precedent in the
// ubiquex monorepo: "merges every format's own parsed generic tree...
// then decodes the single merged result via one JSON round-trip -- one
// decode path... rather than three separate format-specific struct
// decoders"). Each Factory owns its own target struct's `json` tags.
func paramsAs(params map[string]any, target any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// envValue reads name from the process environment, erroring clearly
// (never silently proceeding with an empty credential) if it's unset or
// empty -- the same "$secret is a reference, never the literal value"
// discipline ubx's own IntentConfig.KeyRef already established
// (cli/config.go: "a literal API key sitting in a git-tracked cascade
// file is exactly the... failure... transplanted one layer up"). No auth
// Factory in this package ever accepts a literal credential value in
// config -- only the name of an environment variable to read one from at
// request time.
func envValue(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no environment variable name given")
	}
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("environment variable %s is not set (or empty)", name)
	}
	return v, nil
}
