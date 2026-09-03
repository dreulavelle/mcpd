package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
)

// A plugin's tool list is paid for on every conversation, before a single tool
// is called. A client fetches tools/list on connect and the whole of it enters
// the model's context: names, descriptions, input schemas, output schemas and
// annotations. Nothing here is charged per call -- it is charged per
// conversation, whether the tools are used or not.
//
// That makes it the one cost in this project nobody notices growing. A
// description gains a sentence, a result type gains a nested struct, and the
// tool list is a kilobyte heavier with every test still passing. So it is
// measured, and it is bounded.
//
// # What the ceilings are
//
// Roughly fifteen percent above what each plugin costs today. They are not
// targets and they are not tight: the point is to catch a doubling, not to
// argue about a sentence. Raising one is fine, and doing it deliberately -- in
// a diff somebody reads -- is the entire mechanism.
//
// # What the measurement showed
//
// Output schemas are the largest single line item at about forty percent of
// the total, and nobody chose them: they are derived from each handler's Go
// return type. A tool that returns three nested collections carries a schema
// three times the size of one that returns a flat list, without anybody
// writing a line of it.
//
// It also contradicted something this codebase had written down in three
// places -- that grouping several endpoints into one tool saves context.
// Measured, it does not. observium serves three *fewer* tools than cnmaestro
// and costs five kilobytes *more*, because its grouped tools return composite
// results whose schemas grow by exactly what the grouping saved in tool
// entries. Cost tracks total surface area, not tool count. Grouping is still
// right -- it is how a model is kept from choosing between nine tools that
// answer one question -- but it should be argued for on those grounds and not
// on a saving that is not there.
var toolListBudget = map[string]int{
	"echo":           12_000,
	"graylog":        19_000,
	"observium":      29_000,
	"cnmaestro":      23_000,
	"extremecloudiq": 26_000,
	// Raised from 42,000 when the plugin grew from 31 tools to 39: port-outs,
	// account entitlements, a composite per-number read, caller-ID name,
	// directory listings, customer service records, notification subscriptions
	// and Insights call events.
	//
	// It is now by a wide margin the most expensive tool list here, and that is
	// a deliberate trade rather than drift. Bandwidth is not one API -- it is
	// voice, messaging, number inventory, porting, 10DLC, E911 and line records,
	// which a telephony operator treats as one system and asks questions
	// across. The additions are not breadth for its own sake: every one of them
	// corresponds to a product this deployment's own account reports as
	// enabled, checked with list_products rather than assumed.
	"bandwidth": 56_000,
	// The cheapest of the seven per tool, at about 1,460 bytes against
	// observium's 1,850, because every listing returns the same flat shape: a
	// row array, a returned count and a truncation note. Output schemas are the
	// largest line item and one shape reused across five tools is paid for once.
	"textable": 12_000,
	// Sixteen read tools over a PBX. Wider per tool than textable because
	// several answers are composite by nature -- one extension carries its
	// handsets, its forwarding profiles and its key layout, and a status
	// report carries the licence, the offline trunks and the stopped services
	// -- and a flat row shape cannot say those without a nested array each.
	"threecx": 28_000,
}

// budgetTotal bounds every plugin at once, which is what the aggregate
// endpoint serves to one credential scoped to everything -- the tunnel's case.
//
// It is a headroom figure over the sum of the per-plugin ceilings rather than
// a target of its own, so it moves when an integration is added or grows: it
// went from 85,000 to 100,000 when extremecloudiq arrived with nine tools, to
// 110,000 when that plugin gained its five diagnostics tools, and to 125,000
// when bandwidth arrived with fourteen. Nobody in practice mounts every
// integration -- a host with two is the ordinary case -- so this bounds the
// worst arrangement rather than the likely one.
//
// Bandwidth is the cheapest of the six per tool, at about 1,100 bytes against
// observium's 1,850, because most of its listings return the same flat shape.
// Output schemas are the largest line item and one shape reused across a dozen
// tools is paid for once.
//
// It is also by some way the largest at 31 tools and 42,000 bytes, and that is
// a deliberate trade rather than an oversight. Bandwidth is not one API: it is
// voice, messaging, number inventory, porting, 10DLC registration and E911,
// which are six products a telephony operator treats as one system and asks
// questions across. Splitting them into six plugins would divide the cost by
// six only for a host that mounted one of them, and nobody mounts one -- the
// question "why is this number not receiving texts" crosses four.
// Raised from 155,000 with the bandwidth expansion above and the arrival of
// textable, and again to 210,000 when threecx arrived with sixteen tools.
// Nobody in practice mounts every integration -- a host with two is the
// ordinary case -- so this bounds the worst arrangement rather than the
// likely one, and it is the aggregate endpoint's cost for one credential scoped
// to everything.
const budgetTotal = 210_000

func TestToolList_StaysWithinItsContextBudget(t *testing.T) {
	h := allPluginsApp(t).Handler()

	var grand int
	for _, plugin := range sortedPlugins() {
		tools := listTools(t, h, plugin)

		var desc, in, out, ann, total int
		rows := make([]string, 0, len(tools))
		for _, tl := range tools {
			raw, err := json.Marshal(tl)
			if err != nil {
				t.Fatalf("%s: %v", plugin, err)
			}
			desc += len(tl.Description)
			in += len(tl.InputSchema)
			out += len(tl.OutputSchema)
			ann += len(tl.Annotations)
			total += len(raw)
			rows = append(rows, fmt.Sprintf("    %-40s %6d  (description %5d  input %5d  output %5d)",
				tl.Name, len(raw), len(tl.Description), len(tl.InputSchema), len(tl.OutputSchema)))
		}
		sort.Strings(rows)
		grand += total

		// Logged whether or not it passes. The number being visible in every
		// CI run is half of what this test is for; a threshold nobody sees
		// until it trips teaches nothing about the direction of travel.
		t.Logf("\n%s: %d tools, %d bytes (~%d tokens), budget %d\n"+
			"  descriptions %d | input schemas %d | output schemas %d | annotations %d\n%s",
			plugin, len(tools), total, total/4, toolListBudget[plugin],
			desc, in, out, ann, strings.Join(rows, "\n"))

		if total > toolListBudget[plugin] {
			t.Errorf("%s advertises %d bytes of tools, past its %d budget. "+
				"Either trim it -- a shorter description, a flatter result type -- "+
				"or raise the budget in toolListBudget and say why in the commit",
				plugin, total, toolListBudget[plugin])
		}
	}

	if grand > budgetTotal {
		t.Errorf("every plugin mounted advertises %d bytes (~%d tokens), past "+
			"the %d budget. This is what one credential scoped to everything "+
			"pays on the aggregate endpoint, on every conversation",
			grand, grand/4, budgetTotal)
	}
	t.Logf("all plugins mounted: %d bytes (~%d tokens) before a tool is called",
		grand, grand/4)
}

// advertisedTool is the part of an MCP tool definition a client sends on to
// the model. Decoded rather than measured as raw bytes so the breakdown can
// say which part grew.
type advertisedTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  json.RawMessage `json:"annotations"`
}

func sortedPlugins() []string {
	out := make([]string, 0, len(toolListBudget))
	for name := range toolListBudget {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func listTools(t *testing.T, h http.Handler, plugin string) []advertisedTool {
	t.Helper()
	w := mcpRequest(t, h, "/mcp/"+plugin, tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("%s tools/list: %d %s", plugin, w.Code, w.Body.String())
	}
	// The response is server-sent events; the JSON-RPC envelope is the payload
	// of the one message in it.
	body := w.Body.String()
	i := strings.Index(body, `{"jsonrpc"`)
	if i < 0 {
		t.Fatalf("%s: no JSON-RPC envelope in %s", plugin, body)
	}
	var env struct {
		Result struct {
			Tools []advertisedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body[i:]), &env); err != nil {
		t.Fatalf("%s: %v", plugin, err)
	}
	return env.Result.Tools
}

// allPluginsApp mounts every integration this binary carries.
//
// Settings are supplied because a plugin whose required fields are empty is
// not mounted at all -- it waits to be configured, and an endpoint that does
// not exist advertises nothing. The addresses are unreachable on purpose:
// nothing here calls a tool, and a plugin that cannot reach its upstream still
// advertises exactly the tools it would if it could.
func allPluginsApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MCPD_TOKEN_WILDCARD", tokenWildcard)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = map[string]config.PluginConfig{
		"echo": {Enabled: true},
		"graylog": {Enabled: true, Settings: map[string]any{
			"base_url": "https://graylog.invalid", "token": "t"}},
		"observium": {Enabled: true, Settings: map[string]any{
			"base_url": "https://observium.invalid", "token": "t"}},
		"cnmaestro": {Enabled: true, Settings: map[string]any{
			"base_url":  "https://cnmaestro.invalid",
			"client_id": "i", "client_secret": "s"}},
		"extremecloudiq": {Enabled: true, Settings: map[string]any{
			"base_url": "https://extremecloudiq.invalid", "api_token": "t"}},
		"bandwidth": {Enabled: true, Settings: map[string]any{
			"client_id": "i", "client_secret": "s"}},
		"textable": {Enabled: true, Settings: map[string]any{
			"base_url": "https://textable.invalid", "api_key": "svc-token"}},
		"threecx": {Enabled: true, Settings: map[string]any{
			"host": "pbx.invalid", "extension": "100", "password": "p"}},
	}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "wildcard", SecretRef: "env:MCPD_TOKEN_WILDCARD",
		Principal: "svc:wildcard", Role: "admin", Plugins: []string{"*"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// The MCP specification splits failures in two, and the split is the whole of
// whether a model can recover from one.
//
// A *protocol* error -- unknown tool, unparseable arguments -- is a JSON-RPC
// error. The call did not happen and there is nothing to reason about.
//
// A *tool execution* error -- a bad query, an upstream that refused -- is an
// ordinary result carrying `isError: true` and the message as text. The model
// sees it, and because this project's tool errors say what to do instead, it
// can fix the call and try again rather than reporting a failure to the user.
//
// Getting this backwards is invisible until it matters: a handler error
// returned as a protocol error is swallowed by some clients and surfaced as a
// transport failure by others, and in neither case does the model get the
// sentence that would have let it correct itself.
func TestToolErrors_ReachTheModelAsRecoverableResults(t *testing.T) {
	h := allPluginsApp(t).Handler()

	// A handler that refuses its arguments.
	bad := mcpRequest(t, h, "/mcp/graylog", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "graylog_search_messages",
			"arguments": map[string]any{"sort_order": "sideways"},
		},
	})
	body := bad.Body.String()
	if strings.Contains(body, `"error"`) {
		t.Errorf("a handler error came back as a protocol error, so the model "+
			"cannot see it or act on it:\n%s", body)
	}
	if !strings.Contains(body, `"isError":true`) {
		t.Errorf("a handler error was not marked isError:\n%s", body)
	}
	// And it must carry the sentence that says what to do instead. An error
	// the model can see but not act on is only half of this.
	if !strings.Contains(body, "asc or desc") {
		t.Errorf("the error did not reach the model with its guidance:\n%s", body)
	}

	// A tool that does not exist is the other kind: the call never happened.
	missing := mcpRequest(t, h, "/mcp/graylog", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "graylog_nope", "arguments": map[string]any{}},
	})
	if !strings.Contains(missing.Body.String(), `"error"`) {
		t.Errorf("an unknown tool should be a protocol error:\n%s", missing.Body.String())
	}
}

// Every result goes over the wire twice: once as structuredContent, once
// serialized into a text block, which the specification asks for so that a
// client predating structured content can still recover the payload.
//
// Pinned because it is the multiplier on every size decision this project
// makes. A budget written as though a result is sent once is half the budget
// somebody thought they were setting -- see plugins.MaxResultBytes.
func TestToolResults_AreSentTwice(t *testing.T) {
	h := allPluginsApp(t).Handler()

	w := mcpRequest(t, h, "/mcp/echo", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "echo_get_echo", "arguments": map[string]any{"message": "hello"},
		},
	})
	body := w.Body.String()
	if !strings.Contains(body, `"structuredContent"`) {
		t.Fatalf("no structured content:\n%s", body)
	}
	if strings.Count(body, "hello") < 2 {
		t.Errorf("the payload was not duplicated into a text block; if the SDK "+
			"has stopped doing this, plugins.MaxResultBytes is now half of what "+
			"it could be:\n%s", body)
	}
}
