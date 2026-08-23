package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Legacy holds the settings config.yaml used to supply and no longer does.
//
// They live in the database now, and the database is the only authority for
// them: this struct exists so that the first start after an upgrade can read
// what a deployment had and put it there, once, and so that a key still
// present in the file afterwards can be named rather than silently ignored.
//
// Every field is a pointer, because presence is the whole question. A file
// that says nothing about read_timeout and a file that sets it to the default
// are different situations -- the first has nothing to import and nothing to
// warn about, and the second has both -- and a zero value cannot tell them
// apart.
type Legacy struct {
	Server struct {
		PublicURL         *string `yaml:"public_url"`
		FrontendPublicURL *string `yaml:"frontend_public_url"`
		FrontendEnabled   *bool   `yaml:"frontend_enabled"`
		TLS               struct {
			Mode *string `yaml:"mode"`
		} `yaml:"tls"`
		ReadHeaderTimeout *time.Duration `yaml:"read_header_timeout"`
		ReadTimeout       *time.Duration `yaml:"read_timeout"`
		WriteTimeout      *time.Duration `yaml:"write_timeout"`
		IdleTimeout       *time.Duration `yaml:"idle_timeout"`
		ShutdownTimeout   *time.Duration `yaml:"shutdown_timeout"`
	} `yaml:"server"`

	Storage struct {
		BusyTimeout       *time.Duration `yaml:"busy_timeout"`
		RelaxedDurability *bool          `yaml:"relaxed_durability"`
	} `yaml:"storage"`

	Auth struct {
		Accounts struct {
			SessionTTL *time.Duration `yaml:"session_ttl"`
		} `yaml:"accounts"`
	} `yaml:"auth"`

	Approval struct {
		ProposalTTL   *time.Duration `yaml:"proposal_ttl"`
		ApprovalTTL   *time.Duration `yaml:"approval_ttl"`
		LeaseTTL      *time.Duration `yaml:"lease_ttl"`
		InlineMaxRisk *string        `yaml:"inline_max_risk"`
	} `yaml:"approval"`

	Logging struct {
		Level  *string `yaml:"level"`
		Format *string `yaml:"format"`
	} `yaml:"logging"`

	Tunnel struct {
		Enabled             *bool     `yaml:"enabled"`
		TunnelID            *string   `yaml:"tunnel_id"`
		APIKeyRef           *string   `yaml:"api_key_ref"`
		Principal           *string   `yaml:"principal"`
		Role                *string   `yaml:"role"`
		Plugins             *[]string `yaml:"plugins"`
		ControlPlaneBaseURL *string   `yaml:"control_plane_base_url"`
		CheckForUpdates     *bool     `yaml:"check_for_updates"`
		DiagnosticsAddr     *string   `yaml:"diagnostics_addr"`
	} `yaml:"tunnel"`

	// sources records where each supplied value came from, keyed by its path
	// in the file. An environment override names the variable instead, so a
	// warning about a value that is being ignored points at the thing that
	// actually sets it rather than at a file that does not mention it.
	sources map[string]string
}

// SourceFile is what Sources reports for a value the file itself supplied.
const SourceFile = "config.yaml"

// Sources reports where each supplied value came from, keyed by its path in
// config.yaml.
func (l *Legacy) Sources() map[string]string {
	out := make(map[string]string, len(l.sources))
	for k, v := range l.sources {
		out[k] = v
	}
	return out
}

// Any reports whether the file or the environment supplies any moved value.
func (l *Legacy) Any() bool { return len(l.sources) > 0 }

// parseLegacy reads the moved keys out of a configuration file and layers the
// environment overrides that used to apply to them.
func parseLegacy(raw []byte) (*Legacy, error) {
	l := &Legacy{sources: map[string]string{}}
	if err := yaml.Unmarshal(raw, l); err != nil {
		// Load has already parsed the same bytes into Config, so a syntax
		// error cannot reach here. A type error can: a key whose shape changed
		// between versions.
		return nil, fmt.Errorf("config: the settings that have moved to the "+
			"database could not be read from this file: %w", err)
	}
	for _, path := range l.present() {
		l.sources[path] = SourceFile
	}
	if err := l.applyEnv(); err != nil {
		return nil, err
	}
	return l, nil
}

// present lists the paths the file itself set.
func (l *Legacy) present() []string {
	var out []string
	mark := func(path string, set bool) {
		if set {
			out = append(out, path)
		}
	}
	mark("server.public_url", l.Server.PublicURL != nil)
	mark("server.frontend_public_url", l.Server.FrontendPublicURL != nil)
	mark("server.frontend_enabled", l.Server.FrontendEnabled != nil)
	mark("server.tls.mode", l.Server.TLS.Mode != nil)
	mark("server.read_header_timeout", l.Server.ReadHeaderTimeout != nil)
	mark("server.read_timeout", l.Server.ReadTimeout != nil)
	mark("server.write_timeout", l.Server.WriteTimeout != nil)
	mark("server.idle_timeout", l.Server.IdleTimeout != nil)
	mark("server.shutdown_timeout", l.Server.ShutdownTimeout != nil)
	mark("storage.busy_timeout", l.Storage.BusyTimeout != nil)
	mark("storage.relaxed_durability", l.Storage.RelaxedDurability != nil)
	mark("auth.accounts.session_ttl", l.Auth.Accounts.SessionTTL != nil)
	mark("approval.proposal_ttl", l.Approval.ProposalTTL != nil)
	mark("approval.approval_ttl", l.Approval.ApprovalTTL != nil)
	mark("approval.lease_ttl", l.Approval.LeaseTTL != nil)
	mark("approval.inline_max_risk", l.Approval.InlineMaxRisk != nil)
	mark("logging.level", l.Logging.Level != nil)
	mark("logging.format", l.Logging.Format != nil)
	mark("tunnel.enabled", l.Tunnel.Enabled != nil)
	mark("tunnel.tunnel_id", l.Tunnel.TunnelID != nil)
	mark("tunnel.api_key_ref", l.Tunnel.APIKeyRef != nil)
	mark("tunnel.principal", l.Tunnel.Principal != nil)
	mark("tunnel.role", l.Tunnel.Role != nil)
	mark("tunnel.plugins", l.Tunnel.Plugins != nil)
	mark("tunnel.control_plane_base_url", l.Tunnel.ControlPlaneBaseURL != nil)
	mark("tunnel.check_for_updates", l.Tunnel.CheckForUpdates != nil)
	mark("tunnel.diagnostics_addr", l.Tunnel.DiagnosticsAddr != nil)
	return out
}

// applyEnv layers the MCPD_ overrides that used to apply to these keys.
//
// They still work, and they still work exactly once: like the file, they seed
// the store on the first start and are ignored afterwards. A container that
// sets MCPD_PUBLIC_URL on a fresh volume therefore comes up configured, and
// one that keeps setting it after somebody has changed the address in the
// dashboard is told the two disagree instead of quietly winning.
func (l *Legacy) applyEnv() error {
	if v, ok := lookupEnv("PUBLIC_URL"); ok {
		l.Server.PublicURL = &v
		l.sources["server.public_url"] = envPrefix + "PUBLIC_URL"
	}
	if v, ok := lookupEnv("FRONTEND_ENABLED"); ok {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: %sFRONTEND_ENABLED=%q is not a boolean; "+
				"use true or false", envPrefix, v)
		}
		l.Server.FrontendEnabled = &parsed
		l.sources["server.frontend_enabled"] = envPrefix + "FRONTEND_ENABLED"
	}
	if v, ok := lookupEnv("LOG_LEVEL"); ok {
		l.Logging.Level = &v
		l.sources["logging.level"] = envPrefix + "LOG_LEVEL"
	}
	if v, ok := lookupEnv("LOG_FORMAT"); ok {
		l.Logging.Format = &v
		l.sources["logging.format"] = envPrefix + "LOG_FORMAT"
	}
	return nil
}

func lookupEnv(name string) (string, bool) {
	v, ok := os.LookupEnv(envPrefix + name)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
