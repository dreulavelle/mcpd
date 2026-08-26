package graylog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// registerSystemTools adds the three tools that describe the installation
// rather than what is in it: what streams exist, what fields can be queried,
// and whether Graylog itself is well.
func (p *Plugin) registerSystemTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_streams",
		Title: "List streams",
		Description: "Lists streams: the named subsets Graylog routes messages " +
			"into, and the unit its permissions are granted on. Start here " +
			"before searching -- naming stream_ids on a search is what keeps " +
			"it from scanning every index, and it is also how a stream id in " +
			"an alert rule turns into a name somebody recognises.",
		Idempotent: true,
	}, p.listStreams)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_message_fields",
		Title: "List queryable message fields",
		Description: "The message fields this Graylog knows about, with their " +
			"types. Use it before writing a query against anything but " +
			"timestamp, source and message: a query naming a field that does " +
			"not exist matches nothing and reports no error, which reads " +
			"exactly like an all-clear. Narrow with `contains` or by stream -- " +
			"a busy installation has thousands.",
		Idempotent: true,
	}, p.listMessageFields)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_system_status",
		Title: "Get Graylog's own status",
		Description: "How the installation itself is: version and lifecycle, " +
			"the search backend's cluster health, Graylog's own outstanding " +
			"notifications, and on request the nodes, the inputs messages " +
			"arrive on, and the index sets they are written to.\n\n" +
			"Reach for it when a search returns less than somebody expected. " +
			"A yellow or red backend, a node that stopped processing or an " +
			"input that is not running all produce the same symptom -- missing " +
			"messages -- and none of them look like an error from a search.",
		Idempotent: true,
	}, p.getSystemStatus)
}

// --- streams ---------------------------------------------------------------

type streamsArgs struct {
	Query string `json:"query,omitempty" jsonschema:"narrows by title or description"`
	Limit int    `json:"limit,omitempty" jsonschema:"most streams to return"`
	Page  int    `json:"page,omitempty" jsonschema:"1-based page"`
}

type streamDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
	IsDefault   bool   `json:"is_default"`
	IndexSetID  string `json:"index_set_id"`
	Rules       []any  `json:"rules"`
}

type streamRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Enabled rather than Disabled: a listing read for "which of these can I
	// search" should not need the reader to invert every value.
	Enabled bool `json:"enabled"`
	Default bool `json:"is_default,omitempty"`
	// Rules is a count. Which rules route into a stream is a question about
	// the routing rather than about the stream, and it is the count that says
	// whether a stream nobody is seeing messages in has any rules at all.
	Rules      int    `json:"routing_rules"`
	IndexSetID string `json:"index_set_id,omitempty"`
}

type streamsResult struct {
	Streams   []streamRow `json:"streams"`
	Returned  int         `json:"returned"`
	Matching  int         `json:"total_matching"`
	Truncated bool        `json:"truncated,omitempty"`
	Note      string      `json:"note,omitempty"`
}

func (p *Plugin) listStreams(ctx context.Context, in streamsArgs) (streamsResult, error) {
	if err := p.ready(); err != nil {
		return streamsResult{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}
	page := in.Page
	if page <= 0 {
		page = 1
	}

	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("per_page", strconv.Itoa(limit))
	if q := strings.TrimSpace(in.Query); q != "" {
		params.Set("query", q)
	}

	raw, err := p.client.Get(ctx, "/streams/paginated", params)
	p.note(err)
	if err != nil {
		return streamsResult{}, err
	}

	items, total, err := pickList(raw, "streams", "elements", "entities")
	if err != nil {
		return streamsResult{}, fmt.Errorf("graylog: the stream listing %w", err)
	}
	var decoded []streamDTO
	if err := json.Unmarshal(items, &decoded); err != nil {
		return streamsResult{}, fmt.Errorf("graylog: decoding streams: %w", err)
	}

	out := streamsResult{Matching: total, Streams: make([]streamRow, 0, len(decoded))}
	var off int
	for _, s := range decoded {
		if len(out.Streams) >= limit {
			out.Truncated = true
			break
		}
		if s.Disabled {
			off++
		}
		out.Streams = append(out.Streams, streamRow{
			ID:          s.ID,
			Title:       s.Title,
			Description: s.Description,
			Enabled:     !s.Disabled,
			Default:     s.IsDefault,
			Rules:       len(s.Rules),
			IndexSetID:  s.IndexSetID,
		})
	}
	out.Returned = len(out.Streams)
	if total > page*limit {
		out.Truncated = true
	}
	if off > 0 {
		out.Note = fmt.Sprintf("%d of these streams are stopped, so no new "+
			"messages are being routed into them. Searching one still returns "+
			"what it holds from before it was stopped.", off)
	}
	return out, nil
}

// --- fields ----------------------------------------------------------------

type fieldsArgs struct {
	Contains string   `json:"contains,omitempty" jsonschema:"only fields whose name contains this, case-insensitive"`
	Streams  []string `json:"stream_ids,omitempty" jsonschema:"only fields present in these streams; the way to cut a long list down to the ones that matter"`
	Limit    int      `json:"limit,omitempty" jsonschema:"most field names to return"`
}

type fieldDTO struct {
	Name string `json:"name"`
	Type struct {
		Type string `json:"type"`
	} `json:"type"`
}

type fieldRow struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type fieldsResult struct {
	Fields    []fieldRow `json:"fields"`
	Returned  int        `json:"returned"`
	Matching  int        `json:"total_matching"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func (p *Plugin) listMessageFields(ctx context.Context, in fieldsArgs) (fieldsResult, error) {
	if err := p.ready(); err != nil {
		return fieldsResult{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}

	// Two endpoints for one question: the GET is every field in the system and
	// the POST is the same question narrowed to a set of streams. Narrowing is
	// worth the second code path -- on an installation with a hundred
	// applications writing structured logs, "every field" is thousands of
	// names and none of the ones somebody wants stand out.
	var (
		raw json.RawMessage
		err error
	)
	if ids := cleanIDs(in.Streams); len(ids) > 0 {
		raw, err = p.client.Post(ctx, "/views/fields", map[string]any{"streams": ids})
	} else {
		raw, err = p.client.Get(ctx, "/views/fields", nil)
	}
	p.note(err)
	if err != nil {
		return fieldsResult{}, err
	}

	items, _, err := pickList(raw, "fields")
	if err != nil {
		return fieldsResult{}, fmt.Errorf("graylog: the field listing %w", err)
	}
	var decoded []fieldDTO
	if err := json.Unmarshal(items, &decoded); err != nil {
		return fieldsResult{}, fmt.Errorf("graylog: decoding field types: %w", err)
	}

	want := strings.ToLower(strings.TrimSpace(in.Contains))
	rows := make([]fieldRow, 0, len(decoded))
	for _, f := range decoded {
		if want != "" && !strings.Contains(strings.ToLower(f.Name), want) {
			continue
		}
		rows = append(rows, fieldRow{Name: f.Name, Type: f.Type.Type})
	}
	// Sorted by name. The API answers with a set, which has no order, so
	// without this the same call returns the same fields in a different
	// sequence each time and a model comparing two answers sees changes that
	// did not happen.
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	out := fieldsResult{Matching: len(rows)}
	if len(rows) > limit {
		rows, out.Truncated = rows[:limit], true
	}
	out.Fields, out.Returned = rows, len(rows)

	if out.Truncated {
		out.Note = "Cut short. Narrow with `contains`, or pass stream_ids to " +
			"see only the fields those streams actually carry."
	}
	if out.Returned == 0 && want != "" {
		out.Note = fmt.Sprintf("No field name contains %q. A query naming a "+
			"field that does not exist matches nothing without saying so, so "+
			"check the spelling before reading an empty search as an "+
			"all-clear.", in.Contains)
	}
	return out, nil
}

// --- system ----------------------------------------------------------------

// The sections graylog_get_system_status can report. Named constants because they are
// both what a caller passes and what the code branches on, and a listing that
// disagreed with the branch would be a section nobody could ask for.
const (
	sectionHealth        = "health"
	sectionNotifications = "notifications"
	sectionNodes         = "nodes"
	sectionInputs        = "inputs"
	sectionIndexSets     = "index_sets"
)

// defaultSections is what "is Graylog well" means without further instruction.
// The two that answer it and nothing else: three requests rather than six.
var defaultSections = []string{sectionHealth, sectionNotifications}

type systemArgs struct {
	Include []string `json:"include,omitempty" jsonschema:"sections to fetch: health, notifications, nodes, inputs, index_sets; default health and notifications"`
}

type systemResult struct {
	Server        *serverSummary `json:"server,omitempty"`
	Backend       *backendHealth `json:"search_backend,omitempty"`
	Notifications []notification `json:"notifications,omitempty"`
	Nodes         []nodeRow      `json:"nodes,omitempty"`
	Inputs        []inputRow     `json:"inputs,omitempty"`
	IndexSets     []indexSetRow  `json:"index_sets,omitempty"`
	Note          string         `json:"note,omitempty"`
}

type serverSummary struct {
	Version    string `json:"version"`
	Hostname   string `json:"hostname,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	ClusterID  string `json:"cluster_id,omitempty"`
	Lifecycle  string `json:"lifecycle,omitempty"`
	LBStatus   string `json:"load_balancer_status,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	Processing bool   `json:"is_processing"`
}

// backendHealth is the search backend's cluster health -- OpenSearch or
// Elasticsearch, depending on the deployment.
//
// Named "search backend" rather than by product because which one is behind a
// given installation is not something this integration knows or needs to, and
// a field called "elasticsearch" on an OpenSearch cluster is a small lie that
// costs somebody five minutes.
type backendHealth struct {
	Status string `json:"status"`
	Shards struct {
		Active       int `json:"active"`
		Initializing int `json:"initializing"`
		Relocating   int `json:"relocating"`
		Unassigned   int `json:"unassigned"`
	} `json:"shards"`
	Means string `json:"means,omitempty"`
}

type notification struct {
	Severity  string         `json:"severity"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp,omitempty"`
	NodeID    string         `json:"node_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type nodeRow struct {
	NodeID    string `json:"node_id"`
	ShortID   string `json:"short_node_id,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Type      string `json:"type,omitempty"`
	IsLeader  bool   `json:"is_leader"`
	LastSeen  string `json:"last_seen,omitempty"`
	Transport string `json:"transport_address,omitempty"`
}

type inputRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
	// Global says the input runs on every node rather than one, which is the
	// difference between "messages are arriving" and "messages are arriving at
	// one of five nodes".
	Global bool   `json:"global"`
	NodeID string `json:"node_id,omitempty"`
	// Settings is a deliberately short allow-list -- see safeInputSettings.
	Settings map[string]any `json:"settings,omitempty"`
}

type indexSetRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Prefix      string `json:"index_prefix,omitempty"`
	Default     bool   `json:"is_default,omitempty"`
	Writable    bool   `json:"writable"`
	Rotation    string `json:"rotation,omitempty"`
	Retention   string `json:"retention,omitempty"`
}

func (p *Plugin) getSystemStatus(ctx context.Context, in systemArgs) (systemResult, error) {
	if err := p.ready(); err != nil {
		return systemResult{}, err
	}

	wanted, err := sections(in.Include)
	if err != nil {
		return systemResult{}, err
	}

	var out systemResult
	// Sequential rather than concurrent, deliberately. The rate limiter would
	// serialise them anyway, and six goroutines that each need their error
	// folded into one result is a lot of machinery to save nothing.
	if wanted[sectionHealth] {
		if err := p.readHealth(ctx, &out); err != nil {
			return systemResult{}, err
		}
	}
	if wanted[sectionNotifications] {
		if err := p.readNotifications(ctx, &out); err != nil {
			return systemResult{}, err
		}
	}
	if wanted[sectionNodes] {
		if err := p.readNodes(ctx, &out); err != nil {
			return systemResult{}, err
		}
	}
	if wanted[sectionInputs] {
		if err := p.readInputs(ctx, &out); err != nil {
			return systemResult{}, err
		}
	}
	if wanted[sectionIndexSets] {
		if err := p.readIndexSets(ctx, &out); err != nil {
			return systemResult{}, err
		}
	}
	return out, nil
}

// sections resolves what to fetch, refusing a name it does not know rather
// than ignoring it. A misspelled section that silently returned the default
// would answer a different question than the one asked, with nothing saying so.
func sections(in []string) (map[string]bool, error) {
	known := map[string]bool{
		sectionHealth: true, sectionNotifications: true, sectionNodes: true,
		sectionInputs: true, sectionIndexSets: true,
	}
	names := cleanIDs(in)
	if len(names) == 0 {
		names = defaultSections
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.ToLower(name)
		if name == "all" {
			for section := range known {
				out[section] = true
			}
			continue
		}
		if !known[name] {
			return nil, fmt.Errorf("graylog: %q is not a section; they are "+
				"health, notifications, nodes, inputs, index_sets, or all", name)
		}
		out[name] = true
	}
	return out, nil
}

func (p *Plugin) readHealth(ctx context.Context, out *systemResult) error {
	info, err := p.client.Probe(ctx)
	p.note(err)
	if err != nil {
		return err
	}
	out.Server = &serverSummary{
		Version:    info.Version,
		Hostname:   info.Hostname,
		NodeID:     info.NodeID,
		ClusterID:  info.ClusterID,
		Lifecycle:  info.Lifecycle,
		LBStatus:   info.LBStatus,
		Timezone:   info.Timezone,
		Processing: info.IsProcessing,
	}
	if !info.IsProcessing {
		out.Note = join(out.Note, "This node is not processing messages, so "+
			"anything arriving at it is not being indexed.")
	}

	raw, err := p.client.Get(ctx, "/system/indexer/cluster/health", nil)
	p.note(err)
	if err != nil {
		return err
	}
	var health backendHealth
	if err := json.Unmarshal(raw, &health); err != nil {
		return fmt.Errorf("graylog: decoding search backend health: %w", err)
	}
	health.Means = healthMeaning(health.Status)
	out.Backend = &health
	return nil
}

// healthMeaning says what a colour costs, because the colours are widely
// recognised and widely misread.
//
// Yellow in particular reads as "degraded, some data unavailable" and is not:
// it is the ordinary steady state of a single-node cluster with replicas
// configured, and treating it as an incident sends somebody looking for a
// fault that is a setting.
func healthMeaning(status string) string {
	switch strings.ToLower(status) {
	case "green":
		return "every shard is assigned; searches cover everything the cluster holds"
	case "yellow":
		return "every primary shard is assigned but some replicas are not. " +
			"Searches are complete; there is no second copy of some data. On a " +
			"single-node cluster this is normal and permanent rather than a fault"
	case "red":
		return "at least one primary shard is unassigned, so part of the data " +
			"cannot be read and searches over it are silently incomplete"
	}
	return ""
}

func (p *Plugin) readNotifications(ctx context.Context, out *systemResult) error {
	raw, err := p.client.Get(ctx, "/system/notifications", nil)
	p.note(err)
	if err != nil {
		return err
	}
	items, _, err := pickList(raw, "notifications")
	if err != nil {
		return fmt.Errorf("graylog: the notification listing %w", err)
	}
	if err := json.Unmarshal(items, &out.Notifications); err != nil {
		return fmt.Errorf("graylog: decoding notifications: %w", err)
	}
	if len(out.Notifications) > 0 {
		out.Note = join(out.Note, fmt.Sprintf("Graylog is raising %d "+
			"notification(s) about itself. These are its own complaints -- "+
			"about journals, indices or nodes -- not alerts about log content.",
			len(out.Notifications)))
	}
	return nil
}

func (p *Plugin) readNodes(ctx context.Context, out *systemResult) error {
	raw, err := p.client.Get(ctx, "/system/cluster/nodes", nil)
	p.note(err)
	if err != nil {
		return err
	}
	items, _, err := pickList(raw, "nodes")
	if err != nil {
		return fmt.Errorf("graylog: the node listing %w", err)
	}
	if err := json.Unmarshal(items, &out.Nodes); err != nil {
		return fmt.Errorf("graylog: decoding nodes: %w", err)
	}
	return nil
}

// safeInputSettings is the allow-list of input attributes this integration
// will return.
//
// An allow-list, and a short one, because the alternative fails. An input's
// attributes are whatever its plugin declares -- a syslog input has a port, an
// AWS input has an access key and a secret, a Beats input has a TLS key
// password -- and the set is open-ended by design, so a deny-list would only
// ever cover the names somebody thought of. Graylog hands the whole map to any
// account that can read inputs, so this is the only place it can be narrowed,
// and a credential in a tool result is a live credential in a model's context
// and from there in whatever the transcript reaches.
//
// What is on it is what somebody asks about an input: where it listens and
// whether it is encrypted. Nothing here is a secret in any input type.
var safeInputSettings = map[string]bool{
	"port": true, "bind_address": true, "tls_enable": true,
	"tls_client_auth": true, "number_worker_threads": true,
	"recv_buffer_size": true, "override_source": true, "charset_name": true,
	"expand_structured_data": true, "allow_override_date": true,
	"store_full_message": true, "force_rdns": true, "topic": true,
	"queue": true, "path": true, "url": true, "timezone": true,
}

func (p *Plugin) readInputs(ctx context.Context, out *systemResult) error {
	raw, err := p.client.Get(ctx, "/system/inputs", nil)
	p.note(err)
	if err != nil {
		return err
	}
	items, _, err := pickList(raw, "inputs")
	if err != nil {
		return fmt.Errorf("graylog: the input listing %w", err)
	}
	var decoded []struct {
		ID         string         `json:"id"`
		Title      string         `json:"title"`
		Type       string         `json:"type"`
		Global     bool           `json:"global"`
		Node       string         `json:"node"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(items, &decoded); err != nil {
		return fmt.Errorf("graylog: decoding inputs: %w", err)
	}

	var withheld bool
	for _, input := range decoded {
		settings := make(map[string]any, len(input.Attributes))
		for name, value := range input.Attributes {
			if !safeInputSettings[name] {
				withheld = true
				continue
			}
			settings[name] = value
		}
		if len(settings) == 0 {
			settings = nil
		}
		out.Inputs = append(out.Inputs, inputRow{
			ID: input.ID, Title: input.Title, Type: input.Type,
			Global: input.Global, NodeID: input.Node, Settings: settings,
		})
	}
	if withheld {
		out.Note = join(out.Note, "An input's configuration can hold "+
			"credentials -- a TLS key password, a cloud access key -- so only "+
			"a short list of settings is returned. The rest is in Graylog's "+
			"own interface.")
	}
	return nil
}

func (p *Plugin) readIndexSets(ctx context.Context, out *systemResult) error {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(p.cfg.MaxItems))
	raw, err := p.client.Get(ctx, "/system/indices/index_sets", params)
	p.note(err)
	if err != nil {
		return err
	}
	items, _, err := pickList(raw, "index_sets")
	if err != nil {
		return fmt.Errorf("graylog: the index set listing %w", err)
	}
	var decoded []struct {
		ID                    string `json:"id"`
		Title                 string `json:"title"`
		Description           string `json:"description"`
		IndexPrefix           string `json:"index_prefix"`
		Default               bool   `json:"default"`
		Writable              bool   `json:"writable"`
		RotationStrategyClass string `json:"rotation_strategy_class"`
		RetentionStrategy     string `json:"retention_strategy_class"`
	}
	if err := json.Unmarshal(items, &decoded); err != nil {
		return fmt.Errorf("graylog: decoding index sets: %w", err)
	}
	for _, set := range decoded {
		out.IndexSets = append(out.IndexSets, indexSetRow{
			ID: set.ID, Title: set.Title, Description: set.Description,
			Prefix: set.IndexPrefix, Default: set.Default, Writable: set.Writable,
			Rotation:  simpleClassName(set.RotationStrategyClass),
			Retention: simpleClassName(set.RetentionStrategy),
		})
	}
	return nil
}

// simpleClassName reduces a Java class name to the part that means something.
//
// "org.graylog2.indexer.rotation.strategies.TimeBasedRotationStrategy" is a
// package path with one word of information in it, and passing the whole thing
// to a model costs a line to say "time based".
func simpleClassName(in string) string {
	if i := strings.LastIndex(in, "."); i >= 0 {
		in = in[i+1:]
	}
	return strings.TrimSuffix(in, "Config")
}

// --- shared ----------------------------------------------------------------

// pickList finds the array in a Graylog listing response, and says what it
// found when it cannot.
//
// Graylog answers a listing in three shapes depending on the endpoint's age: a
// bare array, an object with the collection under a name of its own
// ("streams", "inputs"), and a generic paginated envelope whose list key is
// chosen per endpoint. Naming the candidates rather than guessing from the
// path is what keeps a rename upstream from reading as an empty installation.
//
// The failure mode this exists to prevent is observium's: a wrong key returned
// no items and no error, and the tool above reported that there were none. So
// an absent key is an error, and the message names the keys the response
// actually carried, because that is the fix.
func pickList(raw json.RawMessage, candidates ...string) (json.RawMessage, int, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, 0, fmt.Errorf("was an array this could not read: %w", err)
		}
		return raw, len(items), nil
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, 0, fmt.Errorf("was neither an array nor an object: %w", err)
	}

	total := -1
	if field, ok := body["total"]; ok {
		var n int
		if err := json.Unmarshal(field, &n); err == nil {
			total = n
		}
	}

	for _, name := range candidates {
		items, ok := body[name]
		if !ok {
			continue
		}
		if total < 0 {
			var decoded []json.RawMessage
			if err := json.Unmarshal(items, &decoded); err == nil {
				total = len(decoded)
			}
		}
		return items, total, nil
	}

	names := make([]string, 0, len(body))
	for name := range body {
		names = append(names, name)
	}
	sort.Strings(names)
	return nil, 0, fmt.Errorf("carried no %s -- it answered with %s. The API "+
		"has renamed the field this reads, so the endpoint's answer is being "+
		"read as an empty one",
		strings.Join(candidates, " or "), strings.Join(names, ", "))
}
