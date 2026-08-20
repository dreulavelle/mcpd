// Package cnmaestro integrates Cambium Networks cnMaestro.
//
// It is the reference plugin: it exercises the full contract -- read tools,
// typed approval-gated mutations, health reporting, and an upstream client
// with its own authentication. Everything specific to cnMaestro lives here,
// and nothing about cnMaestro leaks into the platform.
package cnmaestro

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the cnMaestro integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	mu       sync.RWMutex
	lastErr  error
	lastPing time.Time
}

// New constructs the plugin from its configuration block.
//
// Credentials are resolved here, once, so a missing secret fails startup
// rather than producing a plugin that returns an authentication error on every
// call.
func New(deps plugins.Deps, cfg Config) (*Plugin, error) {
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	clientID, err := deps.Secrets.Secret("client_id_ref")
	if err != nil {
		return nil, fmt.Errorf("cnmaestro: client id: %w", err)
	}
	secret, err := deps.Secrets.Secret("client_secret_ref")
	if err != nil {
		return nil, fmt.Errorf("cnmaestro: client secret: %w", err)
	}

	httpClient := deps.HTTP
	if cfg.InsecureSkipVerify {
		// On-Premises appliances commonly ship a self-signed certificate.
		// This is a deliberate, logged downgrade rather than a silent default,
		// and it applies only to this plugin's client.
		deps.Log.Warn("TLS certificate verification is DISABLED for cnMaestro; " +
			"the API token is exposed to anyone who can intercept this connection")
		httpClient = cloneWithInsecureTLS(httpClient)
	}
	if cfg.Timeout > 0 {
		c := *httpClient
		c.Timeout = cfg.Timeout
		httpClient = &c
	}

	p := &Plugin{deps: deps, cfg: cfg}
	p.client = NewClient(httpClient, cfg, clientID, secret, deps.Log, deps.Now)
	return p, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "cnmaestro",
		Version: "1.0.0",
		Title:   "Cambium cnMaestro",
		Description: "Manage a Cambium Networks wireless estate: devices, clients, " +
			"radios, alarms and events. Reads are immediate. Every change is a " +
			"proposal that a human must approve before it is applied, and no tool " +
			"here can execute arbitrary commands on a device.",
	}
}

// Register implements plugins.Plugin.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerReadTools(r)
	p.registerMutations(r)
	return nil
}

// Start implements plugins.Starter. It verifies credentials so a
// misconfiguration surfaces at boot rather than at the first tool call.
func (p *Plugin) Start(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := p.client.Ping(pingCtx); err != nil {
		p.recordPing(err)
		return fmt.Errorf("cnmaestro: could not reach the controller: %w", err)
	}
	p.recordPing(nil)
	p.deps.Log.Info("cnmaestro ready",
		"base_url", p.cfg.BaseURL,
		"api_host", p.client.tokens.APIHost(),
		"managed_account", p.cfg.ManagedAccount)
	return nil
}

// Check implements plugins.Checker.
//
// A controller outage marks this plugin degraded rather than the host
// unhealthy: mcpd is still serving, and other plugins are unaffected.
func (p *Plugin) Check(ctx context.Context) plugins.Health {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := p.client.Ping(checkCtx); err != nil {
		p.recordPing(err)
		// The message reaches the readiness endpoint, which is unauthenticated,
		// so it names the failure without quoting the upstream response.
		return plugins.Degraded("cnMaestro API is unreachable")
	}
	p.recordPing(nil)
	return plugins.Healthy()
}

func (p *Plugin) recordPing(err error) {
	p.mu.Lock()
	p.lastErr = err
	p.lastPing = p.deps.Now()
	p.mu.Unlock()
}

// cloneWithInsecureTLS returns a copy of the client with verification off,
// leaving the host's shared client untouched.
func cloneWithInsecureTLS(base *http.Client) *http.Client {
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	cloned := transport.Clone()
	if cloned.TLSClientConfig == nil {
		cloned.TLSClientConfig = &tls.Config{}
	}
	cloned.TLSClientConfig.InsecureSkipVerify = true

	c := *base
	c.Transport = cloned
	return &c
}
