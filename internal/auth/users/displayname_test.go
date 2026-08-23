package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

func TestValidateDisplayName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    string
		refused bool
		why     string
	}{
		{name: "ordinary", in: "Alice Example", want: "Alice Example"},
		{name: "trimmed", in: "  Alice  ", want: "Alice"},
		{name: "empty clears it", in: "", want: ""},
		{name: "whitespace only clears it", in: "   ", want: ""},
		{
			name: "a newline breaks a log line in two",
			in:   "Alice\nBob", refused: true,
			why: "control characters",
		},
		{
			name: "a tab is a control character too",
			in:   "Alice\tBob", refused: true,
		},
		{
			name: "a bidirectional override renders as something else",
			in:   "Alice‮Bob", refused: true,
			why: "invisible formatting",
		},
		{
			name: "a zero-width joiner is invisible",
			in:   "Ali‍ce", refused: true,
		},
		{
			name: "bounded",
			in:   strings.Repeat("a", MaxDisplayNameRunes+1), refused: true,
			why: "at most",
		},
		{
			name: "malformed utf-8 is refused rather than stored as replacement characters",
			in:   "Ali\xffce", refused: true,
			why: "valid UTF-8",
		},
		{
			name: "the bound is in runes, not bytes",
			in:   strings.Repeat("é", MaxDisplayNameRunes),
			want: strings.Repeat("é", MaxDisplayNameRunes),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateDisplayName(tc.in)
			if tc.refused {
				if err == nil {
					t.Fatalf("ValidateDisplayName(%q) = %q, want a refusal", tc.in, got)
				}
				if tc.why != "" && !strings.Contains(err.Error(), tc.why) {
					t.Errorf("error = %q, want it to mention %q", err, tc.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateDisplayName(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// An account with no display name still has to appear in a heading and beside
// every operation it requested. An empty string there is worse than the
// address, which at least names a real person.
func TestName_FallsBackToTheAddress(t *testing.T) {
	u := &User{Email: "alice@example.com"}
	if got := u.Name(); got != "alice@example.com" {
		t.Errorf("Name() = %q, want the address", got)
	}
	u.DisplayName = "Alice"
	if got := u.Name(); got != "Alice" {
		t.Errorf("Name() = %q, want the display name", got)
	}
	if got := u.Principal("ses_1").DisplayName; got != "Alice" {
		t.Errorf("principal DisplayName = %q", got)
	}
	// And the identity is never the name.
	if got := u.Principal("ses_1").ID; got != "user:alice@example.com" {
		t.Errorf("principal ID = %q, want it built from the address", got)
	}
}

// A name is for reading. Letting one read as somebody else's address is how a
// list of who did what stops being one.
func TestUpdate_RefusesAnotherAccountsAddress(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)
	mustCreate(t, store, "bob@example.com", auth.RoleUser)

	name := "bob@example.com"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &name}); !errors.Is(err, ErrNameCollides) {
		t.Fatalf("err = %v, want ErrNameCollides", err)
	}

	// Case does not get around it: addresses are stored lowercase and the
	// guard lowercases what it is given.
	shouty := "BOB@Example.com"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &shouty}); !errors.Is(err, ErrNameCollides) {
		t.Fatalf("err = %v, want ErrNameCollides for a differently-cased address", err)
	}

	// Her own address is hers to use.
	own := "alice@example.com"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &own}); err != nil {
		t.Fatalf("an account may name itself after its own address: %v", err)
	}
}

// The mirror of the rule above: an address must not already be somebody's
// display name, or creating the account would produce two rows that read the
// same.
func TestCreate_RefusesAnAddressSomebodyIsAlreadyNamedAfter(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	name := "bob@example.com"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &name}); err != nil {
		t.Fatalf("update: %v", err)
	}

	_, err := store.Create(ctx, CreateRequest{
		Email: "bob@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser, Plugins: []string{auth.Wildcard},
	})
	if !errors.Is(err, ErrNameCollides) {
		t.Fatalf("err = %v, want ErrNameCollides", err)
	}
}

func TestUpdate_ValidatesTheName(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	bad := "Alice\nBob"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &bad}); err == nil {
		t.Fatal("a name with a control character must be refused")
	}
	// And nothing was written.
	after, err := store.ByID(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != "" {
		t.Errorf("display name = %q, want it unchanged", after.DisplayName)
	}
}

// Renaming yourself is not a privilege change, so it must not end the session
// doing it. Losing your session every time you fix a typo in your own name is
// the kind of thing that makes people stop using a feature.
func TestUpdate_ANameChangeKeepsSessionsAlive(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleUser)

	token, _, err := store.NewSession(ctx, alice.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	name := "Alice"
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveSession(ctx, token); err != nil {
		t.Fatalf("the session was ended by a rename: %v", err)
	}

	// A role change is a privilege change and still does end them.
	role := auth.RoleAdmin
	if _, err := store.Update(ctx, alice.ID, UpdateRequest{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveSession(ctx, token); err == nil {
		t.Error("a role change must still end live sessions")
	}
}

// The bound is in the schema as well as in Go, so a value typed at a sqlite3
// prompt cannot become a value this host renders.
func TestDisplayName_TheDatabaseEnforcesTheBound(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	_, err := store.db.Writer().ExecContext(ctx,
		`UPDATE users SET display_name = ? WHERE id = ?`,
		strings.Repeat("a", MaxDisplayNameRunes+1), alice.ID)
	if err == nil {
		t.Fatal("the database must refuse a display name past the bound")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("err = %v, want a CHECK constraint failure", err)
	}
}

// The other direction of the same rule, at creation: a new account must not be
// named after an address that already belongs to somebody.
func TestCreate_RefusesANameThatIsAnotherAccountsAddress(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	_, err := store.Create(ctx, CreateRequest{
		Email: "bob@example.com", Password: "a-sufficiently-long-passphrase",
		DisplayName: "alice@example.com",
		Role:        auth.RoleUser, Plugins: []string{auth.Wildcard},
	})
	if !errors.Is(err, ErrNameCollides) {
		t.Fatalf("err = %v, want ErrNameCollides", err)
	}

	// And an ordinary name is still fine.
	if _, err := store.Create(ctx, CreateRequest{
		Email: "bob@example.com", Password: "a-sufficiently-long-passphrase",
		DisplayName: "Bob", Role: auth.RoleUser, Plugins: []string{auth.Wildcard},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
}

// A row written before there were rules must not render what the rules exist
// to prevent.
//
// 0011's retroactive normalisation covers the length, because that is the one
// rule a CHECK can express and the rebuild would have failed without it. The
// character rules are not in the schema and cannot be: enumerating the format
// characters in SQL would cover a score of the hundred and seventy in the
// category, and would drift from the Go rule the first time either changed.
// So the same function that refuses them on the way in refuses them on the
// way out, and every render goes through Name().
func TestName_FallsBackForALegacyValueTheRulesRefuse(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	for _, tc := range []struct {
		name     string
		stored   string
		rendered string
	}{
		{name: "a bidirectional override", stored: "Alice‮Bob"},
		{name: "a zero-width space", stored: "Al​ice"},
		{name: "a control character", stored: "Alice\x01"},
		{name: "a byte that is not UTF-8", stored: "Ali\xffce"},
		{
			// At the bound rather than past it, so the fallback cannot pass
			// by refusing everything it is handed.
			name:     "an ordinary name is still rendered",
			stored:   strings.Repeat("a", MaxDisplayNameRunes),
			rendered: strings.Repeat("a", MaxDisplayNameRunes),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.rendered
			if want == "" {
				want = "alice@example.com"
			}
			// Written past this package, the way a value predating the rules
			// got there. The schema bounds the length and nothing else, so
			// every one of these is a row a real database can hold.
			if _, err := store.db.Writer().ExecContext(ctx,
				`UPDATE users SET display_name = ? WHERE id = ?`, tc.stored, alice.ID); err != nil {
				t.Fatalf("seed a legacy row: %v", err)
			}
			u, err := store.ByID(ctx, alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Name(); got != want {
				t.Errorf("Name() = %q, want %q", got, want)
			}
			// The principal a page and a log line read is built from Name().
			if got := u.Principal("ses_1").DisplayName; got != want {
				t.Errorf("principal DisplayName = %q, want %q", got, want)
			}
			// The stored value is left alone, so its owner can see what is
			// there and replace it.
			if u.DisplayName != tc.stored {
				t.Errorf("the stored value was rewritten to %q", u.DisplayName)
			}
		})
	}
}

// The write path still refuses every value Name falls back from, so nothing
// can put one back through this package.
func TestUpdate_StillRefusesTheValuesNameFallsBackFrom(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	alice := mustCreate(t, store, "alice@example.com", auth.RoleAdmin)

	for _, bad := range []string{
		"Alice‮Bob", "Al​ice", "Alice\x01", "Ali\xffce",
		strings.Repeat("a", MaxDisplayNameRunes+1),
	} {
		if _, err := store.Update(ctx, alice.ID, UpdateRequest{DisplayName: &bad}); err == nil {
			t.Errorf("Update accepted %q", bad)
		}
	}
}
