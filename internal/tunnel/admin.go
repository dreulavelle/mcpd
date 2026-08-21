package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	adminKey    string
	orgID       string
	workspaceID string
	baseURL     string
}

// ErrNoAdminKey means tunnel management is unavailable, not that it failed.
var ErrNoAdminKey = errors.New("tunnel: no admin key is configured")

// NewDirectory returns a directory. Missing credentials leave it unavailable
// rather than failing, so the dashboard can offer paste-an-id instead.
func NewDirectory(adminKey, orgID, workspaceID, baseURL string) *Directory {
	return &Directory{
		adminKey:    strings.TrimSpace(adminKey),
		orgID:       strings.TrimSpace(orgID),
		workspaceID: strings.TrimSpace(workspaceID),
		baseURL:     baseURL,
	}
}

// Available reports whether tunnels can be listed and created.
//
// Both credentials are needed. The API scopes every tunnel request to exactly
// one organisation and rejects a request that names none, so an admin key on
// its own cannot list anything.
func (d *Directory) Available() bool {
	return d != nil && d.adminKey != "" && d.orgID != ""
}

// Missing names which credential is absent, for a dashboard that would
// otherwise say only that the feature is off.
func (d *Directory) Missing() string {
	switch {
	case d == nil, d.adminKey == "" && d.orgID == "":
		return "an OpenAI admin key and organization ID"
	case d.adminKey == "":
		return "an OpenAI admin key"
	case d.orgID == "":
		return "your OpenAI organization ID"
	}
	return ""
}

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
	resp, err := client.ListTunnels(ctx, d.orgID, "", "")
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
	// A workspace is sent alongside the organisation when one is configured.
	// OpenAI's own CLI requires at least one of the two and passes both when
	// it has them, and a tunnel scoped only to the organisation is not
	// necessarily visible where ChatGPT is looking.
	req := tcadmin.TunnelCreateRequest{
		Name:            name,
		Description:     description,
		OrganizationIDs: []string{d.orgID},
	}
	if d.workspaceID != "" {
		req.WorkspaceIDs = []string{d.workspaceID}
	}
	t, err := client.CreateTunnel(ctx, req)
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
		BaseURL:         parsed,
		AdminKey:        d.adminKey,
		OrganizationIDs: []string{d.orgID},
	})
}

// explain turns the control plane's rejection into something an operator can
// act on.
//
// It reads the typed error rather than searching the message, because the
// message ends with a request id -- and a request id is hex, so one containing
// "403" would otherwise be diagnosed as a permissions problem.
func (d *Directory) explain(err error) error {
	if err == nil {
		return nil
	}

	var req *tcadmin.RequestError
	if errors.As(err, &req) {
		switch req.StatusCode {
		case http.StatusUnauthorized:
			return errors.New("OpenAI rejected that admin key. Admin keys are made " +
				"under Settings, Organization, Admin keys -- the runtime key that " +
				"runs the tunnel will not work here")
		case http.StatusForbidden:
			// The permission sits on the principal that made the key, not on
			// the key, so this is fixed under Organization, People, Roles --
			// not by making another key.
			return errors.New("that key is not allowed to manage tunnels. The " +
				"person who created it needs a role with Tunnels: Manage, under " +
				"Settings, Organization, People, Roles")
		case http.StatusBadRequest:
			if strings.Contains(req.ResponseBody, "organization_id") ||
				strings.Contains(req.Message, "organization_id") {
				return errors.New("that organization ID was not accepted. Find it " +
					"under Settings, Organization, General -- it starts with org_")
			}
		}
	}
	return redactKey(err, d.adminKey)
}

// defaultControlPlaneBaseURL matches the embedded client's own default.
const defaultControlPlaneBaseURL = "https://api.openai.com"
