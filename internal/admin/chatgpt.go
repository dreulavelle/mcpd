package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/tunnel"
)

// accountView is one ChatGPT account as the dashboard sees it.
//
// No credential is ever in here, and that is the point rather than an
// oversight. The dashboard needs to know whether a key is set, not what it is:
// an operator who has forgotten a key replaces it, and a page that could show
// one is a page that leaks every key to anyone who reaches it. HasAdminKey is
// the only thing said about the admin credential, because whether tunnels can
// be created from an account is a fact the page has to render.
type accountView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Principal string `json:"principal"`
	Role      string `json:"role"`
	// Plugins is what this account may reach, or ["*"].
	Plugins    []string `json:"plugins"`
	RatePerSec float64  `json:"rate_per_sec"`
	Enabled    bool     `json:"enabled"`
	OrgID      string   `json:"organization_id,omitempty"`
	// HasAdminKey reports whether tunnels can be made and deleted from this
	// account, without saying anything about the key itself.
	HasAdminKey bool `json:"has_admin_key"`
	// CanManage, Missing and Problem describe this account's own access to its
	// organisation. Per account rather than per host: one workspace's expired
	// admin key says nothing about another's.
	CanManage bool      `json:"can_manage"`
	Missing   string    `json:"missing,omitempty"`
	Problem   string    `json:"problem,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// availableTunnel is a tunnel in an organisation, with the account it was
// listed from. Two accounts can hold tunnels with different ids and the same
// name, so the id alone does not say which workspace one belongs to.
type availableTunnel struct {
	tunnel.TunnelInfo
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
}

func newAccountView(a tunnel.Account) accountView {
	return accountView{
		ID:          a.ID,
		Name:        a.Name,
		Principal:   a.Principal,
		Role:        string(a.Role),
		Plugins:     a.Plugins,
		RatePerSec:  a.RatePerSec,
		Enabled:     a.Enabled,
		OrgID:       a.OrgID,
		HasAdminKey: strings.TrimSpace(a.AdminKey) != "",
		CreatedAt:   a.CreatedAt,
	}
}

// chatgptAccounts reads the accounts, or an empty list when there are none to
// read. The error is returned rather than swallowed so a host with no
// encryption key says why its accounts are missing.
func (s *Server) chatgptAccounts(ctx context.Context) ([]tunnel.Account, error) {
	if s.opts.ChatGPTAccounts == nil {
		return nil, nil
	}
	return s.opts.ChatGPTAccounts(ctx)
}

// resolveAccount settles which account a tunnel request means.
//
// An empty id is the only account when there is exactly one, and an error when
// there are several. Choosing on the operator's behalf would be choosing whose
// credential a connector authenticates with and whose organisation a tunnel is
// created in -- neither of which this host can infer.
func (s *Server) resolveAccount(ctx context.Context, id string) (tunnel.Account, error) {
	accounts, err := s.chatgptAccounts(ctx)
	if err != nil {
		return tunnel.Account{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		switch len(accounts) {
		case 0:
			return tunnel.Account{}, fmt.Errorf(
				"there are no ChatGPT accounts yet; add one on the ChatGPT page")
		case 1:
			return accounts[0], nil
		default:
			return tunnel.Account{}, fmt.Errorf(
				"say which ChatGPT account this is for; there are %d", len(accounts))
		}
	}
	for _, a := range accounts {
		if a.ID == id {
			return a, nil
		}
	}
	return tunnel.Account{}, fmt.Errorf("there is no ChatGPT account with that id")
}

// handleListChatGPTAccounts lists the accounts, without their credentials.
func (s *Server) handleListChatGPTAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.chatgptAccounts(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	// Empty rather than nil: the page maps over these, and a null would blank
	// it on the ordinary state of a new install.
	views := []accountView{}
	for _, a := range accounts {
		view := newAccountView(a)
		dir := s.directory(a.ID)
		view.Missing = dir.Missing()
		view.CanManage = dir.Available()
		views = append(views, view)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"accounts": views,
		// What an account may be granted, so the form offers the systems this
		// host actually has rather than a free-text box.
		"plugins": s.pluginNames(),
	})
}

// accountBody is what the form sends.
//
// Pointers on every field that can be edited, so that "not sent" and "set to
// empty" stay different instructions. It matters most for the keys: the page
// never reads one back, so an edit that changes only the rate limit arrives
// with no key at all, and a plain string would read that as an erasure.
type accountBody struct {
	Name       *string   `json:"name"`
	APIKey     *string   `json:"api_key"`
	AdminKey   *string   `json:"admin_key"`
	OrgID      *string   `json:"organization_id"`
	Role       *string   `json:"role"`
	Plugins    *[]string `json:"plugins"`
	RatePerSec *float64  `json:"rate_per_sec"`
	Enabled    *bool     `json:"enabled"`
}

// handleAddChatGPTAccount stores a new account.
func (s *Server) handleAddChatGPTAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.AddChatGPTAccount == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store ChatGPT accounts; no encryption key is configured")
		return
	}
	var body accountBody
	if !s.decode(w, r, &body) {
		return
	}

	acct := tunnel.Account{Enabled: true, Role: auth.RoleUser}
	if body.Name != nil {
		acct.Name = *body.Name
	}
	if body.APIKey != nil {
		acct.APIKey = *body.APIKey
	}
	if body.AdminKey != nil {
		acct.AdminKey = *body.AdminKey
	}
	if body.OrgID != nil {
		acct.OrgID = *body.OrgID
	}
	if body.Role != nil {
		acct.Role = auth.Role(*body.Role)
	}
	if body.Plugins != nil {
		acct.Plugins = *body.Plugins
	}
	if body.RatePerSec != nil {
		acct.RatePerSec = *body.RatePerSec
	}
	if body.Enabled != nil {
		acct.Enabled = *body.Enabled
	}
	if err := s.checkAccountPlugins(acct.Plugins); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	created, err := s.opts.AddChatGPTAccount(r.Context(), auth.FromContext(r.Context()).ID, acct)
	if err != nil {
		s.writeError(w, r, accountStatus(err), err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusCreated, newAccountView(created))
}

// handleUpdateChatGPTAccount edits one, leaving unsent fields alone.
func (s *Server) handleUpdateChatGPTAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.UpdateChatGPTAccount == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store ChatGPT accounts; no encryption key is configured")
		return
	}
	var body accountBody
	if !s.decode(w, r, &body) {
		return
	}

	up := tunnel.AccountUpdate{
		Name: body.Name, APIKey: body.APIKey, AdminKey: body.AdminKey,
		OrgID: body.OrgID, Plugins: body.Plugins,
		RatePerSec: body.RatePerSec, Enabled: body.Enabled,
	}
	if body.Role != nil {
		role := auth.Role(*body.Role)
		up.Role = &role
	}
	if body.Plugins != nil {
		if err := s.checkAccountPlugins(*body.Plugins); err != nil {
			s.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
	}

	updated, err := s.opts.UpdateChatGPTAccount(r.Context(),
		auth.FromContext(r.Context()).ID, r.PathValue("id"), up)
	if err != nil {
		s.writeError(w, r, accountStatus(err), err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, newAccountView(updated))
}

// handleRemoveChatGPTAccount forgets an account and unassigns its tunnels.
func (s *Server) handleRemoveChatGPTAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.RemoveChatGPTAccount == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store ChatGPT accounts; no encryption key is configured")
		return
	}
	err := s.opts.RemoveChatGPTAccount(r.Context(),
		auth.FromContext(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, accountStatus(err), err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "removed"})
}

// checkAccountPlugins refuses a grant naming a system this host does not have.
//
// The wildcard is always allowed: it means "everything mounted", which stays
// true as instances come and go, and refusing it because some name does not
// exist yet would refuse the ordinary case.
func (s *Server) checkAccountPlugins(plugins []string) error {
	known := s.pluginNames()
	for _, p := range plugins {
		p = strings.TrimSpace(p)
		if p == "" || p == auth.Wildcard {
			continue
		}
		found := false
		for _, name := range known {
			if name == p {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("there is no system called %q", p)
		}
	}
	return nil
}

func (s *Server) pluginNames() []string {
	if s.opts.Plugins == nil {
		return []string{}
	}
	if names := s.opts.Plugins(); names != nil {
		return names
	}
	return []string{}
}

// accountStatus maps a store refusal onto the status that describes it.
//
// A name already taken and a validation failure are both the caller's to fix,
// and an account that is not there is not there. Everything else is this
// host's problem and says so, rather than being reported as a bad request the
// operator can do nothing about.
func accountStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists"):
		return http.StatusConflict
	case strings.Contains(msg, "no such ChatGPT account"):
		return http.StatusNotFound
	case strings.Contains(msg, "chatgpt account:"):
		return http.StatusBadRequest
	case strings.Contains(msg, "no encryption key"):
		return http.StatusNotImplemented
	}
	return http.StatusInternalServerError
}
