package bandwidth

import (
	"strings"
	"testing"
	"time"
)

// Describe goes into the startup log and the health report, and it formats the
// three configured addresses.
//
// This plugin used to carry its own redactURL. Despite a comment saying it
// "drops everything that could carry a credential", it kept the host *and* the
// userinfo in front of it -- it only trimmed from the first "/", "?" or "#", and
// a password sits before all three. Config.Validate accepts any http or https
// address, so a credential in one of these is a configuration an operator can
// actually write, and it went straight into the log.
func TestDescribe_DoesNotLeakACredentialInAConfiguredAddress(t *testing.T) {
	cfg := Config{
		APIURL:       "https://someone:hunter2@api.example.com",
		VoiceURL:     "https://someone:hunter2@voice.example.com",
		MessagingURL: "https://someone:hunter2@messaging.example.com",
		InsightsURL:  "https://insights.example.com",
	}
	got := NewClient(nil, cfg, nil, time.Now, nil).Describe()

	if strings.Contains(got, "hunter2") || strings.Contains(got, "someone") {
		t.Errorf("Describe leaked the credential in a configured address: %q", got)
	}
	// Still says where it reads from -- redacting must not blank the sentence.
	for _, want := range []string{"api.example.com", "voice.example.com", "messaging.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe no longer names %s: %q", want, got)
		}
	}
}
