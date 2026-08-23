package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
)

// InlinePolicy decides how a change may be approved in the conversation.
//
// It is a ceiling on the *shortcut*, not on the decision. A routine change can
// be approved from a single yes/no prompt, which is what stops the gate being
// worked around by people who would otherwise stop using it. Above the
// ceiling that prompt is withheld: the model has to show the change in full
// and be told explicitly before calling approve_operation.
//
// Either way the person decides in the conversation. Sending them somewhere
// else to approve is how a gate turns into an obstacle, and an obstacle is
// what people route around.
type InlinePolicy interface {
	// AllowsInline reports whether a risk level may be approved from a single
	// yes/no prompt, rather than needing the change shown in full first.
	AllowsInline(risk operations.RiskLevel) bool
}

// ApprovalService is the subset of the operations service the tools need.
type ApprovalService interface {
	Propose(ctx context.Context, p *auth.Principal, req operations.ProposeRequest) (*operations.Operation, error)
	Approve(ctx context.Context, p *auth.Principal, operationID, reason string) (*operations.Operation, error)
	Reject(ctx context.Context, p *auth.Principal, operationID, reason string) (*operations.Operation, error)
	Cancel(ctx context.Context, p *auth.Principal, operationID, reason string) (*operations.Operation, error)
	Get(ctx context.Context, p *auth.Principal, operationID string) (*operations.Operation, error)
	// ApproveInline records an approval given through a client's own
	// confirmation prompt. It is a separate method so the audit trail can say
	// which it was: a prompt the client raised and a deliberate call after
	// seeing the change carry different evidence about who decided.
	ApproveInline(ctx context.Context, p *auth.Principal, operationID string) (*operations.Operation, error)
	// AwaitOutcome waits for an approved operation to finish executing, so an
	// inline approval can report what actually happened rather than leaving
	// the user to ask.
	AwaitOutcome(ctx context.Context, operationID string, timeout time.Duration) (*operations.Operation, error)
	List(ctx context.Context, p *auth.Principal, plugin string, states []operations.OperationState, limit int) ([]*operations.Operation, error)
}

// operationView is what a model sees. It deliberately omits the payload hash,
// lease details, and correlation ID: none of them help a model decide, and all
// of them invite it to reason about internals it must not depend on.
type operationView struct {
	OperationID string              `json:"operation_id"`
	State       string              `json:"state"`
	Plugin      string              `json:"plugin"`
	Action      string              `json:"action"`
	Risk        string              `json:"risk"`
	Impact      string              `json:"impact"`
	Changes     []operations.Change `json:"changes,omitempty"`
	// Target and Observed are decoded rather than raw. json.RawMessage is a
	// []byte, and the SDK infers a byte array as an array-or-null schema,
	// which fails validation against the object these actually contain.
	Target      any       `json:"target,omitempty"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ApprovedBy  string    `json:"approved_by,omitempty"`
	// AuthorizedByRule names the standing rule that authorised this change
	// when nobody was asked. Its presence is the difference between "a person
	// said yes" and "an administrator said in advance that this class of
	// change does not need asking about", and a model that reports the second
	// as the first is telling somebody something untrue.
	AuthorizedByRule string     `json:"authorized_by_rule,omitempty"`
	ExecuteBy        *time.Time `json:"execute_by,omitempty"`
	Verified         *bool      `json:"verified,omitempty"`
	Observed         any        `json:"observed_state,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	ErrorDetail      string     `json:"error_detail,omitempty"`
	// Assurance says what this record can prove: "reviewed_change" when the
	// exact fields were planned, drift is detectable and the outcome is
	// confirmed by re-reading, "gated_call" when it was authorised and made
	// and that is the whole of the evidence.
	//
	// It says nothing about who authorised it. AuthorizedByRule below is that
	// fact, and the two are independent: a change nobody was asked about can
	// still carry every proof, and one a person approved can carry none.
	Assurance string `json:"assurance"`
	// Note tells the model, in the response itself, what has and has not
	// happened. A model that reads only the state string can still misread
	// "approved" as "done"; this leaves no room for that.
	Note string `json:"note"`
}

func viewOf(op *operations.Operation) operationView {
	return operationView{
		OperationID:      op.ID,
		State:            op.State.String(),
		Plugin:           op.Plugin,
		Action:           op.Action,
		Risk:             op.Risk.String(),
		Impact:           op.Impact,
		Changes:          op.Changes,
		Target:           decodeJSON(op.Target),
		RequestedBy:      op.RequestedBy,
		RequestedAt:      op.RequestedAt,
		ExpiresAt:        op.ExpiresAt,
		ApprovedBy:       op.ApprovedBy,
		AuthorizedByRule: op.AuthorizedByRule,
		ExecuteBy:        op.ApprovalExpiresAt,
		Verified:         op.OutcomeVerified,
		Observed:         decodeJSON(op.Observed),
		ErrorCode:        op.ErrorCode,
		ErrorDetail:      op.ErrorDetail,
		Assurance:        op.Assurance().String(),
		Note:             noteFor(op),
	}
}

// decodeJSON turns stored JSON into a value the SDK can describe in a schema.
// A payload that fails to decode is reported as such rather than dropped: a
// silently missing target would let a model reason about a change it cannot
// see.
func decodeJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]string{"error": "stored value could not be decoded"}
	}
	return v
}

// noteFor tells the model, in the response itself, what has and has not
// happened -- and, where nobody was asked, that nobody was asked.
func noteFor(op *operations.Operation) string {
	return stateNote(op) + authorisationNote(op)
}

// authorisationNote says, in the note itself, that nobody was asked.
//
// The field beside it carries the same fact for anything that parses the
// response, but the note is what a model repeats to a person, and "this was
// approved" read out about a change no human ever saw is a claim about a
// decision that was not made. Naming the rule is what turns it back into
// something true: somebody did authorise this, in advance, and here is what
// they wrote.
func authorisationNote(op *operations.Operation) string {
	if !op.AutoApproved() {
		return ""
	}
	return " Nobody was asked about this change: it was authorised in advance " +
		"by the standing rule \"" + op.AuthorizedByRule + "\". Say so rather than " +
		"describing it as approved by a person."
}

// stateNote states plainly what the current state means, and how much of it is
// actually known.
//
// The succeeded case used to say "Applied and confirmed by re-reading the
// target" for every operation, including ones where nothing had been compared
// against anything. A note that overstates the evidence is worse than none:
// the model repeats it to a person, who now believes a check happened.
func stateNote(op *operations.Operation) string {
	switch op.State {
	case operations.StatePendingApproval:
		return "NOTHING HAS CHANGED YET. This is a proposal awaiting human approval. " +
			"It will expire at the time shown unless someone approves it." +
			assuranceCaveat(op)
	case operations.StateApproved:
		return "Approved but not yet applied. It will be executed shortly, " +
			"or expire at execute_by if it cannot be." + assuranceCaveat(op)
	case operations.StateExecuting:
		return "Currently being applied."
	case operations.StateSucceeded:
		// Three-valued in the reader as well as the writer. Branching on the
		// pointer alone would report a pointer to false -- "re-read, and it
		// did not match" -- as a confirmation. Nothing produces that pairing
		// today, because the executor writes false only alongside
		// indeterminate, but the state machine permits indeterminate ->
		// succeeded under TriggerReconcile and no transition clears the
		// column, so the first reconciler to arrive would land here.
		switch {
		case op.OutcomeVerified == nil:
			return "Applied. The upstream call reported success, but this change " +
				"cannot be confirmed by re-reading the target, so nothing here " +
				"proves what the result is. Say so rather than reporting it as verified."
		case !*op.OutcomeVerified:
			return "Recorded as applied, but the target was re-read and did not " +
				"match what was requested. Do not report this as done; a human " +
				"has to look at the target."
		}
		return "Applied and confirmed by re-reading the target."
	case operations.StateFailed:
		return "Not applied. The target was left unchanged."
	case operations.StateIndeterminate:
		return "UNKNOWN OUTCOME. The change may or may not have been applied. " +
			"Do not retry it. A human must check the device and resolve this."
	case operations.StateRejected:
		return "Rejected by an approver. Nothing was changed."
	case operations.StateExpired:
		return "Expired before it could be applied. Nothing was changed."
	case operations.StateCancelled:
		return "Withdrawn. Nothing was changed."
	default:
		return ""
	}
}

// assuranceCaveat names the proofs a not-yet-executed operation will not be
// able to offer, so a person deciding is told what approving it does and does
// not buy them.
//
// Silence would be the wrong default. A gated call and a reviewed change reach
// the approver through the same tool, wearing the same word, and the one that
// carries less evidence is exactly the one that needs to say so.
func assuranceCaveat(op *operations.Operation) string {
	if op.Assurance() == operations.AssuranceReviewedChange {
		return ""
	}
	var missing []string
	if !op.DriftChecked() {
		missing = append(missing, "it declares no preconditions, so a change made "+
			"to the target between now and execution will not be noticed")
	}
	if !op.Verifiable {
		missing = append(missing, "its outcome cannot be confirmed by re-reading "+
			"the target, so success will mean the call was accepted and no more")
	}
	return " This is a gated call rather than a reviewed change: " +
		strings.Join(missing, ", and ") + "."
}

// attachApprovalTools adds the operation lifecycle tools to a plugin endpoint.
//
// They live on each plugin's own endpoint rather than on a shared one so that
// a principal scoped to a single plugin can approve that plugin's operations
// without gaining any visibility into another's.
func attachApprovalTools(srv *mcp.Server, plugin string, svc ApprovalService, gate ToolMiddleware) {
	type getArgs struct {
		OperationID string `json:"operation_id" jsonschema:"the operation to look up"`
	}
	type decideArgs struct {
		OperationID string `json:"operation_id" jsonschema:"the operation to act on"`
		Reason      string `json:"reason,omitempty" jsonschema:"why this decision was made; recorded in the audit trail"`
	}
	type listArgs struct {
		State string `json:"state,omitempty" jsonschema:"filter by state, e.g. pending_approval"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 20"`
	}
	type listResult struct {
		Operations []operationView `json:"operations"`
		Count      int             `json:"count"`
	}

	readOnly, mutating := true, false

	mcp.AddTool(srv, &mcp.Tool{
		Name: plugin + "_list_operations",
		Description: "List proposed and completed changes for " + plugin + ". " +
			"Use this to find the operation_id of a change awaiting approval.",
		Annotations: &mcp.ToolAnnotations{
			Title: "List operations", ReadOnlyHint: readOnly, IdempotentHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listArgs) (*mcp.CallToolResult, listResult, error) {
		if err := gate(ctx, plugin+"_list_operations", auth.CapRead); err != nil {
			return nil, listResult{}, err
		}
		var states []operations.OperationState
		if in.State != "" {
			st := operations.OperationState(in.State)
			if !st.Valid() {
				return nil, listResult{}, fmt.Errorf("unknown state %q", in.State)
			}
			states = []operations.OperationState{st}
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		ops, err := svc.List(ctx, auth.FromContext(ctx), plugin, states, limit)
		if err != nil {
			return nil, listResult{}, err
		}
		views := make([]operationView, len(ops))
		for i, op := range ops {
			views[i] = viewOf(op)
		}
		return nil, listResult{Operations: views, Count: len(views)}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        plugin + "_get_operation",
		Description: "Get the full detail and current state of one " + plugin + " operation.",
		Annotations: &mcp.ToolAnnotations{
			Title: "Get operation", ReadOnlyHint: readOnly, IdempotentHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getArgs) (*mcp.CallToolResult, operationView, error) {
		if err := gate(ctx, plugin+"_get_operation", auth.CapRead); err != nil {
			return nil, operationView{}, err
		}
		op, err := svc.Get(ctx, auth.FromContext(ctx), in.OperationID)
		if err != nil {
			return nil, operationView{}, err
		}
		return nil, viewOf(op), nil
	})

	// Approving is the one call that lets a change reach live infrastructure,
	// so it is the one a client should put in front of a person first.
	// destructiveHint is how a client is told to do that, and it was false
	// here -- the same variable supplied ReadOnlyHint, where false is correct,
	// and DestructiveHint, where it asked every client not to bother
	// confirming the most consequential call in the API.
	//
	// The hint does not enforce anything; the approval row in SQLite does. It
	// decides whether ChatGPT frames this as a confirmation or fires it like a
	// getter, which is the difference between a human seeing the change and
	// not.
	destructive, reachesUpstream := true, true

	mcp.AddTool(srv, &mcp.Tool{
		Name: plugin + "_approve_operation",
		Description: "Approve a pending " + plugin + " change so it can be applied. " +
			"This authorises the change exactly as it was proposed; the stored " +
			"parameters cannot be altered here. Only a human should decide to call this.",
		Annotations: &mcp.ToolAnnotations{
			Title: "Approve a proposed change", ReadOnlyHint: mutating,
			DestructiveHint: &destructive, IdempotentHint: false,
			OpenWorldHint: &reachesUpstream,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in decideArgs) (*mcp.CallToolResult, operationView, error) {
		if err := gate(ctx, plugin+"_approve_operation", auth.CapApprove); err != nil {
			return nil, operationView{}, err
		}
		op, err := svc.Approve(ctx, auth.FromContext(ctx), in.OperationID, in.Reason)
		if err != nil {
			return nil, operationView{}, err
		}
		return nil, viewOf(op), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        plugin + "_reject_operation",
		Description: "Reject a pending " + plugin + " change. Nothing is applied.",
		Annotations: &mcp.ToolAnnotations{
			Title: "Reject a proposed change", ReadOnlyHint: mutating,
			DestructiveHint: &mutating,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in decideArgs) (*mcp.CallToolResult, operationView, error) {
		if err := gate(ctx, plugin+"_reject_operation", auth.CapApprove); err != nil {
			return nil, operationView{}, err
		}
		op, err := svc.Reject(ctx, auth.FromContext(ctx), in.OperationID, in.Reason)
		if err != nil {
			return nil, operationView{}, err
		}
		return nil, viewOf(op), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        plugin + "_cancel_operation",
		Description: "Withdraw a " + plugin + " change you proposed. Nothing is applied.",
		Annotations: &mcp.ToolAnnotations{
			Title: "Cancel a proposed change", ReadOnlyHint: mutating,
			DestructiveHint: &mutating,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in decideArgs) (*mcp.CallToolResult, operationView, error) {
		if err := gate(ctx, plugin+"_cancel_operation", auth.CapPropose); err != nil {
			return nil, operationView{}, err
		}
		op, err := svc.Cancel(ctx, auth.FromContext(ctx), in.OperationID, in.Reason)
		if err != nil {
			return nil, operationView{}, err
		}
		return nil, viewOf(op), nil
	})
}
