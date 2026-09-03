package auth

import (
	"encoding/json"
	"testing"
)

// Write includes read and decide includes read: the one composition rule,
// and the one every product people trust with credentials shares.
func TestLevel_Includes(t *testing.T) {
	cases := []struct {
		held, need Level
		want       bool
	}{
		{LevelWrite, LevelRead, true},
		{LevelDecide, LevelRead, true},
		{LevelRead, LevelWrite, false},
		{LevelRead, LevelRead, true},
		{LevelNone, LevelRead, false},
		{LevelRead, LevelNone, true},
		{LevelNone, LevelNone, true},
	}
	for _, tc := range cases {
		if got := tc.held.Includes(tc.need); got != tc.want {
			t.Errorf("%s includes %s = %v, want %v", tc.held, tc.need, got, tc.want)
		}
	}
}

// A permission is a closed vocabulary: approvals cannot be held at write and
// nothing can be held at "sudo", so a typo is refused rather than stored as
// a permission nobody holds.
func TestPermission_Split(t *testing.T) {
	for _, ok := range []Permission{PermApprovalsDecide, PermSettingsWrite, PermHistoryRead} {
		if _, _, valid := ok.Split(); !valid {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []Permission{"approvals:write", "settings:decide", "sudo", "settings", "billing:read"} {
		if _, _, valid := bad.Split(); valid {
			t.Errorf("%s should be refused", bad)
		}
	}
	if !PermSignedIn.Valid() {
		t.Error("the signed-in permission is one anybody may be asked for")
	}
}

// Merging takes the higher level in every area: a group can only add.
func TestPermissions_Merge(t *testing.T) {
	own := Permissions{AreaSettings: LevelRead, AreaTunnels: LevelWrite}
	group := Permissions{AreaSettings: LevelWrite, AreaApprovals: LevelDecide}
	got := own.Merge(group)
	want := Permissions{AreaSettings: LevelWrite, AreaTunnels: LevelWrite, AreaApprovals: LevelDecide}
	if !got.Equal(want) {
		t.Fatalf("Merge = %v, want %v", got, want)
	}
	if !own.Equal(Permissions{AreaSettings: LevelRead, AreaTunnels: LevelWrite}) {
		t.Fatal("Merge must not change its receiver")
	}
}

// The stored form is an object of area to level, in display order, so two
// equal sets encode identically and a hand-written row reads back.
func TestPermissions_JSON(t *testing.T) {
	ps := Permissions{AreaSystem: LevelWrite, AreaApprovals: LevelDecide, AreaAccess: LevelNone}
	encoded, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"approvals":"decide","system":"write"}` {
		t.Fatalf("encoded = %s", encoded)
	}
	var back Permissions
	if err := json.Unmarshal([]byte(`{"approvals":"decide","system":"write","billing":"read","settings":"sudo"}`), &back); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(Permissions{AreaSystem: LevelWrite, AreaApprovals: LevelDecide}) {
		t.Fatalf("decoded = %v; unknown areas and levels are dropped", back)
	}
}

// Normalising a grant list keeps the highest level per plugin, lets a
// wildcard absorb what it already covers, and orders the result so two equal
// lists encode identically.
func TestGrants_Normalize(t *testing.T) {
	in := Grants{
		{"graylog", LevelRead}, {"graylog", LevelWrite}, {" ", LevelRead},
		{"observium", LevelRead}, {Wildcard, LevelRead}, {"cnmaestro", Level("sudo")},
	}
	got := in.Normalize()
	want := Grants{{Wildcard, LevelRead}, {"graylog", LevelWrite}}
	if !got.Equal(want) || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Normalize = %v, want %v", got, want)
	}
	if got.LevelFor("observium") != LevelRead || got.LevelFor("graylog") != LevelWrite || got.LevelFor("nothing") != LevelRead {
		t.Fatalf("LevelFor after normalising: %v", got)
	}
	if plugins := got.Plugins(); len(plugins) != 1 || plugins[0] != Wildcard {
		t.Fatalf("Plugins = %v, want [*]", plugins)
	}
}

// A stored value this build cannot parse reads as no grants: a row nobody
// can decode hands out nothing rather than everything.
func TestDecodeGrants_FailsClosed(t *testing.T) {
	if got := DecodeGrants(`not json`); len(got) != 0 {
		t.Fatalf("DecodeGrants(garbage) = %v, want nothing", got)
	}
	if got := DecodeGrants(`[{"plugin":"x","level":"write"}]`); !got.Reaches("x", LevelWrite) {
		t.Fatalf("DecodeGrants round trip = %v", got)
	}
	if EncodeGrants(nil) != "[]" {
		t.Fatalf("EncodeGrants(nil) = %s", EncodeGrants(nil))
	}
}
