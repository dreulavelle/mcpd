package bandwidth

import (
	"regexp"
	"strings"
)

// A campaign's decline reason arrives as one string holding several reasons,
// and the string is written twice over in two different hands.
//
// The Campaign Registry writes one reason per line, each opening "Rejection
// Code NNNN:" and carrying its own title and explanation:
//
//	Bandwidth: Rejection Code 5105: Missing Mandatory Message Terminology - The
//	opt-in message must contain disclosures on message frequency.
//
// A secondary DCA writes free prose with the code in parentheses at the end,
// and puts several complaints in one sentence:
//
//	DCA2: Unable to verify, needs compliant and accurate CTA information.  (806)
//
// Splitting them into codes is what makes "which rejection keeps recurring"
// answerable across campaigns. The parse is additive and never destructive:
// SecondaryDcaDeclineReason is returned exactly as Bandwidth wrote it, and this
// sits beside it. A carrier's wording is evidence, and paraphrasing evidence to
// fit a schema is how the reason a campaign was actually refused gets lost.
type DeclineReason struct {
	// Code is the rejection code as text, because these are identifiers
	// rather than quantities and nothing here does arithmetic on them.
	Code string `json:"code,omitempty"`
	// Title is the short category, present only in the registry's form. The
	// DCA's form has no title to take, and inventing one would put words in a
	// carrier's mouth.
	Title string `json:"title,omitempty"`
	// Description is the reason text, verbatim from the segment it came from.
	Description string `json:"description"`
	// Source is who is refusing: "Bandwidth" for a registry rejection, "DCA2"
	// for a secondary DCA. Worth keeping separate because the two do not
	// always agree, and a campaign can be accepted by one and refused by the
	// other.
	Source string `json:"source,omitempty"`
}

var (
	// The leading "Bandwidth:" or "DCA2:" that names who is speaking.
	declineSourceRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)\s*:\s*`)
	// "Rejection Code 5105:" — the registry's per-reason opener.
	rejectionCodeRe = regexp.MustCompile(`Rejection Code\s+(\d+)\s*:\s*`)
	// A trailing "(806)" — the DCA's way of naming a code.
	trailingCodeRe = regexp.MustCompile(`\((\d+)\)\s*$`)
)

// parseDeclineReasons splits a decline reason string into its parts.
//
// It returns nil for an empty string, and for a string it cannot take apart it
// returns the whole thing as one reason with no code rather than nothing at
// all. A reason nobody can parse is still a reason somebody needs to read, and
// dropping it would report an unparsed refusal as no refusal.
func parseDeclineReasons(raw string) []DeclineReason {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}

	source := ""
	if m := declineSourceRe.FindStringSubmatch(s); m != nil {
		// Only treat the prefix as a speaker when what follows is not itself
		// the registry's opener, so "Rejection Code 1: ..." with no speaker
		// does not have "Rejection" taken as one.
		if !strings.HasPrefix(s, "Rejection Code") {
			source = m[1]
			s = strings.TrimSpace(s[len(m[0]):])
		}
	}

	if locs := rejectionCodeRe.FindAllStringSubmatchIndex(s, -1); len(locs) > 0 {
		out := make([]DeclineReason, 0, len(locs))
		for i, loc := range locs {
			end := len(s)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			title, desc := splitDeclineTitle(strings.TrimSpace(s[loc[1]:end]))
			out = append(out, DeclineReason{
				Code:        s[loc[2]:loc[3]],
				Title:       title,
				Description: desc,
				Source:      source,
			})
		}
		return out
	}

	reason := DeclineReason{Description: s, Source: source}
	if m := trailingCodeRe.FindStringSubmatchIndex(s); m != nil {
		reason.Code = s[m[2]:m[3]]
		reason.Description = strings.TrimSpace(s[:m[0]])
	}
	return []DeclineReason{reason}
}

// splitDeclineTitle separates "Invalid Sample Messages - All samples must ..."
// into its category and its explanation.
//
// The first " - " is the separator and later ones are left alone: the
// explanations contain hyphens of their own, and splitting on the last would
// move most of the sentence into the title.
func splitDeclineTitle(body string) (title, description string) {
	body = strings.TrimSpace(body)
	if i := strings.Index(body, " - "); i >= 0 {
		return strings.TrimSpace(body[:i]), strings.TrimSpace(body[i+3:])
	}
	return "", body
}
