// Package notify sends this host's own events somewhere an operator will see
// them.
//
// What it deliberately does not do is ask for anything. mcpd's approval model
// puts the decision where the work is -- a client's own confirmation below the
// inline ceiling, the assistant showing the change in full above it -- and a
// message saying "something is waiting, go to the dashboard" would build
// exactly the path that model exists to avoid. So every event here is a
// statement about something that already happened, and none of them carries a
// link to approve anything.
//
// Off until a URL is set, like crash reporting and for the same reason: mcpd
// runs on somebody else's hardware, and a host that started talking to an
// outside service because it was upgraded would be doing something nobody
// agreed to.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Severity says how much of somebody's attention an event is worth.
type Severity string

const (
	// SeverityInfo is something worth knowing about later.
	SeverityInfo Severity = "info"
	// SeverityWarning is something worth looking at today.
	SeverityWarning Severity = "warning"
)

// Event is one thing that happened.
type Event struct {
	// Kind is the stable name, e.g. "mcpservers.tools_changed". It is not
	// shown; it is what a receiving system routes on.
	Kind     string
	Title    string
	Text     string
	Severity Severity
}

// Format is the shape a receiver expects.
type Format string

const (
	// FormatJSON posts mcpd's own event, for anything that can read JSON.
	FormatJSON Format = "json"
	// FormatSlack posts {"text": ...}, which Slack, Mattermost and Discord's
	// Slack-compatible endpoint all accept.
	FormatSlack Format = "slack"
	// FormatNtfy posts ntfy's publishing shape, with the topic in the body.
	FormatNtfy Format = "ntfy"
	// FormatDiscord posts a Discord embed, which carries a colour.
	FormatDiscord Format = "discord"
)

// Discord's documented ceilings. A payload over one of them is refused
// outright, so a long event would otherwise be delivered as nothing at all.
const (
	discordTitleLimit       = 256
	discordDescriptionLimit = 4096
)

// The two colours, as Discord wants them: one integer, not a CSS string.
//
// Amber and blue rather than red and green. Nothing mcpd sends is an
// emergency -- every event is a statement about something that already
// happened -- and a wall of red trains somebody to close the channel.
const (
	discordAmber = 0xE8A33D
	discordBlue  = 0x5865F2
)

// Config is where and how to send.
type Config struct {
	URL    string
	Format Format
	// Topic is ntfy's destination. Ignored by the other formats.
	Topic string
	// Token is sent as a bearer credential when set.
	Token string
}

// Notifier delivers events without making the caller wait.
type Notifier struct {
	client *http.Client
	log    *slog.Logger
	config func(context.Context) Config

	// queue is bounded. A webhook that has stopped answering must cost this
	// host a dropped message rather than an ever-growing backlog, and it must
	// never be able to slow down the thing that raised the event.
	queue chan Event

	// dropped counts what the bound cost, reported once rather than per event
	// so a dead receiver cannot fill the log either.
	mu      sync.Mutex
	dropped int
}

const (
	queueDepth   = 64
	sendTimeout  = 10 * time.Second
	dropInterval = 5 * time.Minute
)

// New builds a notifier. The config is read per send, so an operator changing
// the address does not have to restart anything.
func New(log *slog.Logger, config func(context.Context) Config) *Notifier {
	return &Notifier{
		client: &http.Client{Timeout: sendTimeout},
		log:    log,
		config: config,
		queue:  make(chan Event, queueDepth),
	}
}

// Notify queues an event. It never blocks and never returns an error: a
// notification is a courtesy, and nothing this host does should fail because
// one could not be delivered.
func (n *Notifier) Notify(ctx context.Context, e Event) {
	if n == nil || n.config(ctx).URL == "" {
		return
	}
	select {
	case n.queue <- e:
	default:
		n.mu.Lock()
		n.dropped++
		n.mu.Unlock()
	}
}

// Run delivers queued events until the context ends.
func (n *Notifier) Run(ctx context.Context) error {
	report := time.NewTicker(dropInterval)
	defer report.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-n.queue:
			n.deliver(ctx, e)
		case <-report.C:
			n.reportDropped(ctx)
		}
	}
}

func (n *Notifier) reportDropped(ctx context.Context) {
	n.mu.Lock()
	dropped := n.dropped
	n.dropped = 0
	n.mu.Unlock()
	if dropped > 0 {
		n.log.WarnContext(ctx, "notifications were dropped because the receiver "+
			"could not keep up", "dropped", dropped)
	}
}

func (n *Notifier) deliver(ctx context.Context, e Event) {
	cfg := n.config(ctx)
	if cfg.URL == "" {
		return
	}
	if err := n.post(ctx, cfg, e); err != nil {
		// Warn rather than error, and without the body: a receiver being down
		// is somebody else's outage, and this host reporting it as its own
		// failure would bury the things that are its problem.
		n.log.WarnContext(ctx, "could not deliver a notification",
			"kind", e.Kind, "error", err)
	}
}

// Send delivers one event and waits, for the test button on the settings page.
// Everything else goes through Notify.
func (n *Notifier) Send(ctx context.Context, e Event) error {
	cfg := n.config(ctx)
	if cfg.URL == "" {
		return fmt.Errorf("notify: no address is configured")
	}
	return n.post(ctx, cfg, e)
}

func (n *Notifier) post(ctx context.Context, cfg Config, e Event) error {
	body, err := encode(cfg, e)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(cfg), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// The status and nothing else. A receiver's error page can carry
		// anything, and this host has no business copying it into a log.
		return fmt.Errorf("notify: the receiver answered %s", resp.Status)
	}
	return nil
}

// endpoint is where a payload of this shape has to be posted.
//
// Discord publishes one webhook at three addresses: the bare one takes
// Discord's own payload, and the /slack and /github suffixes take somebody
// else's. Somebody who chooses the discord shape and pastes the /slack address
// -- which is the address half the internet tells you to use -- would
// otherwise get a 400 with nothing in it to say why. The suffix is dropped
// rather than obeyed, because the shape the operator picked is the one they
// meant.
func endpoint(cfg Config) string {
	if cfg.Format != FormatDiscord {
		return cfg.URL
	}
	trimmed := strings.TrimRight(cfg.URL, "/")
	for _, suffix := range []string{"/slack", "/github"} {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSuffix(trimmed, suffix)
		}
	}
	return cfg.URL
}

// encode renders an event in the shape the receiver expects.
func encode(cfg Config, e Event) ([]byte, error) {
	switch cfg.Format {
	case FormatSlack:
		// One text field, which Slack, Mattermost and Discord's
		// Slack-compatible endpoint all accept. No blocks: a payload that
		// renders beautifully in one of them fails to render at all in
		// another, and this is a line of text either way.
		return json.Marshal(map[string]string{
			"text": strings.TrimSpace(e.Title + "\n" + e.Text),
		})

	case FormatDiscord:
		// An embed rather than a content string, because the colour is the
		// point: a warning has to be distinguishable from routine news at a
		// glance, and Discord offers no other way to say so.
		//
		// This is a deliberate exception to the rule the slack shape follows.
		// That rule exists because one payload has to render in Slack,
		// Mattermost and Discord at once, and a block kit that is beautiful in
		// one fails to render at all in another. A Discord-only shape does not
		// carry that constraint, so it can say more.
		return json.Marshal(map[string]any{
			"embeds": []any{map[string]any{
				"title":       truncate(e.Title, discordTitleLimit),
				"description": truncate(e.Text, discordDescriptionLimit),
				"color":       discordColour(e.Severity),
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
				"footer":      map[string]string{"text": "mcpd"},
			}},
		})

	case FormatNtfy:
		payload := map[string]any{
			"topic":   cfg.Topic,
			"title":   e.Title,
			"message": e.Text,
			// 4 of 5 for a warning, the default for anything else. Higher
			// would make every event bypass a phone's quiet hours, which is
			// not a decision this host should be making for somebody.
			"priority": ntfyPriority(e.Severity),
			"tags":     ntfyTag(e.Severity),
		}
		return json.Marshal(payload)

	default:
		return json.Marshal(map[string]any{
			"kind":     e.Kind,
			"title":    e.Title,
			"text":     e.Text,
			"severity": string(e.Severity),
			"source":   "mcpd",
			"at":       time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func discordColour(s Severity) int {
	if s == SeverityWarning {
		return discordAmber
	}
	return discordBlue
}

// truncate cuts a string to n characters, marking that it was cut.
//
// Counted in runes rather than bytes: Discord's limits are character counts,
// and slicing a byte at a time would also split a multi-byte one and produce
// invalid UTF-8 in the bargain.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "\u2026"
}

func ntfyPriority(s Severity) int {
	if s == SeverityWarning {
		return 4
	}
	return 3
}

func ntfyTag(s Severity) string {
	if s == SeverityWarning {
		return "warning"
	}
	return "information_source"
}
