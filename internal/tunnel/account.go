package tunnel

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

// Account is one ChatGPT account this host connects to.
//
// An account owns credentials and a grant; a tunnel owns an endpoint. Keeping
// them apart is what lets several workspaces share one mcpd: each connects
// with its own key, acts as its own identity in the audit trail, and reaches
// only what its account was granted -- while the tunnels themselves stay what
// they were, one per connector.
//
// The zero value is not usable. Validate says why.
type Account struct {
	ID   string
	Name string

	// APIKey is the *runtime* key, in the clear. It is decrypted on the way
	// out of the store and never logged; an admin key is a different
	// credential and is deliberately not interchangeable with it.
	APIKey string
	// AdminKey and OrgID create and delete tunnels in this account's
	// organisation. Both empty is a valid account -- it can still run a
	// tunnel whose id was pasted in -- but either alone can do nothing,
	// because the API scopes every tunnel request to one organisation.
	AdminKey string
	OrgID    string

	// Principal is the identity calls through this account's tunnels act as.
	// It is what the audit trail records, which is why two accounts may not
	// share one.
	Principal string
	Role      auth.Role
	// Plugins is what this account may reach, or the single element
	// auth.Wildcard. Never empty: an empty grant reaches nothing, which is
	// not what leaving the field blank meant.
	Plugins []string

	// RatePerSec bounds calls per second across every tunnel this account
	// owns. Zero is unlimited, and is the default.
	//
	// The traffic runs inward -- ChatGPT calls mcpd -- so this is not a quota
	// owed to OpenAI. It bounds what one account can ask of this host and the
	// systems behind it, so that one workspace's retry loop is not every
	// other workspace's outage.
	RatePerSec float64

	Enabled   bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// accountNamePattern is what a name may contain.
//
// Deliberately narrow, because the name is not only a label: it seeds the
// principal, which lands in the audit trail and in a plugin grant. Letters,
// digits, space, dash and underscore leave a name somebody can read and a
// slug that needs no escaping anywhere it is later written.
var accountNamePattern = regexp.MustCompile(`^[\p{L}\p{N} _-]+$`)

// Validate checks an account before it is stored or connected.
func (a *Account) Validate() error {
	var problems []string

	name := strings.TrimSpace(a.Name)
	switch {
	case name == "":
		problems = append(problems, "a name is required")
	case len(name) > 64:
		problems = append(problems, "the name must be 64 characters or fewer")
	case !accountNamePattern.MatchString(name):
		problems = append(problems,
			"the name may hold letters, digits, spaces, dashes and underscores")
	}
	if strings.TrimSpace(a.APIKey) == "" {
		problems = append(problems, "an OpenAI key is required")
	}
	// An admin key with no organisation cannot list a single tunnel, so it is
	// refused at the point it is typed rather than at the first request that
	// comes back empty and unexplained.
	if strings.TrimSpace(a.AdminKey) != "" && strings.TrimSpace(a.OrgID) == "" {
		problems = append(problems,
			"an admin key needs the organization ID that goes with it")
	}
	if strings.TrimSpace(a.Principal) == "" {
		problems = append(problems, "a principal is required")
	}
	if !a.Role.Valid() {
		problems = append(problems, fmt.Sprintf("unknown role %q", a.Role))
	}
	if a.RatePerSec < 0 {
		problems = append(problems, "the rate limit cannot be negative")
	}
	for _, p := range a.Plugins {
		if strings.TrimSpace(p) == "" {
			problems = append(problems, "a blank entry in the systems list")
			break
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("chatgpt account: %s", strings.Join(problems, "; "))
	}
	return nil
}

// AsPrincipal renders the account as the identity its tunnels carry.
//
// The grant is the account's own. A tunnel narrows it further -- a per-plugin
// tunnel is bound to one system -- and the narrowing happens where the tunnel
// is configured, so that assigning a tunnel to an account can only ever reduce
// what that tunnel reaches.
func (a *Account) AsPrincipal() auth.Principal {
	plugins := a.Plugins
	if len(plugins) == 0 {
		plugins = []string{auth.Wildcard}
	}
	return auth.Principal{
		ID:          a.Principal,
		DisplayName: "chatgpt: " + a.Name,
		Role:        a.Role,
		Plugins:     plugins,
		// The account is the credential, so it is also what a revocation
		// would name. There is no separate token to identify.
		TokenID: a.ID,
	}
}

// PrincipalFor derives the default identity for an account name.
//
// Derived rather than typed, because an operator adding an account should not
// have to invent an identifier whose only job is to be unique -- but stored
// rather than recomputed, so that renaming an account does not silently
// rewrite history the audit trail has already recorded under the old name.
func PrincipalFor(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == ' ', r == '_', r == '-':
			return '-'
		}
		// Anything else -- an accented letter, a symbol the name pattern let
		// through -- is dropped rather than transliterated. A slug is an
		// identifier, not a rendering of the name.
		return -1
	}, strings.TrimSpace(name))

	slug = strings.Trim(collapseDashes(slug), "-")
	if slug == "" {
		// A name of nothing but characters the slug drops. Rare, and the
		// caller still needs an identity; uniqueness is enforced by the store.
		return "svc:chatgpt"
	}
	return "svc:chatgpt:" + slug
}

// collapseDashes turns runs of dashes into one, so "Work  Space" and
// "Work-Space" do not become two different identities.
func collapseDashes(s string) string {
	var b strings.Builder
	var prevDash bool
	for _, r := range s {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// AccountUpdate is a partial edit.
//
// Pointers rather than values, so that "leave this alone" and "set this to
// empty" are different instructions. It matters most for the credentials: the
// dashboard never reads a key back, so an edit that changes only the rate
// limit arrives with no key at all -- and a struct of plain strings would read
// that as an instruction to erase one.
type AccountUpdate struct {
	Name       *string
	APIKey     *string
	AdminKey   *string
	OrgID      *string
	Role       *auth.Role
	Plugins    *[]string
	RatePerSec *float64
	Enabled    *bool
}
