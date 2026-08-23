package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// envPrefix namespaces every override so mcpd cannot pick up an unrelated
// variable that happens to share a name.
const envPrefix = "MCPD_"

// applyEnvOverrides layers environment variables over the file.
//
// The set is deliberately small, and smaller than it was: what remains is the
// three things a container has to decide for itself, because the port mapping
// and the volume layout are not this file's to know. The overrides for keys
// that have since moved into the database are in legacy.go, where they seed
// the store on a first start rather than overriding it on every start.
//
// Secrets are not here. They are referenced from the file by name and resolved
// through SecretResolver, so a credential never becomes a config field.
func (c *Config) applyEnvOverrides() error {
	var errs []string

	overrideString(&c.Server.Listen, "LISTEN")
	overrideString(&c.Server.FrontendListen, "FRONTEND_LISTEN")
	overrideString(&c.Storage.Path, "STORAGE_PATH")

	// Plugin enablement, so an image can be shipped with everything compiled
	// in and switched on per deployment.
	for name, pc := range c.Plugins {
		key := "PLUGIN_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_ENABLED"
		enabled := pc.Enabled
		if err := overrideBool(&enabled, key); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		pc.Enabled = enabled
		c.Plugins[name] = pc
	}

	if len(errs) > 0 {
		return fmt.Errorf("config: invalid environment overrides:\n  - %s",
			strings.Join(errs, "\n  - "))
	}
	return nil
}

func overrideString(target *string, key string) {
	if v, ok := os.LookupEnv(envPrefix + key); ok && v != "" {
		*target = v
	}
}

// overrideBool reports a malformed value rather than defaulting.
//
// Silently treating MCPD_FRONTEND_ENABLED=ture as false would disable the
// dashboard with no indication why.
func overrideBool(target *bool, key string) error {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok || v == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s%s=%q is not a boolean; use true or false", envPrefix, key, v)
	}
	*target = parsed
	return nil
}
