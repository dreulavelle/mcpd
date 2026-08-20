package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// pluginStore namespaces plugin_state rows to one plugin.
type pluginStore struct {
	db     *sqlite.DB
	plugin string
}

func newPluginStore(db *sqlite.DB, plugin string) *pluginStore {
	return &pluginStore{db: db, plugin: plugin}
}

func (s *pluginStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var v string
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT value_json FROM plugin_state WHERE plugin = ? AND key = ?`,
		s.plugin, key).Scan(&v)
	if err != nil {
		// A missing key is an ordinary outcome, not an error to propagate.
		return nil, false, nil
	}
	return []byte(v), true, nil
}

func (s *pluginStore) Put(ctx context.Context, key string, value []byte) error {
	if !json.Valid(value) {
		return fmt.Errorf("plugin store: value for %q is not valid JSON", key)
	}
	return s.db.WriteTx(ctx, nowMillis(), func(u *sqlite.UnitOfWork) error {
		return u.PluginStatePut(s.plugin, key, string(value))
	})
}

func (s *pluginStore) Delete(ctx context.Context, key string) error {
	return s.db.WriteTx(ctx, nowMillis(), func(u *sqlite.UnitOfWork) error {
		return u.PluginStateDelete(s.plugin, key)
	})
}

// pluginPublisher binds a plugin to its own event namespace, so a plugin
// cannot publish under another plugin's subject.
type pluginPublisher struct {
	db     *sqlite.DB
	plugin string
}

func newPluginPublisher(db *sqlite.DB, plugin string) *pluginPublisher {
	return &pluginPublisher{db: db, plugin: plugin}
}

func (p *pluginPublisher) Publish(ctx context.Context, subject string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("plugin publisher: encode payload: %w", err)
	}
	full := "mcp.plugin." + p.plugin + "." + subject
	return p.db.WriteTx(ctx, nowMillis(), func(u *sqlite.UnitOfWork) error {
		return u.EnqueueEvent(full, "", "", body)
	})
}

// pluginSecrets resolves only the secrets declared under this plugin's own
// configuration block, so one plugin cannot read another's credentials.
type pluginSecrets struct {
	settings map[string]any
	resolver *config.SecretResolver
	plugin   string
}

func newPluginSecrets(cfg *config.Config, plugin string) *pluginSecrets {
	return &pluginSecrets{
		settings: cfg.Plugins[plugin].Settings,
		resolver: config.NewSecretResolver(),
		plugin:   plugin,
	}
}

func (s *pluginSecrets) Secret(name string) (string, error) {
	raw, ok := s.settings[name]
	if !ok {
		return "", fmt.Errorf("plugin %s: no secret named %q in its configuration", s.plugin, name)
	}
	ref, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("plugin %s: secret %q must be a reference string", s.plugin, name)
	}
	return s.resolver.Resolve(ref)
}
