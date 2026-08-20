package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/spoked/mcpd/internal/operations"
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
			p := &Principal{ID: "svc:agent", Role: RoleOperator, Plugins: tc.grants}
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
	p := &Principal{ID: "svc:agent", Role: RoleOperator}
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
		{RoleViewer, []Capability{CapRead}, []Capability{CapPropose, CapApprove, CapAdmin}},
		{RoleOperator, []Capability{CapRead, CapPropose}, []Capability{CapApprove, CapAdmin}},
		{RoleApprover, []Capability{CapRead, CapPropose, CapApprove}, []Capability{CapAdmin}},
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
			ID: "svc:agent-a", Role: RoleOperator, Plugins: []string{"cnmaestro"}}),
		mustToken(t, "agent-b", tokenB, Principal{
			ID: "svc:agent-b", Role: RoleViewer, Plugins: []string{"netbox"}}),
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

// A static token is shared by definition, so it cannot support separation of
// duties. The verifier must mark it as such regardless of what the caller asks
// for.
func TestStaticToken_IsNeverDistinguishable(t *testing.T) {
	tok := mustToken(t, "shared", tokenA, Principal{
		ID: "svc:shared", Role: RoleApprover,
		Plugins: []string{Wildcard}, Distinguishable: true, // caller asks for true
	})
	if tok.Principal.Distinguishable {
		t.Fatal("a static token must never be marked distinguishable")
	}
}

func TestStaticToken_RejectsWeakSecrets(t *testing.T) {
	if _, err := NewStaticToken("weak", "short", Principal{
		ID: "u", Role: RoleViewer, Plugins: []string{Wildcard}}); err == nil {
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
	a := NewAuthorizer(RiskPolicy{})
	scoped := &Principal{ID: "svc:a", Role: RoleOperator, Plugins: []string{"cnmaestro"}}

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

func TestAuthorizeApproval_SeparationOfDuties(t *testing.T) {
	a := NewAuthorizer(RiskPolicy{
		RequireDistinctApproverAtOrAbove: operations.RiskHigh,
	})
	op := func(risk operations.RiskLevel) *operations.Operation {
		return &operations.Operation{
			Plugin: "cnmaestro", Risk: risk, RequestedBy: "user:alice"}
	}
	alice := &Principal{ID: "user:alice", Role: RoleApprover,
		Plugins: []string{"cnmaestro"}, Distinguishable: true}
	bob := &Principal{ID: "user:bob", Role: RoleApprover,
		Plugins: []string{"cnmaestro"}, Distinguishable: true}
	shared := &Principal{ID: "svc:shared", Role: RoleApprover,
		Plugins: []string{"cnmaestro"}, Distinguishable: false, TokenID: "static"}

	tests := []struct {
		name    string
		p       *Principal
		op      *operations.Operation
		allowed bool
		code    string
	}{
		{"self-approval below threshold is fine", alice, op(operations.RiskMedium), true, ""},
		{"self-approval at threshold refused", alice, op(operations.RiskHigh), false, operations.CodeSelfApproval},
		{"self-approval above threshold refused", alice, op(operations.RiskCritical), false, operations.CodeSelfApproval},
		{"distinct approver permitted", bob, op(operations.RiskCritical), true, ""},
		{"indistinct identity fails closed", shared, op(operations.RiskHigh), false, operations.CodeIdentityIndistinct},
		{"indistinct identity fine below threshold", shared, op(operations.RiskLow), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := a.AuthorizeApproval(tc.p, tc.op)
			if d.Allowed != tc.allowed {
				t.Fatalf("Allowed = %v, want %v (reason: %s)", d.Allowed, tc.allowed, d.Reason)
			}
			if !tc.allowed && d.Code != tc.code {
				t.Fatalf("code = %s, want %s", d.Code, tc.code)
			}
		})
	}
}

func TestAuthorizeApproval_RequiresApproveCapability(t *testing.T) {
	a := NewAuthorizer(RiskPolicy{})
	operator := &Principal{ID: "user:o", Role: RoleOperator,
		Plugins: []string{"cnmaestro"}, Distinguishable: true}
	op := &operations.Operation{Plugin: "cnmaestro", Risk: operations.RiskLow, RequestedBy: "user:x"}

	if d := a.AuthorizeApproval(operator, op); d.Allowed {
		t.Fatal("an operator must not be able to approve")
	}
}

func TestVisiblePlugins_HidesUngrantedPlugins(t *testing.T) {
	a := NewAuthorizer(RiskPolicy{})
	p := &Principal{ID: "svc:a", Role: RoleOperator, Plugins: []string{"cnmaestro"}}

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
