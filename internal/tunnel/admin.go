package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	tcconfig "github.com/openai/tunnel-client/pkg/config"
	tcadmin "github.com/openai/tunnel-client/pkg/controlplane/admin"
)

// Directory manages tunnels in an OpenAI organisation.
//
// It is separate from everything else in this package because it uses a
// different credential and answers a different question. A runtime key runs a
// tunnel; an admin key creates and deletes them. Keeping them apart is not
// bookkeeping -- an admin key leaked from a long-running daemon can delete
// every tunnel in the organisation, so it is used only for the request an
// operator explicitly asked for and never held by the running client.
type Directory struct {
	adminKey string
	baseURL  string
}

// ErrNoAdminKey means tunnel management is unavailable, not that it failed.
var ErrNoAdminKey = errors.New("tunnel: no admin key is configured")

// NewDirectory returns a directory. An empty key leaves it unavailable rather
// than failing, so the dashboard can offer paste-an-id instead.
func NewDirectory(adminKey, baseURL string) *Directory {
	return &Directory{adminKey: strings.TrimSpace(adminKey), baseURL: baseURL}
}

// Available reports whether tunnels can be listed and created.
func (d *Directory) Available() bool { return d != nil && d.adminKey != "" }

// TunnelInfo is one tunnel as OpenAI knows it.
type TunnelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// List returns every tunnel in the organisation.
func (d *Directory) List(ctx context.Context) ([]TunnelInfo, error) {
	client, err := d.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.ListTunnels(ctx, "", "", "")
	if err != nil {
		return nil, d.explain(err)
	}
	out := make([]TunnelInfo, 0, len(resp.Tunnels))
	for _, t := range resp.Tunnels {
		out = append(out, TunnelInfo{ID: t.ID, Name: t.Name, Description: t.Description})
	}
	return out, nil
}

// Create makes a new tunnel and returns it.
func (d *Directory) Create(ctx context.Context, name, description string) (*TunnelInfo, error) {
	client, err := d.client()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("tunnel: a name is required")
	}
	t, err := client.CreateTunnel(ctx, tcadmin.TunnelCreateRequest{
		Name:        name,
		Description: description,
	})
	if err != nil {
		return nil, d.explain(err)
	}
	return &TunnelInfo{ID: t.ID, Name: t.Name, Description: t.Description}, nil
}

// Delete removes a tunnel from the organisation.
//
// This is not reversible and it is not local: any connector anywhere pointing
// at this tunnel stops working. The caller is responsible for having asked.
func (d *Directory) Delete(ctx context.Context, id string) error {
	client, err := d.client()
	if err != nil {
		return err
	}
	if !tunnelIDPattern(id) {
		return fmt.Errorf("tunnel: %q is not a tunnel id", id)
	}
	if _, err := client.DeleteTunnel(ctx, id); err != nil {
		return d.explain(err)
	}
	return nil
}

func (d *Directory) client() (*tcadmin.AdminTunnelClient, error) {
	if !d.Available() {
		return nil, ErrNoAdminKey
	}
	base := strings.TrimSpace(d.baseURL)
	if base == "" {
		base = defaultControlPlaneBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("tunnel: %q is not a usable control-plane URL", base)
	}
	return tcadmin.NewAdminTunnelClient(&tcconfig.AdminConfig{
		BaseURL:  parsed,
		AdminKey: d.adminKey,
	})
}

// explain turns the control plane's rejection into something an operator can
// act on. The two keys look alike and are made on adjacent pages, so "401" on
// its own sends people to check the wrong one.
func (d *Directory) explain(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "invalid_api_key"):
		return errors.New("OpenAI rejected that admin key. Admin keys are made under " +
			"Settings, Organization, Admin keys -- a runtime API key will not work here, " +
			"even though it is the right key for running the tunnel")
	case strings.Contains(msg, "403"):
		return errors.New("that admin key is valid but not allowed to manage tunnels. " +
			"It needs the Tunnels: Manage permission")
	}
	return redactKey(err, d.adminKey)
}

// defaultControlPlaneBaseURL matches the embedded client's own default.
const defaultControlPlaneBaseURL = "https://api.openai.com"
