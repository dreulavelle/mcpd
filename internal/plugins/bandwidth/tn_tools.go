package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// One telephone number, from every angle Bandwidth will describe it.
//
// These endpoints are unusual in this API: they hang off /tns rather than off
// an account, because a number is a thing in the North American numbering plan
// before it is a thing on anybody's account. So they answer for a number this
// account does not hold -- which is the point, when the question is "where did
// this number go".

// tnPattern is the shape these endpoints accept: ten digits, no punctuation.
//
// Enforced here rather than passed through, because Bandwidth answers a
// malformed number with a 404 that is indistinguishable from a number that does
// not exist, and the two send somebody looking in completely different places.
var tnPattern = regexp.MustCompile(`^[2-9][0-9]{9}$`)

func (p *Plugin) registerTNTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_number",
		Title: "Get everything about one telephone number",
		Description: "One number from every angle: its state, rate centre, LATA " +
			"and local calling area, its E911 record, and the site and SIP peer " +
			"routing it on this account.\n\n" +
			"It answers whether or not this account holds the number, because " +
			"the numbering plan is not account-scoped — so it is also how you " +
			"find out a number has left. Sections that cannot be read are named " +
			"in not_read with the reason, and the rest still comes back: a " +
			"number on another carrier having no site here is an answer, not a " +
			"fault.",
		Idempotent: true,
	}, p.getNumber)
}

// NumberInput names one telephone number.
type NumberInput struct {
	PhoneNumber string `json:"phone_number" jsonschema:"the number in 10-digit form, such as 9195551234"`
	// The sub-reads are on by default rather than opt-in. Each is one small
	// request, the question this tool answers is "everything", and a caller who
	// has to know which flag to set in order to get an answer is being asked to
	// know the thing they came here to find out.
	WithoutRouting bool `json:"without_routing,omitempty" jsonschema:"skip the site and SIP peer lookup, which is empty for a number this account does not hold"`
}

// NumberOutput is one number and everything readable about it.
type NumberOutput struct {
	PhoneNumber string `json:"phone_number"`
	// Number is the number's own record: status, account, order history
	// pointers -- whatever Bandwidth holds against it.
	Number Record `json:"number,omitempty"`
	// Details is the fuller description, which carries the fields an operator
	// recognises from the Dashboard.
	Details Record `json:"details,omitempty"`
	// E911 is the emergency record. Its absence is a finding rather than a
	// gap: a number in service with no E911 record is a compliance problem.
	E911 Record `json:"e911,omitempty"`
	// Where the number sits in the numbering plan. Rate centre and LATA decide
	// what a call to it costs and whether it can be ported to a given carrier.
	RateCenter Record `json:"rate_center,omitempty"`
	LATA       Record `json:"lata,omitempty"`
	// LocalCallingArea is what can reach it without long distance.
	LocalCallingArea Record `json:"local_calling_area,omitempty"`
	// Sites and SipPeers are where this account routes it, and are empty for a
	// number this account does not hold.
	Sites    []Record `json:"sites,omitempty"`
	SipPeers []Record `json:"sip_peers,omitempty"`
	// Missing names the sections that could not be read and why, so an absent
	// field is never confused with an absent fact.
	Missing []string `json:"not_read,omitempty"`
	Note    string   `json:"note,omitempty"`
}

func (p *Plugin) getNumber(ctx context.Context, in NumberInput) (NumberOutput, error) {
	if err := p.ready(); err != nil {
		return NumberOutput{}, err
	}
	tn := normaliseTN(in.PhoneNumber)
	if !tnPattern.MatchString(tn) {
		return NumberOutput{}, fmt.Errorf("bandwidth: %q is not a telephone "+
			"number these endpoints accept. They want ten digits and nothing "+
			"else -- no +1, no punctuation -- as in 9195551234. Bandwidth "+
			"answers a malformed number with the same 404 it gives a number "+
			"that does not exist, so this is checked here instead",
			in.PhoneNumber)
	}

	out := NumberOutput{PhoneNumber: tn}
	base := "/tns/" + url.PathEscape(tn)

	// Each section is read on its own and a failure is recorded rather than
	// returned. A number that is not on this account answers several of these
	// with a 404, and refusing to report the parts that did come back would
	// make the tool useless for exactly the case it is best at.
	one := func(label, path string, into *Record) {
		rec, err := p.client.getXML(ctx, path, nil)
		if err != nil {
			out.Missing = append(out.Missing, label+" ("+shortErr(err)+")")
			return
		}
		*into = rec
	}
	one("the number", base, &out.Number)
	one("details", base+"/tndetails", &out.Details)
	one("E911", base+"/e911", &out.E911)
	one("rate centre", base+"/ratecenter", &out.RateCenter)
	one("LATA", base+"/lata", &out.LATA)
	one("local calling area", base+"/lca", &out.LocalCallingArea)

	if !in.WithoutRouting {
		many := func(label, path, element string, into *[]Record) {
			rec, err := p.client.getXML(ctx, path, nil)
			if err != nil {
				out.Missing = append(out.Missing, label+" ("+shortErr(err)+")")
				return
			}
			items, _ := collect(rec, "", element)
			*into = items
		}
		many("sites", base+"/sites", "Site", &out.Sites)
		many("SIP peers", base+"/sippeers", "SipPeer", &out.SipPeers)
	}

	p.note(nil, nil)

	switch {
	case len(out.Missing) == 0:
	case out.Number == nil && out.Details == nil:
		out.Note = "none of this number's records could be read. That is what a " +
			"number outside Bandwidth's footprint looks like, and also what a " +
			"credential without number-management rights looks like — the " +
			"reasons above say which."
	default:
		out.Note = "some sections were not readable; the rest are above. A " +
			"number this account does not hold has no site or SIP peer here, " +
			"which is an answer rather than a fault."
	}
	return out, nil
}

// normaliseTN takes the forms people actually paste and returns ten digits.
//
// A number arrives as +1 919 555 1234, (919) 555-1234 or 19195551234 depending
// on which system it was copied out of, and every one of those is the same
// number. Rejecting them all in favour of one spelling would be correct and
// useless.
func normaliseTN(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	// A leading 1 is the country code, not part of the number.
	if len(digits) == 11 && digits[0] == '1' {
		return digits[1:]
	}
	return digits
}

// shortErr trims an upstream error to something that fits beside nine others.
//
// The full text explains itself at length, which is right when it is the only
// thing returned and wrong when it is one of ten lines in a composite answer.
func shortErr(err error) string {
	msg := strings.TrimPrefix(err.Error(), "bandwidth: ")
	if i := strings.IndexAny(msg, ".\n"); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return msg
}
