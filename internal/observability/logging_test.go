package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// How much mcpd says and in what shape are settings, and settings are in a
// database that is not open when the logger has to exist. The logger is
// therefore built with both handlers in it, and this is the test that the
// switch actually switches.
func TestSwitchableLogger_LevelAndFormatApplyWithoutARestart(t *testing.T) {
	var buf bytes.Buffer
	log, ctl := NewSwitchableLogger(&buf, slog.LevelInfo, "json")

	log.Debug("not at this level")
	if buf.Len() != 0 {
		t.Fatalf("debug was emitted at info: %s", buf.String())
	}

	log.Info("hello")
	if got := buf.String(); !strings.HasPrefix(got, "{") {
		t.Fatalf("output = %q, want JSON", got)
	}

	buf.Reset()
	ctl.Set(slog.LevelDebug, "text")
	log.Debug("now visible")
	got := buf.String()
	if strings.HasPrefix(got, "{") {
		t.Fatalf("output = %q, want text after the format changed", got)
	}
	if !strings.Contains(got, "now visible") {
		t.Fatalf("output = %q, want the debug record after the level changed", got)
	}
}

// Components are handed a logger derived with With(...) at wiring time, long
// before the settings are read. A format change afterwards must not lose the
// attributes they were given -- a component whose name disappeared from its log
// lines the moment somebody switched to text would be worse than not offering
// the setting.
func TestSwitchableLogger_DerivedLoggersFollowTheSwitch(t *testing.T) {
	var buf bytes.Buffer
	log, ctl := NewSwitchableLogger(&buf, slog.LevelInfo, "json")
	component := log.With("component", "tunnel").WithGroup("detail")

	ctl.Set(slog.LevelInfo, "text")
	component.Info("connected", "attempt", 2)

	got := buf.String()
	if strings.HasPrefix(got, "{") {
		t.Fatalf("a derived logger kept the old format: %q", got)
	}
	for _, want := range []string{"component=tunnel", "detail.attempt=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q is missing %q", got, want)
		}
	}
}

// Redaction is not a property of the format, and a switch that rebuilt the
// handlers without it would quietly start printing credentials.
func TestSwitchableLogger_RedactionSurvivesBothFormats(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var buf bytes.Buffer
		log, ctl := NewSwitchableLogger(&buf, slog.LevelInfo, "json")
		ctl.Set(slog.LevelInfo, format)
		log.Info("dialling", "api_key", "sk-should-never-appear")
		if strings.Contains(buf.String(), "sk-should-never-appear") {
			t.Errorf("%s output leaked a credential: %s", format, buf.String())
		}
	}
}

// The change arrives on a dashboard request while every other goroutine is
// logging, so it has to be safe under the race detector.
func TestSwitchableLogger_IsSafeUnderConcurrentUse(t *testing.T) {
	log, ctl := NewSwitchableLogger(&bytes.Buffer{}, slog.LevelInfo, "json")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				log.Info("working", "n", j)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			if j%2 == 0 {
				ctl.Set(slog.LevelDebug, "text")
			} else {
				ctl.Set(slog.LevelWarn, "json")
			}
		}
	}()
	wg.Wait()
}

// A nil control is what a test or an embedder that never asked for one holds,
// and it must not be a panic waiting for the first settings change.
func TestLogControl_NilIsSafe(t *testing.T) {
	var ctl *LogControl
	ctl.Set(slog.LevelDebug, "text")
}
