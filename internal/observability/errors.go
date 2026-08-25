package observability

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// Crash reporting, for a process running on somebody else's hardware.
//
// mcpd is deployed onto a customer's network and manages their infrastructure.
// A crash report is therefore the only thing it sends anywhere the operator
// did not choose, which makes three things non-negotiable:
//
// It is off unless somebody turns it on. No DSN means no client, no goroutine
// and no network calls -- not a client pointed at nowhere. An operator who has
// never opened the settings page has not silently agreed to anything.
//
// Everything is scrubbed on the way out. Not at the call sites, which would
// mean every future caller remembering: in BeforeSend, which is the single
// gate every event passes through no matter who raised it or which SDK
// integration added it.
//
// Nothing identifies the machine unless asked for. Sentry defaults ServerName
// to the host's own name, which on a customer deployment is the customer. It
// is replaced by a label an operator types, empty by default, so identifying
// an installation is a decision somebody made rather than a default they
// inherited.

// ErrorReporterOptions is what an operator configured.
type ErrorReporterOptions struct {
	// DSN addresses the collector. Empty disables reporting entirely.
	DSN string
	// Environment separates a test deployment from a real one.
	Environment string
	// Release is the mcpd version, so an error can be tied to a build.
	Release string
	// InstanceLabel identifies this installation in the collector. Empty
	// sends nothing identifying, which is the default: on a customer's own
	// hardware the machine's name is the customer's name.
	InstanceLabel string
	// IncludeMessages sends the text of panics and errors alongside the stack
	// trace. Scrubbed either way; this decides whether the sentence travels at
	// all.
	//
	// Off by default. A stack trace says which code path failed, which is most
	// of the value, and it is structurally incapable of carrying a device name
	// -- Go does not put argument values in one. The messages are where the
	// customer's estate lives, so sending them is a separate decision.
	IncludeMessages bool
	// TracesSampleRate enables performance tracing. Zero disables it, which is
	// the default: a trace carries request paths and timings that say more
	// about what the customer is doing than about whether mcpd is broken.
	TracesSampleRate float64
	// Log receives what this package decides, so an operator can see reporting
	// start and stop in their own log.
	Log *slog.Logger
}

// ErrorReporter sends crashes to a collector, or does nothing.
//
// The nil reporter is valid and does nothing, so a caller never needs a check
// before capturing. That matters at the four places this is called from: they
// are panic handlers, and a nil check that got forgotten there would turn a
// recovered panic into a second one.
type ErrorReporter struct {
	hub  *sentry.Hub
	opts ErrorReporterOptions
	log  *slog.Logger
}

// NewErrorReporter builds a reporter, or returns nil when reporting is off.
//
// Returning nil rather than a disabled reporter is deliberate: there is then
// no client, no background goroutine and no possibility of a misconfiguration
// that quietly sends something.
func NewErrorReporter(opts ErrorReporterOptions) (*ErrorReporter, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(opts.DSN) == "" {
		return nil, nil
	}

	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:         opts.DSN,
		Environment: opts.Environment,
		Release:     opts.Release,
		// The label, or nothing. Sentry fills this with os.Hostname() when it
		// is empty, so "nothing" has to be an explicit space rather than an
		// absence -- an empty string here is read as "you did not choose" and
		// the machine name is used.
		ServerName: serverName(opts.InstanceLabel),
		// PII is never sent. It is the default; stated here because it is the
		// kind of default somebody changes without thinking about whose
		// machine this is running on.
		SendDefaultPII:   false,
		AttachStacktrace: true,
		EnableTracing:    opts.TracesSampleRate > 0,
		TracesSampleRate: opts.TracesSampleRate,
		BeforeSend:       scrubEvent(opts.IncludeMessages, opts.InstanceLabel),
		BeforeSendTransaction: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			// Transactions carry request paths. They are only produced when
			// tracing is switched on, and they pass the same gate.
			return scrubEvent(opts.IncludeMessages, opts.InstanceLabel)(event, nil)
		},
		BeforeBreadcrumb: func(b *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
			if b == nil {
				return nil
			}
			b.Message = Scrub(b.Message)
			// Our own breadcrumbs come from the log handler and carry an
			// allow-listed set of keys chosen for identifying a code path.
			// Their values are scrubbed and kept, because a breadcrumb without
			// them says "something failed" and with them says which plugin,
			// which tool and under which correlation ID.
			//
			// Anything else was added by an SDK integration and has not been
			// reviewed here, so its data is dropped rather than guessed about.
			if b.Category == breadcrumbCategory {
				for k, v := range b.Data {
					if text, ok := v.(string); ok {
						b.Data[k] = Scrub(text)
					}
				}
			} else {
				b.Data = nil
			}
			return b
		},
	})
	if err != nil {
		return nil, fmt.Errorf("observability: error reporting: %w", err)
	}

	log.Info("error reporting is on; crashes will be sent off this machine",
		"environment", opts.Environment,
		"identifies_this_installation", opts.InstanceLabel != "",
		"sends_messages", opts.IncludeMessages,
		"tracing", opts.TracesSampleRate > 0)

	return &ErrorReporter{
		hub:  sentry.NewHub(client, sentry.NewScope()),
		opts: opts,
		log:  log,
	}, nil
}

// serverName resolves what the collector is told this machine is called.
//
// A single space rather than an empty string, because the SDK treats empty as
// "not configured" and substitutes os.Hostname(). On a customer deployment
// that hostname is often the most identifying thing in the whole report.
func serverName(label string) string {
	if l := strings.TrimSpace(label); l != "" {
		return l
	}
	return " "
}

// scrubEvent is the last thing every event passes through.
//
// Every field here holds text somebody wrote for a log file on their own
// machine. Listing them explicitly rather than walking the struct is the point:
// a field the SDK adds later is one this has not considered, and it should
// show up as an unscrubbed field in review rather than be handled by a generic
// pass that nobody checked.
func scrubEvent(includeMessages bool, label string) func(*sentry.Event, *sentry.EventHint) *sentry.Event {
	return func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
		if event == nil {
			return nil
		}

		if includeMessages {
			event.Message = Scrub(event.Message)
		} else {
			event.Message = ""
		}

		for i := range event.Exception {
			ex := &event.Exception[i]
			// The type is a Go type name and says what failed. The value is
			// the sentence, and the sentence is where a customer's estate is
			// named.
			ex.Type = Scrub(ex.Type)
			if includeMessages {
				ex.Value = Scrub(ex.Value)
			} else {
				ex.Value = "(message withheld; turn on \"Send error messages\" to include it)"
			}
			if ex.Stacktrace != nil {
				for j := range ex.Stacktrace.Frames {
					f := &ex.Stacktrace.Frames[j]
					// Go stack traces carry no argument values, so a frame is
					// paths and identifiers -- which is why they are the part
					// worth sending.
					//
					// AbsPath goes entirely. It is the absolute path on
					// whatever machine compiled the binary, so it carries a
					// build user's home directory and says nothing the
					// repository-relative Filename does not. Dropping it
					// removes a whole category of leak for no loss: Sentry
					// groups and source-maps on Filename, Module and Function.
					f.AbsPath = ""
					f.Filename = Scrub(f.Filename)
					f.Module = Scrub(f.Module)
					// Populated by some integrations, and the one place a Go
					// frame can carry a value rather than a name.
					f.Vars = nil
				}
			}
		}

		for i := range event.Threads {
			if st := event.Threads[i].Stacktrace; st != nil {
				for j := range st.Frames {
					st.Frames[j].Vars = nil
				}
			}
		}

		// A user is a person on the customer's side and has no business in a
		// crash report about our code.
		event.User = sentry.User{}

		// A request carries the URL, headers and cookies of whoever was
		// talking to mcpd.
		event.Request = nil

		// Contexts is an open map. The only key this process sets is the
		// recovered stack, which was scrubbed before it went in; anything else
		// came from an SDK integration and has not been reviewed here.
		for key, ctx := range event.Contexts {
			if key == "recovered" {
				continue
			}
			// device and os describe the machine, which is the customer's.
			switch key {
			case "device", "os", "runtime", "trace":
				delete(event.Contexts, key)
			default:
				for field, value := range ctx {
					if text, ok := value.(string); ok {
						ctx[field] = Scrub(text)
					}
				}
			}
		}

		for k, v := range event.Tags {
			event.Tags[k] = Scrub(v)
		}
		// Forced from the configured label rather than read off the event.
		// Trusting what is already there is what a gate must not do: the SDK
		// stamps this before BeforeSend runs, and anything that set it to the
		// machine's own name would have been passed straight through.
		event.ServerName = serverName(label)
		return event
	}
}

// CapturePanic reports a recovered panic.
//
// It takes the value and the stack the recovering code already has, rather
// than re-deriving one, so the trace points at where the panic happened and
// not at this function.
//
// Safe on a nil reporter, which is what reporting being off looks like.
func (r *ErrorReporter) CapturePanic(v any, stack []byte, where string) {
	if r == nil {
		return
	}
	err := fmt.Errorf("panic in %s: %v", where, v)

	hub := r.hub.Clone()
	hub.Scope().SetTag("component", where)
	if len(stack) > 0 {
		// The stack the caller recovered with, as an attachment rather than a
		// parsed trace: the SDK's own capture would start here.
		hub.Scope().SetContext("recovered", map[string]any{
			"stack": Scrub(string(stack)),
		})
	}
	hub.CaptureException(err)
}

// CaptureError reports something that should not have happened.
//
// Deliberately not called from anywhere that returns an ordinary error. This
// project returns errors for outcomes that are simply the answer -- a device
// that is not there, a rate limit, a proposal somebody rejected -- and sending
// those would fill the collector with a working system's normal behaviour and
// carry a customer's estate along with it. Reserve it for the branch whose
// comment says it cannot happen.
func (r *ErrorReporter) CaptureError(err error, where string) {
	if r == nil || err == nil {
		return
	}
	hub := r.hub.Clone()
	hub.Scope().SetTag("component", where)
	hub.CaptureException(err)
}

// Flush waits for queued events, so a crash on the way out is still reported.
func (r *ErrorReporter) Flush(timeout time.Duration) {
	if r == nil {
		return
	}
	if !r.hub.Flush(timeout) {
		r.log.Warn("error reporting did not finish sending before shutdown",
			"waited", timeout)
	}
}

// Enabled reports whether anything is actually being sent, for the health
// endpoint and for a startup line an operator can see.
func (r *ErrorReporter) Enabled() bool { return r != nil }

// TestEvent sends one event on demand, so an operator can confirm the DSN
// works without waiting for something to break.
//
// It carries no estate data by construction: a fixed sentence and nothing
// else.
func (r *ErrorReporter) TestEvent() error {
	if r == nil {
		return fmt.Errorf("error reporting is off; set a DSN first")
	}
	r.hub.CaptureMessage("mcpd test event — error reporting is configured correctly")
	if !r.hub.Flush(10 * time.Second) {
		return fmt.Errorf("the event was queued but the collector did not " +
			"acknowledge it within ten seconds; check the DSN and that this " +
			"machine can reach it")
	}
	return nil
}

// hostnameForDiagnostics is used nowhere in reporting. It exists so the
// settings help can tell an operator what would be sent if they filled the
// label in, without this package being the thing that sends it.
func hostnameForDiagnostics() string {
	h, err := os.Hostname()
	if err != nil {
		return "this machine"
	}
	return h
}

var _ = hostnameForDiagnostics
