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
	adminKey string
	orgID    string
	baseURL  string
}

// ErrNoAdminKey means tunnel management is unavailable, not that it failed.
var ErrNoAdminKey = errors.New("tunnel: no admin key is configured")

// NewDirectory returns a directory. Missing credentials leave it unavailable
// rather than failing, so the dashboard can offer paste-an-id instead.
func NewDirectory(adminKey, orgID, baseURL string) *Directory {
	return &Directory{
		adminKey: strings.TrimSpace(adminKey),
		orgID:    strings.TrimSpace(orgID),
		baseURL:  baseURL,
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
	// WorkspaceIDs are the ChatGPT workspaces this tunnel is listed in.
	//
	// Carried through because there is no endpoint that lists workspaces --
	// not in the SDK and not in the documentation -- and an existing tunnel is
	// the only place a workspace id can be read from rather than typed.
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
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
		out = append(out, TunnelInfo{
			ID: t.ID, Name: t.Name, Description: t.Description,
			WorkspaceIDs: append([]string(nil), t.WorkspaceIDs...),
		})
	}
	return out, nil
}

// Create makes a new tunnel and returns it.
// Create makes a tunnel, optionally listed in a ChatGPT workspace.
//
// The workspace matters more than it looks. A tunnel associated only with a
// Platform organisation does not appear in an Enterprise or Edu workspace, so
// a connector created without one is invisible in exactly the accounts that
// have workspaces at all.
func (d *Directory) Create(ctx context.Context, name, description, workspaceID string) (*TunnelInfo, error) {
	client, err := d.client()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("tunnel: a name is required")
	}
	req := tcadmin.TunnelCreateRequest{
		Name:            name,
		Description:     description,
		OrganizationIDs: []string{d.orgID},
	}
	if w := strings.TrimSpace(workspaceID); w != "" {
		req.WorkspaceIDs = []string{w}
	}
	t, err := client.CreateTunnel(ctx, req)
	if err != nil {
		return nil, d.explain(err)
	}
	return &TunnelInfo{
		ID: t.ID, Name: t.Name, Description: t.Description,
		WorkspaceIDs: append([]string(nil), t.WorkspaceIDs...),
	}, nil
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
			return errors.New("OpenAI did not recognise that admin key.\n\n" +
				"Admin keys are made at platform.openai.com under " +
				"Settings > Organization > Admin keys. They are a different " +
				"thing from the runtime API key a tunnel uses to carry " +
				"traffic, and pasting a runtime key here is always refused.\n\n" +
				"Check the whole value was copied, and that the key has not " +
				"been revoked.")
		case http.StatusForbidden:
			// The permission is evaluated against the person who created the
			// key rather than against the key, so the obvious remedy -- make
			// another key -- produces an identical refusal. Saying that first
			// is the whole value of this message.
			return errors.New("OpenAI accepted that admin key but will not let " +
				"it manage tunnels.\n\n" +
				"Making another key will not help. The permission comes from " +
				"the OpenAI role of the person who created the key, not from " +
				"the key itself, so a replacement made by the same person is " +
				"refused in exactly the same way.\n\n" +
				"Two ways forward:\n" +
				"  1. Give that person a role including \"Tunnels: Manage\", at " +
				"platform.openai.com under Settings > Organization > People, " +
				"then open their member row and change the role. Try the same " +
				"key again before making a new one.\n" +
				"  2. Or have somebody who already holds that role create the " +
				"admin key, and paste theirs instead.\n\n" +
				"Only an organization owner can change roles. If that is not " +
				"you, this is the sentence to send them.")
		case http.StatusBadRequest:
			if strings.Contains(req.ResponseBody, "organization_id") ||
				strings.Contains(req.Message, "organization_id") {
				return errors.New("OpenAI did not accept that organization ID.\n\n" +
					"Find it at platform.openai.com under " +
					"Settings > Organization > General. It begins with " +
					"\"org_\" -- an organization name, an email address or a " +
					"project ID will not work in its place.")
			}
		}
	}
	return redactKey(err, d.adminKey)
}

// defaultControlPlaneBaseURL matches the embedded client's own default.
const defaultControlPlaneBaseURL = "https://api.openai.com"
