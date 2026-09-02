package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"testing"
)

func mustToken(t *testing.T, id, plaintext string, p Principal) *StaticToken {
	t.Helper()
	tok, err := NewStaticToken(id, plaintext, p)
	if err != nil {
		t.Fatalf("NewStaticToken(%s): %v", id, err)
	}
	return tok
}

const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// The headline requirement: an agent handed one plugin must not reach another.
func TestCanAccessPlugin_ScopingIsolatesAgents(t *testing.T) {
	tests := []struct {
		name    string
		grants  []string
		probe   string
		allowed bool
	}{
		{"exact grant", []string{"cnmaestro"}, "cnmaestro", true},
		{"other plugin denied", []string{"cnmaestro"}, "proxmox", false},
		{"multiple grants", []string{"cnmaestro", "netbox"}, "netbox", true},
		{"wildcard grants all", []string{Wildcard}, "anything", true},
		{"empty grants deny all", nil, "cnmaestro", false},
		{"empty name denied", []string{Wildcard}, "", false},
		{"no partial prefix match", []string{"cnmaestro"}, "cnmaestro-staging", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{ID: "svc:agent", Role: RoleUser, Plugins: tc.grants}
			if got := p.CanAccessPlugin(tc.probe); got != tc.allowed {
				t.Fatalf("CanAccessPlugin(%q) with grants %v = %v, want %v",
					tc.probe, tc.grants, got, tc.allowed)
			}
		})
	}
}

// A principal with no plugins listed must be denied everything. Treating an
// empty list as "all" would turn an incomplete config into a full grant.
func TestValidate_RefusesPrincipalWithNoPluginGrants(t *testing.T) {
	p := &Principal{ID: "svc:agent", Role: RoleUser}
	if err := p.Validate(); err == nil {
		t.Fatal("a principal granting no plugins must be refused at load time")
	}
}

func TestRoleCapabilities(t *testing.T) {
	tests := []struct {
		role   Role
		can    []Capability
		cannot []Capability
	}{
		// The whole of the model: the line is administering the host, not
		// operating it.
		{RoleUser, []Capability{CapRead, CapPropose, CapApprove}, []Capability{CapAdmin}},
		{RoleAdmin, []Capability{CapRead, CapPropose, CapApprove, CapAdmin}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.role.String(), func(t *testing.T) {
			p := &Principal{ID: "u", Role: tc.role, Plugins: []string{Wildcard}}
			for _, c := range tc.can {
				if !p.Can(c) {
					t.Errorf("%s should hold %s", tc.role, c)
				}
			}
			for _, c := range tc.cannot {
				if p.Can(c) {
					t.Errorf("%s must not hold %s", tc.role, c)
				}
			}
		})
	}
}

// Capabilities has to agree with Can, because the dashboard draws its
// controls from the list and the server refuses from the check. A group
// ceiling is the case that used to split them: the role said approve, the
// ceiling said no, and the page showed a button that always failed.
func TestCapabilities_AgreesWithCan(t *testing.T) {
	cases := []struct {
		name string
		p    *Principal
		want []Capability
	}{
		{"nil holds nothing", nil, []Capability{}},
		{"admin without a ceiling", &Principal{Role: RoleAdmin},
			[]Capability{CapRead, CapPropose, CapApprove, CapAdmin}},
		{"user without a ceiling", &Principal{Role: RoleUser},
			[]Capability{CapRead, CapPropose, CapApprove}},
		{"a ceiling only removes", &Principal{Role: RoleUser, Ceiling: []Capability{CapRead, CapAdmin}},
			[]Capability{CapRead}},
		{"an empty ceiling suspends", &Principal{Role: RoleAdmin, Ceiling: []Capability{}},
			[]Capability{}},
		{"pending holds nothing", &Principal{Role: RoleAdmin, Pending: true},
			[]Capability{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.Capabilities()
			if got == nil {
				t.Fatal("Capabilities() returned nil; an empty list is the answer, not the absence of one")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Capabilities() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Capabilities() = %v, want %v", got, tc.want)
				}
				if !tc.p.Can(got[i]) {
					t.Errorf("Capabilities() lists %s but Can refuses it", got[i])
				}
			}
		})
	}
}

func TestAnonymousHoldsNothing(t *testing.T) {
	p := Anonymous()
	for _, c := range []Capability{CapRead, CapPropose, CapApprove, CapAdmin} {
		if p.Can(c) {
			t.Errorf("anonymous must not hold %s", c)
		}
	}
	if p.CanAccessPlugin("cnmaestro") {
		t.Error("anonymous must reach no plugin")
	}
}

func TestStaticVerifier(t *testing.T) {
	v, err := NewStaticVerifier(
		mustToken(t, "agent-a", tokenA, Principal{
			ID: "svc:agent-a", Role: RoleUser, Plugins: []string{"cnmaestro"}}),
		mustToken(t, "agent-b", tokenB, Principal{
			ID: "svc:agent-b", Role: RoleUser, Plugins: []string{"netbox"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	p, err := v.Verify(ctx, tokenA, nil)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if p.ID != "svc:agent-a" || !p.CanAccessPlugin("cnmaestro") || p.CanAccessPlugin("netbox") {
		t.Fatalf("wrong principal issued: %+v", p)
	}

	for _, bad := range []string{"", "wrong", tokenA + "x"} {
		if _, err := v.Verify(ctx, bad, nil); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Verify(%q) = %v, want ErrUnauthenticated", bad, err)
		}
	}
}

func TestStaticToken_RejectsWeakSecrets(t *testing.T) {
	if _, err := NewStaticToken("weak", "short", Principal{
		ID: "u", Role: RoleUser, Plugins: []string{Wildcard}}); err == nil {
		t.Fatal("a short token must be refused")
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc123", "abc123", true},
		{"bearer abc123", "abc123", true},
		{"BEARER abc123", "abc123", true},
		{"Bearer   abc123  ", "abc123", true},
		{"Basic abc123", "", false},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		got, ok := BearerToken(r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("BearerToken(%q) = (%q,%v), want (%q,%v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFromContext_DefaultsToAnonymous(t *testing.T) {
	if p := FromContext(context.Background()); p == nil || p.ID != "anonymous" {
		t.Fatal("an unauthenticated context must yield the anonymous principal, never nil")
	}
	p := &Principal{ID: "u", Role: RoleAdmin, Plugins: []string{Wildcard}}
	if got := FromContext(WithPrincipal(context.Background(), p)); got.ID != "u" {
		t.Fatal("principal did not round-trip through context")
	}
}

// --- authorization --------------------------------------------------------

func TestAuthorizeEndpoint(t *testing.T) {
	a := NewAuthorizer()
	scoped := &Principal{ID: "svc:a", Role: RoleUser, Plugins: []string{"cnmaestro"}}

	if d := a.AuthorizeEndpoint(scoped, "cnmaestro"); !d.Allowed {
		t.Fatalf("granted plugin refused: %s", d.Reason)
	}
	if d := a.AuthorizeEndpoint(scoped, "proxmox"); d.Allowed {
		t.Fatal("ungranted plugin must be refused")
	}
	if d := a.AuthorizeEndpoint(Anonymous(), "cnmaestro"); d.Allowed {
		t.Fatal("anonymous must be refused")
	}
}

func TestVisiblePlugins_HidesUngrantedPlugins(t *testing.T) {
	a := NewAuthorizer()
	p := &Principal{ID: "svc:a", Role: RoleUser, Plugins: []string{"cnmaestro"}}

	got := a.VisiblePlugins(p, []string{"cnmaestro", "proxmox", "netbox"})
	if len(got) != 1 || got[0] != "cnmaestro" {
		t.Fatalf("VisiblePlugins = %v, want [cnmaestro]", got)
	}
}

func TestGenerateToken(t *testing.T) {
	tok, err := GenerateToken(rand.Read)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 32 {
		t.Fatalf("generated token is too short: %d chars", len(tok))
	}
	other, _ := GenerateToken(rand.Read)
	if tok == other {
		t.Fatal("generated tokens must not repeat")
	}
}

func TestFingerprint_IsNotReversibleAndIsStable(t *testing.T) {
	a := Fingerprint(tokenA)
	if a == "" || a == tokenA {
		t.Fatal("fingerprint must be derived, not the token itself")
	}
	if a != Fingerprint(tokenA) {
		t.Fatal("fingerprint must be stable")
	}
	if a == Fingerprint(tokenB) {
		t.Fatal("distinct tokens must fingerprint differently")
	}
}

// A ceiling is part of what a principal grants, so two principals that differ
// only in one are not equal. A tunnel restarts when its principal changes;
// one whose ceiling changed and whose Equal said nothing had would keep
// serving with rights that had been taken away.
func TestEqual_SeesTheCeiling(t *testing.T) {
	base := Principal{ID: "user:a", Role: RoleAdmin, Plugins: []string{"*"}}
	none := base
	empty := base
	empty.Ceiling = []Capability{}
	read := base
	read.Ceiling = []Capability{CapRead}
	readAgain := base
	readAgain.Ceiling = []Capability{CapRead}

	if !none.Equal(base) || !read.Equal(readAgain) {
		t.Error("principals with the same ceiling are equal")
	}
	if none.Equal(empty) {
		t.Error("no ceiling and a ceiling permitting nothing are different grants")
	}
	if none.Equal(read) || empty.Equal(read) {
		t.Error("a ceiling that differs makes the principals differ")
	}
}
