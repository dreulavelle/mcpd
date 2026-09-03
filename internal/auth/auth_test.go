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

func operator() Permissions {
	r, _ := BuiltinRole(RoleOperator)
	return r.Permissions
}

func reads(plugins ...string) Grants { return GrantsAt(plugins, LevelRead) }

// The headline requirement: an agent handed one plugin must not reach another.
func TestReaches_ScopingIsolatesAgents(t *testing.T) {
	tests := []struct {
		name    string
		grants  Grants
		probe   string
		level   Level
		allowed bool
	}{
		{"exact grant", reads("cnmaestro"), "cnmaestro", LevelRead, true},
		{"other plugin denied", reads("cnmaestro"), "proxmox", LevelRead, false},
		{"multiple grants", reads("cnmaestro", "netbox"), "netbox", LevelRead, true},
		{"wildcard grants all", reads(Wildcard), "anything", LevelRead, true},
		{"empty grants deny all", nil, "cnmaestro", LevelRead, false},
		{"empty name denied", reads(Wildcard), "", LevelRead, false},
		{"no partial prefix match", reads("cnmaestro"), "cnmaestro-staging", LevelRead, false},
		{"read does not reach write", reads("cnmaestro"), "cnmaestro", LevelWrite, false},
		{"write includes read", GrantsAt([]string{"cnmaestro"}, LevelWrite), "cnmaestro", LevelRead, true},
		{"a wildcard at read does not write a named plugin",
			Grants{{Wildcard, LevelRead}, {"graylog", LevelWrite}}, "cnmaestro", LevelWrite, false},
		{"and the named plugin is written",
			Grants{{Wildcard, LevelRead}, {"graylog", LevelWrite}}, "graylog", LevelWrite, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{ID: "svc:agent", Permissions: operator(), Grants: tc.grants}
			if got := p.Reaches(tc.probe, tc.level); got != tc.allowed {
				t.Fatalf("Reaches(%q, %s) with grants %v = %v, want %v",
					tc.probe, tc.level, tc.grants, got, tc.allowed)
			}
		})
	}
}

// A principal with no plugins listed must be denied everything. Treating an
// empty list as "all" would turn an incomplete config into a full grant.
func TestValidate_RefusesPrincipalWithNoPluginGrants(t *testing.T) {
	p := &Principal{ID: "svc:agent", Permissions: operator()}
	if err := p.Validate(); err == nil {
		t.Fatal("a principal granting no plugins must be refused at load time")
	}
}

// The three built-in roles are the whole of what a fresh host offers, so
// what each one holds is pinned here rather than left to the table.
func TestBuiltinRoles(t *testing.T) {
	tests := []struct {
		id     string
		can    []Permission
		cannot []Permission
	}{
		{RoleReader,
			[]Permission{PermApprovalsRead, PermSettingsRead, PermHistoryRead, PermSystemRead},
			[]Permission{PermApprovalsDecide, PermSettingsWrite, PermAccessRead, PermAccessWrite}},
		{RoleOperator,
			[]Permission{PermApprovalsRead, PermApprovalsDecide, PermSettingsRead, PermPluginsRead},
			[]Permission{PermSettingsWrite, PermPluginsWrite, PermAccessRead, PermTunnelsWrite}},
		{RoleAdministrator,
			[]Permission{PermApprovalsDecide, PermSettingsWrite, PermAccessWrite, PermHistoryWrite, PermSystemWrite},
			nil},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			r, ok := BuiltinRole(tc.id)
			if !ok {
				t.Fatalf("no built-in role %s", tc.id)
			}
			p := &Principal{ID: "u", Permissions: r.Permissions, Grants: reads(Wildcard)}
			for _, perm := range tc.can {
				if !p.Can(perm) {
					t.Errorf("%s should hold %s", r.Name, perm)
				}
			}
			for _, perm := range tc.cannot {
				if p.Can(perm) {
					t.Errorf("%s must not hold %s", r.Name, perm)
				}
			}
		})
	}
}

// PermissionList has to agree with Can, because the dashboard draws its
// controls from the list and the server refuses from the check.
func TestPermissionList_AgreesWithCan(t *testing.T) {
	cases := []struct {
		name string
		p    *Principal
	}{
		{"nil holds nothing", nil},
		{"operator", &Principal{Permissions: operator()}},
		{"a custom role", &Principal{Permissions: Permissions{AreaTunnels: LevelWrite, AreaHistory: LevelRead}}},
		{"pending holds nothing", &Principal{Permissions: operator(), Pending: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.PermissionList()
			if got == nil {
				t.Fatal("PermissionList() returned nil; an empty list is the answer, not the absence of one")
			}
			for _, perm := range got {
				if !tc.p.Can(perm) {
					t.Errorf("PermissionList() lists %s but Can refuses it", perm)
				}
			}
			for _, a := range Areas {
				for _, l := range a.Levels() {
					perm := Perm(a, l)
					listed := false
					for _, g := range got {
						listed = listed || g == perm
					}
					if tc.p.Can(perm) != listed {
						t.Errorf("Can(%s) = %v but listed = %v", perm, tc.p.Can(perm), listed)
					}
				}
			}
		})
	}
}

func TestAnonymousHoldsNothing(t *testing.T) {
	p := Anonymous()
	for _, a := range Areas {
		if p.Can(Perm(a, LevelRead)) {
			t.Errorf("anonymous must not hold %s", a)
		}
	}
	if p.CanAccessPlugin("cnmaestro") {
		t.Error("anonymous must reach no plugin")
	}
}

func TestStaticVerifier(t *testing.T) {
	v, err := NewStaticVerifier(
		mustToken(t, "agent-a", tokenA, Principal{
			ID: "svc:agent-a", Permissions: operator(), Grants: reads("cnmaestro")}),
		mustToken(t, "agent-b", tokenB, Principal{
			ID: "svc:agent-b", Permissions: operator(), Grants: reads("netbox")}),
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
		ID: "u", Permissions: operator(), Grants: reads(Wildcard)}); err == nil {
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
	p := &Principal{ID: "u", Permissions: operator(), Grants: reads(Wildcard)}
	if got := FromContext(WithPrincipal(context.Background(), p)); got.ID != "u" {
		t.Fatal("principal did not round-trip through context")
	}
}

// --- authorization --------------------------------------------------------

func TestAuthorizeEndpoint(t *testing.T) {
	a := NewAuthorizer()
	scoped := &Principal{ID: "svc:a", Permissions: operator(), Grants: reads("cnmaestro")}

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

// The one translation from a tool's vocabulary into the host's. Each
// capability maps to exactly one requirement, and the table says which.
func TestAuthorizeTool_TranslatesCapabilities(t *testing.T) {
	a := NewAuthorizer()
	reader, _ := BuiltinRole(RoleReader)
	admin, _ := BuiltinRole(RoleAdministrator)
	cases := []struct {
		name    string
		p       *Principal
		cap     Capability
		allowed bool
	}{
		{"read needs the plugin at read", &Principal{Permissions: reader.Permissions, Grants: reads("x")}, CapRead, true},
		{"propose needs write", &Principal{Permissions: operator(), Grants: reads("x")}, CapPropose, false},
		{"propose with write", &Principal{Permissions: operator(), Grants: GrantsAt([]string{"x"}, LevelWrite)}, CapPropose, true},
		{"approve needs decide", &Principal{Permissions: reader.Permissions, Grants: reads("x")}, CapApprove, false},
		{"approve with decide", &Principal{Permissions: operator(), Grants: reads("x")}, CapApprove, true},
		{"admin needs plugins:write", &Principal{Permissions: operator(), Grants: GrantsAt([]string{"x"}, LevelWrite)}, CapAdmin, false},
		{"admin with plugins:write", &Principal{Permissions: admin.Permissions, Grants: reads("x")}, CapAdmin, true},
		{"nothing without reach", &Principal{Permissions: admin.Permissions, Grants: reads("y")}, CapRead, false},
		{"unknown capability refused", &Principal{Permissions: admin.Permissions, Grants: reads("x")}, Capability("sudo"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.ID = "u"
			if d := a.AuthorizeTool(tc.p, "x", tc.cap); d.Allowed != tc.allowed {
				t.Fatalf("AuthorizeTool(%s) = %v (%s), want %v", tc.cap, d.Allowed, d.Reason, tc.allowed)
			}
		})
	}
}

func TestVisiblePlugins_HidesUngrantedPlugins(t *testing.T) {
	a := NewAuthorizer()
	p := &Principal{ID: "svc:a", Permissions: operator(), Grants: reads("cnmaestro")}

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

// A tunnel restarts when its principal changes, so Equal has to see every
// part of what a principal grants and ignore the order it was listed in.
func TestEqual_SeesPermissionsAndGrants(t *testing.T) {
	base := Principal{ID: "user:a", RoleID: RoleOperator, Permissions: operator(),
		Grants: Grants{{"a", LevelRead}, {"b", LevelWrite}}}
	same := base
	same.Grants = Grants{{"b", LevelWrite}, {"a", LevelRead}}
	wider := base
	wider.Grants = Grants{{"a", LevelWrite}, {"b", LevelWrite}}
	stronger := base
	stronger.Permissions = base.Permissions.Merge(Permissions{AreaSettings: LevelWrite})

	if !base.Equal(same) {
		t.Error("the same grants in a different order are equal")
	}
	if base.Equal(wider) {
		t.Error("a grant at a higher level makes the principals differ")
	}
	if base.Equal(stronger) {
		t.Error("a permission more makes the principals differ")
	}
}
