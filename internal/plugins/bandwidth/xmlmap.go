package bandwidth

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// decodeXML turns a Dashboard response into the same shape a JSON one decodes
// to, so everything above this line is indifferent to which half of Bandwidth
// answered.
//
// Half of Bandwidth speaks JSON and half speaks XML, and that is a fact about
// Bandwidth rather than a choice worth propagating into fourteen tools. The
// alternative -- a typed struct per response -- would be several hundred lines
// of field declarations that exist only to be turned straight back into a map
// for the model, and would break silently whenever Bandwidth added a field.
//
// The outer <XxxResponse> element is unwrapped: it names the request rather
// than the data, the caller already knows what it asked for, and keeping it
// would put a pointless level of nesting in front of every answer.
func decodeXML(body []byte) (Record, error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("the response was empty")
		}
		if err != nil {
			return nil, fmt.Errorf("the response is not well-formed XML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		value, err := decodeElement(dec, start)
		if err != nil {
			return nil, err
		}
		// A root carrying only text is not a document this integration knows
		// what to do with; naming it beats returning an empty map.
		out, ok := value.(Record)
		if !ok {
			return Record{start.Name.Local: value}, nil
		}
		return out, nil
	}
}

// decodeElement reads one element and everything inside it.
//
// It returns a string for an element holding only text, and a Record for one
// holding children. Repeated children collapse into a slice, which is where
// the one real trap in XML-to-map conversion lives -- see appendChild.
func decodeElement(dec *xml.Decoder, start xml.StartElement) (any, error) {
	out := Record{}
	var text strings.Builder

	// Attributes are kept rather than dropped, under an @ prefix so they
	// cannot collide with a child element of the same name. Dashboard's XML
	// carries almost none, but silently discarding data because it usually is
	// not there is how a field goes missing for a year.
	for _, attr := range start.Attr {
		out["@"+attr.Name.Local] = attr.Value
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("the response ended inside <%s>", start.Name.Local)
		}
		if err != nil {
			return nil, fmt.Errorf("the response is not well-formed XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			child, err := decodeElement(dec, t)
			if err != nil {
				return nil, err
			}
			appendChild(out, t.Name.Local, child)

		case xml.CharData:
			text.Write(t)

		case xml.EndElement:
			if len(out) == 0 {
				// Text-only, which is most leaves. Trimmed because XML
				// indentation is whitespace inside the element.
				return strings.TrimSpace(text.String()), nil
			}
			// Mixed content: children and text together. The text is
			// indentation in every Dashboard response seen, so it is dropped
			// only when it is blank, and kept under a name that cannot
			// collide when it is not.
			if trimmed := strings.TrimSpace(text.String()); trimmed != "" {
				out["#text"] = trimmed
			}
			return out, nil
		}
	}
}

// appendChild adds one child, turning a repeated name into a slice.
//
// This is the trap. XML has no way to say "this is a list", so a collection
// with one member is indistinguishable from a single value: <TelephoneNumber>
// appearing once decodes to a map and appearing twice decodes to a slice, and
// a caller written against the two-member case breaks on an account that
// happens to have one number. Nothing here can fix that -- the information is
// genuinely absent from the document -- so it is fixed at the call site
// instead, where the element name is known. See listOf.
func appendChild(out Record, name string, value any) {
	existing, seen := out[name]
	if !seen {
		out[name] = value
		return
	}
	if list, ok := existing.([]any); ok {
		out[name] = append(list, value)
		return
	}
	out[name] = []any{existing, value}
}

// listOf pulls a repeated element out of a decoded response as a slice,
// whatever it decoded to.
//
// The caller names the container and the element inside it, because it knows
// what it asked for and the document does not say. One member, many members
// and none all come back as a slice, which is what every caller here wants and
// what none of them should have to write.
//
//	listOf(rec, "TelephoneNumbers", "TelephoneNumber")
//
// A missing container is an empty slice rather than an error: Bandwidth omits
// the container entirely when a collection is empty, and an account with no
// numbers is not a failure.
func collect(rec Record, container, element string) (items []Record, note string) {
	if items = listOf(rec, container, element); len(items) > 0 {
		return items, ""
	}
	// The named shape found nothing. Either the collection really is empty, or
	// the names are wrong -- and those two must not look the same. Guessing an
	// element name wrong once reported four endpoints as empty while Bandwidth
	// was returning a hundred kilobytes, which is the worst kind of wrong an
	// integration can be: an operator told their estate is empty believes it.
	if items = discoverList(rec); len(items) > 0 {
		return items, ""
	}
	// A collection with exactly one row in it.
	//
	// discoverList looks for a *repeated* element, which is a sound way to find
	// a list whose wrapper is named something nobody guessed -- and it cannot
	// see a list of one, because one row is not repeated. That gap is not
	// theoretical: a port-out listing returned nothing at limit=1 and two rows
	// at limit=2, from the same account, because the element name here was
	// wrong and discovery could only rescue the plural case.
	//
	// A listing narrowed to a single match -- searching porting orders by phone
	// number, say -- is one of the most common things anybody asks for, and
	// reporting it as empty is the failure this file already warns about
	// elsewhere: an operator told nothing is there believes it.
	if items = discoverSingle(rec); len(items) > 0 {
		return items, ""
	}
	if hasContent(rec) {
		return nil, "the response carried data this integration did not " +
			"recognise as a list, so the count above is not a count of what " +
			"exists — treat it as unknown rather than none"
	}
	return nil, ""
}

// discoverList finds a repeated element anywhere in a decoded response.
//
// Bandwidth's Dashboard wraps collections differently per endpoint -- some in a
// plural container, some directly under the response -- and there are dozens of
// endpoints. Naming every shape correctly is a guess repeated dozens of times,
// and a wrong guess is silent. So the names are a preference and this is the
// floor: whatever the wrapper is called, a list is a repeated element, and that
// is findable without knowing its name.
func discoverList(node any) []Record {
	rec, ok := node.(Record)
	if !ok {
		return nil
	}
	// Widest first: a slice at this level is the collection, and descending
	// before checking would find a repeated field inside the first row
	// instead.
	for _, key := range sortedKeys(rec) {
		if list, ok := rec[key].([]any); ok {
			out := make([]Record, 0, len(list))
			for _, item := range list {
				if r, ok := item.(Record); ok {
					out = append(out, r)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	for _, key := range sortedKeys(rec) {
		if found := discoverList(rec[key]); len(found) > 0 {
			return found
		}
	}
	return nil
}

// discoverSingle finds a collection that happens to hold one row.
//
// Deliberately narrow, because the shape it looks for is ambiguous by nature: a
// lone nested record could be the single row of a listing, or it could be a
// detail response that was never a listing at all. So it only fires after both
// named lookup and repeated-element discovery have failed, and only when the
// response carries exactly one candidate -- ignoring the bookkeeping every
// Dashboard answer wraps its payload in.
//
// One level of descent, then stop. Deeper recursion would eventually find some
// nested object in any response and call it a row, which is how a heuristic
// stops being one.
func discoverSingle(node any) []Record {
	rec, ok := node.(Record)
	if !ok {
		return nil
	}
	var found []Record
	for _, key := range sortedKeys(rec) {
		switch key {
		case "ResultCount", "TotalCount", "Links", "Link", "#text":
			continue
		}
		inner, ok := rec[key].(Record)
		if !ok {
			continue
		}
		found = append(found, inner)
		if len(found) > 1 {
			// More than one candidate and no repetition to disambiguate them.
			// Returning either would be a guess, and a wrong row is worse than
			// the caller being told the shape was not recognised.
			return nil
		}
	}
	if len(found) != 1 {
		return nil
	}
	// The single candidate may be the row, or a container holding it. One more
	// look, and no further.
	if inner := onlyRecord(found[0]); inner != nil {
		return []Record{inner}
	}
	return found
}

// onlyRecord returns the sole nested record of rec, when rec is a container
// carrying exactly one and nothing else of its own.
func onlyRecord(rec Record) Record {
	var found Record
	for _, key := range sortedKeys(rec) {
		switch key {
		case "ResultCount", "TotalCount", "Links", "Link", "#text":
			continue
		}
		if inner, ok := rec[key].(Record); ok {
			if found != nil {
				return nil
			}
			found = inner
			continue
		}
		// A scalar of its own means this is the row, not a wrapper around one.
		return nil
	}
	return found
}

// hasContent reports whether a decoded response carried anything beyond the
// bookkeeping every Dashboard answer has.
func hasContent(rec Record) bool {
	for key, value := range rec {
		switch key {
		case "ResultCount", "TotalCount", "Links", "Link", "#text":
			continue
		}
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return true
			}
		case nil:
		default:
			return true
		}
	}
	return false
}

func sortedKeys(rec Record) []string {
	out := make([]string, 0, len(rec))
	for k := range rec {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func listOf(rec Record, container, element string) []Record {
	node := any(rec)
	if container != "" {
		inner, ok := rec[container]
		if !ok {
			return nil
		}
		node = inner
	}

	holder, ok := node.(Record)
	if !ok {
		return nil
	}
	raw, ok := holder[element]
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case []any:
		out := make([]Record, 0, len(v))
		for _, item := range v {
			if rec, ok := item.(Record); ok {
				out = append(out, rec)
			} else {
				// A repeated text-only element, such as a bare list of
				// numbers. Wrapped so the caller still gets records.
				out = append(out, Record{element: item})
			}
		}
		return out
	case Record:
		return []Record{v}
	default:
		return []Record{{element: v}}
	}
}

// text reads a string field out of a decoded response, for the few places that
// need one value rather than the whole record.
func text(rec Record, key string) string {
	if rec == nil {
		return ""
	}
	if s, ok := rec[key].(string); ok {
		return s
	}
	return ""
}

// setPage writes the Dashboard's paging pair.
//
// Both, always. The Dashboard refuses a size without a page -- "Page parameter
// is required", in as many words, on /orders -- and answers the same complaint
// elsewhere as a bare 404, which reads as an empty collection and is not one.
// Sending page 1 by default costs nothing and removes a whole class of failure
// that does not look like a failure.
func setPage(q url.Values, page, size int) {
	if page <= 0 {
		page = 1
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("size", strconv.Itoa(size))
}
