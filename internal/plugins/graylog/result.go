package graylog

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spoked/mcpd/internal/plugins"
)

// The scripting API answers in columns:
//
//	{"schema":[{"column_type":"field","field":"source"}, …],
//	 "datarows":[["a.example",50], …],
//	 "metadata":{"effective_timerange":{…}}}
//
// This package keeps that shape all the way out to the model rather than
// zipping it into a list of objects. Fifty rows of {"timestamp":…,"source":…,
// "message":…} repeats every field name fifty times, which is the same
// information for several times the context -- and context is the budget a
// tool result actually spends. The tool descriptions say the rows are
// positional, because a model handed columns it was not told about will read
// the first row as a header.

// Two ceilings on what one answer may carry. Neither is a setting: they exist
// for the pathological row rather than for tuning, and an operator who wants a
// smaller answer has max_messages, which is the knob that means what they mean.
const (
	// maxFieldChars bounds one value. A single log line can be a megabyte of
	// stack trace, and one of those would fill a conversation on its own. Cut
	// rather than dropped: the first few thousand characters of a stack trace
	// are the ones somebody wants, and the note says a value was cut so the
	// message can be asked for on its own.
	maxFieldChars = 8000
)

// maxResultChars bounds the whole answer, across every row. It is the backstop
// for the case max_messages cannot see: fifty ordinary-looking messages that
// happen to be large.
//
// The number is the host's, not this package's. It was 120,000 here, chosen by
// eye, which is about 30,000 tokens of payload -- and because the SDK sends a
// result twice, roughly 60,000 tokens on the wire against a client that stops
// at 25,000. A search that large was going to be cut by the client, mid-JSON,
// with nothing saying what went missing. See plugins.MaxResultBytes for the
// arithmetic.
var maxResultChars = plugins.ResultBudget(1)

// columnar is the scripting API's response.
type columnar struct {
	Schema   []schemaColumn   `json:"schema"`
	Datarows [][]any          `json:"datarows"`
	Metadata columnarMetadata `json:"metadata"`
}

type schemaColumn struct {
	ColumnType string `json:"column_type"`
	Type       string `json:"type"`
	Field      string `json:"field"`
	Function   string `json:"function"`
	Name       string `json:"name"`
}

type columnarMetadata struct {
	EffectiveTimerange struct {
		From string `json:"from"`
		To   string `json:"to"`
		Type string `json:"type"`
	} `json:"effective_timerange"`
}

// tableResult is what a searching tool returns.
//
// Columns and rows rather than records, for the reason at the top of this
// file. Truncation is a field rather than a log line, because a model shown
// twenty of two thousand matches and not told so will answer as though it saw
// them all.
type tableResult struct {
	// Columns names each position in a row, in order.
	Columns []string `json:"columns"`
	// Rows are positional: rows[i][j] is the value of columns[j].
	Rows [][]any `json:"rows"`
	// Returned is len(Rows), stated because a model counting a long list gets
	// it wrong and this is the number it is usually about to reason with.
	Returned int `json:"returned"`
	// Truncated says the answer stops short of what matched, and Reason says
	// which ceiling stopped it.
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
	// ValuesCut counts the individual values shortened to maxFieldChars.
	ValuesCut int `json:"values_shortened,omitempty"`
	// Window is the range actually searched, as Graylog resolved it. Reported
	// on every call: a caller who named no window needs to know which one they
	// were given, and a keyword window is not knowable any other way.
	Window string `json:"window_searched"`
	Note   string `json:"note,omitempty"`
}

// decodeTable turns one scripting-API response into a tool result, applying
// both ceilings as it goes.
func decodeTable(raw json.RawMessage, limit int, sent timeRange) (tableResult, error) {
	var body columnar
	if err := json.Unmarshal(raw, &body); err != nil {
		return tableResult{}, fmt.Errorf("graylog: the search answered with "+
			"something that is not the API's result shape: %w", err)
	}

	out := tableResult{
		Columns: columnNames(body.Schema),
		Window:  effectiveWindow(body.Metadata, sent),
		Rows:    make([][]any, 0, len(body.Datarows)),
	}

	budget := maxResultChars
	for _, row := range body.Datarows {
		if len(out.Rows) >= limit {
			out.Truncated = true
			out.Reason = fmt.Sprintf("stopped at %d rows; ask for a narrower "+
				"query or a shorter window rather than a bigger limit", limit)
			break
		}
		trimmed, cut, size := shortenRow(row)
		if size > budget && len(out.Rows) > 0 {
			// Not applied to the first row: an answer of nothing at all,
			// because the one matching message was large, is worse than an
			// answer of one large message.
			out.Truncated = true
			out.Reason = "stopped early because the rows were large; the " +
				"answer would not have fit in a conversation"
			break
		}
		budget -= size
		out.ValuesCut += cut
		out.Rows = append(out.Rows, trimmed)
	}
	out.Returned = len(out.Rows)

	if out.ValuesCut > 0 {
		out.Note = fmt.Sprintf("%d value(s) were longer than %d characters and "+
			"have been cut short. Search for the one message you need if you "+
			"want the whole of it.", out.ValuesCut, maxFieldChars)
	}
	return out, nil
}

// columnNames labels each position in a row.
//
// A grouping and a field are named by the field they are; a metric is not --
// two metrics over the same field are different columns, so the field alone
// would name them both the same thing. Graylog's own label carries the
// function, and the "metric: " it prefixes is furniture rather than
// information.
func columnNames(schema []schemaColumn) []string {
	out := make([]string, 0, len(schema))
	for _, col := range schema {
		switch col.ColumnType {
		case "metric":
			if name := strings.TrimPrefix(col.Name, "metric: "); name != "" {
				out = append(out, name)
				continue
			}
			if col.Field != "" {
				out = append(out, col.Function+"("+col.Field+")")
				continue
			}
			out = append(out, col.Function)
		default:
			if col.Field != "" {
				out = append(out, col.Field)
				continue
			}
			// A column the API named in a way this does not recognise still
			// needs a position, and an empty label would silently shift every
			// value after it by one.
			out = append(out, strings.TrimSpace(col.Name))
		}
	}
	return out
}

// effectiveWindow prefers what Graylog said it actually searched over what was
// asked for.
//
// They differ for a keyword window, which is the case where it matters: only
// Graylog knows what "yesterday" resolved to, and a model reporting a count
// without the window it covers is reporting a number with no unit.
func effectiveWindow(meta columnarMetadata, sent timeRange) string {
	got := meta.EffectiveTimerange
	if got.From != "" && got.To != "" {
		return got.From + " to " + got.To
	}
	return sent.describe()
}

// shortenRow cuts oversized values and reports the row's size in characters.
func shortenRow(row []any) (out []any, cut int, size int) {
	out = make([]any, len(row))
	for i, value := range row {
		text, isText := value.(string)
		if !isText {
			out[i] = value
			// A number, a boolean or null. Counted as something rather than
			// nothing so a wide row of numbers still spends budget.
			size += 8
			continue
		}
		if len(text) > maxFieldChars {
			text = cutAt(text, maxFieldChars) + "…[cut]"
			cut++
		}
		out[i] = text
		size += len(text)
	}
	return out, cut, size
}

// cutAt shortens a string to at most n bytes without splitting a rune.
//
// Cutting on the byte alone leaves a partial rune, which json.Marshal replaces
// with a replacement character -- so a message in any language but English
// ends with a mojibake byte that looks like corrupted data rather than like a
// value somebody cut on purpose.
func cutAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
