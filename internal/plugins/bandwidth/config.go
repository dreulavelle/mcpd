package bandwidth

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The production hosts. Bandwidth serves one product per host rather than one
// API root, so a call's address depends on what it is asking about.
const (
	defaultAPIURL       = "https://api.bandwidth.com"
	defaultVoiceURL     = "https://voice.bandwidth.com"
	defaultMessagingURL = "https://messaging.bandwidth.com"
)

// The test estate, which Bandwidth calls test or uat. Messaging has no
// separate host there; that is Bandwidth's arrangement, not an omission here.
const (
	testAPIURL   = "https://test.api.bandwidth.com"
	testVoiceURL = "https://test.voice.bandwidth.com"
)

const (
	defaultMaxItems = 200
	defaultTimeout  = 30 * time.Second
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// ClientID and ClientSecret are an OAuth2 API credential, made in the
	// Bandwidth console under API credentials. They are exchanged for a
	// short-lived bearer token; neither is ever sent to a product API.
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`

	// AccountID is which Bandwidth account this instance reads.
	//
	// Required, and deliberately not guessed from the credential even when it
	// is scoped to exactly one. A credential covering four accounts would
	// otherwise make the answer depend on how it happened to be created, and
	// two instances reading two accounts is the arrangement mcpd already has
	// a shape for.
	AccountID string `yaml:"account_id" json:"account_id"`

	// Environment selects the estate: "production" or "test".
	Environment string `yaml:"environment" json:"environment"`

	// The three hosts. Filled from Environment when empty, and settable
	// directly so a test can point the whole plugin at one server.
	APIURL       string `yaml:"api_url" json:"api_url"`
	VoiceURL     string `yaml:"voice_url" json:"voice_url"`
	MessagingURL string `yaml:"messaging_url" json:"messaging_url"`

	// MaxItems caps how many rows one listing returns. Reported in the result
	// when it bites, so a caller narrows their question rather than silently
	// seeing the first slice of an estate.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// Timeout bounds one upstream call.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

// withDefaults fills what an operator did not set.
func (c *Config) withDefaults() {
	c.Environment = strings.ToLower(strings.TrimSpace(c.Environment))
	test := c.Environment == "test" || c.Environment == "uat"

	if c.APIURL == "" {
		c.APIURL = defaultAPIURL
		if test {
			c.APIURL = testAPIURL
		}
	}
	if c.VoiceURL == "" {
		c.VoiceURL = defaultVoiceURL
		if test {
			c.VoiceURL = testVoiceURL
		}
	}
	if c.MessagingURL == "" {
		// No test host of its own; the production one serves both estates.
		c.MessagingURL = defaultMessagingURL
	}
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}

	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = strings.TrimSpace(c.ClientSecret)
	c.AccountID = strings.TrimSpace(c.AccountID)
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	c.VoiceURL = strings.TrimRight(c.VoiceURL, "/")
	c.MessagingURL = strings.TrimRight(c.MessagingURL, "/")
}

// Configured reports whether enough was supplied to reach the API.
//
// All three, because a credential without an account has nothing to read and
// an account without a credential cannot be read. A plugin that is not
// configured still mounts, so its settings form has somewhere to live.
func (c Config) Configured() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.AccountID != ""
}

// Validate rejects a configuration that cannot work.
func (c Config) Validate() error {
	if !c.Configured() {
		return nil
	}
	if strings.ContainsAny(c.AccountID, "/?#") {
		return fmt.Errorf("bandwidth: account id %q is not an account id; it "+
			"is the number shown beside the account name in the Bandwidth "+
			"console, such as 5009021", c.AccountID)
	}
	for _, u := range []string{c.APIURL, c.VoiceURL, c.MessagingURL} {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("bandwidth: %q is not an http or https address", u)
		}
	}
	if c.Environment != "" && c.Environment != "production" &&
		c.Environment != "test" && c.Environment != "uat" {
		return errors.New(`bandwidth: environment must be "production" or "test"`)
	}
	return nil
}

// redactSecret removes the client secret from an error before it reaches a log
// line or the dashboard. Transport errors quote request details freely.
func redactSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, secret, "[REDACTED]"))
}

// redactURL keeps the host and drops everything that could carry a credential.
func redactURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return raw[:i+3] + rest[:j]
		}
	}
	return raw
}
