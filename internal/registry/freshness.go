package registry

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNotModified reports that a conditional request was answered 304: what is
// already held is still current, and nothing was re-sent.
//
// A sentinel rather than a Page, because there is no page to return -- the
// point of the exchange is that the body did not travel. The cache turns it
// back into the answer it already has.
var ErrNotModified = errors.New("registry: not modified")

// Freshness is what a catalogue said about reusing one answer.
//
// It is read from the response's own headers rather than decided here, because
// how long a catalogue's answer stays true is the catalogue's to say and the
// three that have been measured say three different things. It is not part of
// the wire shape: what the dashboard needs to know about staleness is on Page
// already, in the form of a flag and a timestamp.
type Freshness struct {
	// TTL is how long the answer may be reused without asking again. Zero
	// means the catalogue said nothing, and the cache's configured default
	// stands in.
	TTL time.Duration
	// StaleWhile is how long past TTL the answer may still be served while a
	// refresh runs behind it. Only ever what the catalogue granted.
	StaleWhile time.Duration
	// NoStore is the catalogue asking that the answer not be held at all.
	NoStore bool
	// Validators let the next request ask "has this changed?" instead of
	// "send it again".
	Validators Validators
}

// Validators are the HTTP cache validators from a previous answer.
type Validators struct {
	ETag         string
	LastModified string
}

func (v Validators) empty() bool { return v.ETag == "" && v.LastModified == "" }

// Revalidating is a catalogue that can be asked to confirm a previous answer
// rather than re-send it.
//
// Optional, and probed for rather than required, because it is worth having
// only where the far end offers a validator. Of the catalogues measured, the
// official registry sends none at all and Docker's CDN sends both an ETag and
// a Last-Modified -- so a refresh of Docker's 567 KiB catalogue is a 304 and a
// few hundred bytes rather than the whole document again.
type Revalidating interface {
	Client
	// ListIfChanged is List, with the answer's previous validators. It returns
	// ErrNotModified when the catalogue says nothing has changed.
	ListIfChanged(ctx context.Context, q Query, v Validators) (Page, error)
	// GetIfChanged is Get, with the same contract.
	GetIfChanged(ctx context.Context, name string, v Validators) (Detail, error)
}

// noCacheTTL is how long an answer marked `no-cache` is reused.
//
// `no-cache` means revalidate before use, not "never store". Where a validator
// is offered the revalidation is a conditional request and this never applies.
// Where none is offered -- PulseMCP sends `no-cache` and no ETag -- there is
// nothing to revalidate *with*, and the two honest readings are "hold it for a
// moment" or "ignore the directive". A minute is the first: short enough that
// the catalogue's wish is substantially met, long enough that a person typing
// into a search box does not generate one upstream request per keystroke.
const noCacheTTL = time.Minute

// readFreshness reads what a response says about reusing it.
func readFreshness(resp *http.Response) Freshness {
	out := Freshness{Validators: Validators{
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
	}}

	directives := parseCacheControl(resp.Header.Get("Cache-Control"))
	switch {
	case directives.noStore:
		out.NoStore = true
		return out
	case directives.noCache:
		out.TTL = noCacheTTL
	case directives.sharedMaxAge >= 0:
		// s-maxage before max-age: mcpd is a shared cache serving every
		// administrator of this deployment, not one person's browser, and a
		// catalogue that distinguishes the two means the larger number for us.
		// Smithery grants sixty seconds to a browser and four hours to a
		// shared cache, and taking the sixty would be throwing away what was
		// offered.
		out.TTL = directives.sharedMaxAge
	case directives.maxAge >= 0:
		out.TTL = directives.maxAge
	}

	// Age is how long the answer sat in somebody else's cache before reaching
	// this one. Counting it is the difference between "fresh for four hours"
	// and "fresh for four hours from whenever the CDN fetched it".
	if out.TTL > 0 {
		if age, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Age"))); err == nil && age > 0 {
			out.TTL -= time.Duration(age) * time.Second
			if out.TTL < 0 {
				out.TTL = 0
			}
		}
	}
	if directives.staleWhileRevalidate > 0 {
		out.StaleWhile = directives.staleWhileRevalidate
	}
	return out
}

// cacheDirectives is the subset of Cache-Control this host acts on.
//
// `must-revalidate` is deliberately absent. It forbids serving a stale answer
// when the refresh fails, and this host serves one anyway and says so: an
// operator looking at a catalogue marked "this is what we last saw, at 10:04"
// can still see what is there and still decide to import one, where an
// operator looking at an error page can do neither. That is a decision about
// this deployment's own availability, and it is not a third party's to make.
type cacheDirectives struct {
	noStore              bool
	noCache              bool
	maxAge               time.Duration
	sharedMaxAge         time.Duration
	staleWhileRevalidate time.Duration
}

func parseCacheControl(header string) cacheDirectives {
	out := cacheDirectives{maxAge: -1, sharedMaxAge: -1}
	for _, part := range strings.Split(header, ",") {
		directive := strings.TrimSpace(strings.ToLower(part))
		if directive == "" {
			continue
		}
		name, value, hasValue := strings.Cut(directive, "=")
		name = strings.TrimSpace(name)
		seconds := func() (time.Duration, bool) {
			if !hasValue {
				return 0, false
			}
			n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(value), `"`))
			if err != nil || n < 0 {
				return 0, false
			}
			return time.Duration(n) * time.Second, true
		}
		switch name {
		case "no-store":
			out.noStore = true
		case "no-cache":
			out.noCache = true
		case "max-age":
			if d, ok := seconds(); ok {
				out.maxAge = d
			}
		case "s-maxage":
			if d, ok := seconds(); ok {
				out.sharedMaxAge = d
			}
		case "stale-while-revalidate":
			if d, ok := seconds(); ok {
				out.staleWhileRevalidate = d
			}
		}
	}
	return out
}

// applyValidators makes a request conditional.
//
// Both headers, when both are held. A far end that understands neither answers
// 200 with the body, which is exactly what would have happened anyway -- so
// this costs nothing where it does not work, which is why it is written for
// catalogues that do not offer a validator today.
func applyValidators(req *http.Request, v Validators) {
	if v.ETag != "" {
		req.Header.Set("If-None-Match", v.ETag)
	}
	if v.LastModified != "" {
		req.Header.Set("If-Modified-Since", v.LastModified)
	}
}

// refreshKey marks a request as an operator asking the catalogue again now.
type refreshKey struct{}

// WithRefresh marks a request as an explicit "ask again", bypassing whatever
// is cached for it.
//
// On the context rather than in Query, because Get takes a name and no query,
// and a flag that only half the surface could carry would be a refresh button
// that worked on the list and not on the entry behind it. It is request-scoped
// and crosses an interface boundary without widening it, which is what a
// context value is for.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// RefreshRequested reports an explicit refresh.
//
// Exported so that the handler that sets it can be tested against the same
// question the cache asks, rather than against a copy of the rule.
func RefreshRequested(ctx context.Context) bool {
	asked, _ := ctx.Value(refreshKey{}).(bool)
	return asked
}
