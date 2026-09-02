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
		// Already gone is the outcome asked for. A tunnel deleted in
		// OpenAI's own dashboard answers 404 here, and refusing on it left
		// the assignment in place -- the one thing that keeps mcpd
		// reporting the tunnel on every restart.
		var req *tcadmin.RequestError
		if errors.As(err, &req) && req.StatusCode == http.StatusNotFound {
			return nil
		}
		return d.explain(err)
	}
	return nil
}

// Exists reports whether this directory's organisation has a tunnel.
//
// The admin API is scoped to one organisation, so a 404 means the tunnel is
// not in this one: deleted, or made in another. Anything else is the
// question not being answered, and is returned as an error so a caller does
// not mark a tunnel missing because the admin key expired.
func (d *Directory) Exists(ctx context.Context, id string) (bool, error) {
	client, err := d.client()
	if err != nil {
		return false, err
	}
	if !tunnelIDPattern(id) {
		return false, fmt.Errorf("tunnel: %q is not a tunnel id", id)
	}
	_, err = client.GetTunnel(ctx, id)
	if err == nil {
		return true, nil
	}
	var req *tcadmin.RequestError
	if errors.As(err, &req) && req.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, d.explain(err)
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
// Reasons a request to OpenAI's control plane was refused.
//
// Stable strings rather than prose, because the dashboard renders the
// explanation and prose cannot be branched on. What each one means for an
// operator is written once, in the page that shows it -- a paragraph is not a
// thing an error return should carry, and flattened into a toast it is worse
// than no explanation at all.
const (
	// ReasonAdminKeyRejected: the key is not an admin key, or is revoked.
	ReasonAdminKeyRejected = "openai_admin_key_rejected"
	// ReasonTunnelsManageRequired: the key is real, and its creator's role
	// does not carry tunnel management.
	ReasonTunnelsManageRequired = "openai_tunnels_manage_required"
	// ReasonOrgIDRejected: the organization id is not one.
	ReasonOrgIDRejected = "openai_org_id_rejected"
)

// Refused builds a refusal with the same reason and a fuller sentence, for a
// caller that has learned something the control plane's one line did not
// say -- such as that the same key can list tunnels and so lacks only the
// scope to make them.
func Refused(reason, msg string) error {
	return &refusal{reason: reason, msg: msg}
}

// refusal is an error that names why OpenAI said no.
type refusal struct {
	reason string
	msg    string
}

func (r *refusal) Error() string { return r.msg }

// Reason reports why OpenAI refused, or "" for anything else.
//
// The dashboard branches on this to decide which explanation to show, so it is
// the contract rather than the message beside it.
func Reason(err error) string {
	var r *refusal
	if errors.As(err, &r) {
		return r.reason
	}
	return ""
}

// explain turns the control plane's rejection into something an operator can
// act on.
//
// It reads the typed error rather than searching the message, because the
// message ends with a request id -- and a request id is hex, so one containing
// "403" would otherwise be diagnosed as a permissions problem.
//
// One sentence each. The rest belongs where it can be laid out.
func (d *Directory) explain(err error) error {
	if err == nil {
		return nil
	}

	var req *tcadmin.RequestError
	if errors.As(err, &req) {
		switch req.StatusCode {
		case http.StatusUnauthorized:
			return &refusal{ReasonAdminKeyRejected,
				"OpenAI did not recognise that admin key."}
		case http.StatusForbidden:
			// Two things gate this independently: the org role of whoever made
			// the key, and the scopes chosen for the key itself. An earlier
			// version of this said the role was the whole story and that
			// making another key would not help -- which is wrong, and worse
			// than saying nothing, because regenerating the key is exactly
			// what fixes the common case. The dashboard lays out both.
			return &refusal{ReasonTunnelsManageRequired,
				"That admin key is not allowed to manage tunnels."}
		case http.StatusBadRequest:
			if strings.Contains(req.ResponseBody, "organization_id") ||
				strings.Contains(req.Message, "organization_id") {
				return &refusal{ReasonOrgIDRejected,
					"OpenAI did not accept that organization ID."}
			}
		}
	}
	return redactKey(err, d.adminKey)
}

// defaultControlPlaneBaseURL matches the embedded client's own default.
const defaultControlPlaneBaseURL = "https://api.openai.com"
