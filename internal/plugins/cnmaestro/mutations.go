package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// registerMutations declares every approval-gated write.
//
// The set is deliberately small. Each entry is an allow-listed, strongly typed
// operation with a verifiable outcome; there is no generic configuration
// endpoint and no way to reach one.
func (p *Plugin) registerMutations(r *plugins.Registry) {
	plugins.Mutation(r, plugins.MutationSpec{
		Action: "device.set_radio_channel",
		Title:  "Change a radio channel",
		Description: "Change the channel a radio operates on. Clients associated with " +
			"that radio will briefly disconnect while it moves.",
		Risk:       operations.RiskMedium,
		Reversible: true,
	}, &setRadioChannel{p: p})

	plugins.Mutation(r, plugins.MutationSpec{
		Action: "device.reboot",
		Title:  "Reboot a device",
		Description: "Reboot a device. It will be offline for one to several minutes " +
			"and every client on it will disconnect.",
		Risk: operations.RiskMedium,
		// A reboot cannot be undone, only repeated.
		Reversible: false,
	}, &rebootDevice{p: p})
}

// --- radio channel ---------------------------------------------------------

// SetRadioChannelParams is the typed parameter set.
//
// Band is required alongside RadioID because the ids overlap: 2.4 GHz is id 1,
// 5 GHz is 1-2 and 6 GHz is 2-3, so "radio 2" alone names a different radio
// depending on the access point.
//
// Channel is a string, not an integer, because the API defines it as a string
// enum whose values include "auto".
type SetRadioChannelParams struct {
	MAC     string `json:"mac" jsonschema:"the access point MAC address"`
	Band    string `json:"band" jsonschema:"which radio band: 2.4GHz, 5GHz or 6GHz"`
	RadioID int    `json:"radio_id" jsonschema:"the radio number within that band"`
	Channel string `json:"channel" jsonschema:"the target channel as a string, e.g. \"149\", or \"auto\""`
	Width   *int   `json:"channel_width,omitempty" jsonschema:"optional channel width in MHz: 20, 40, 80 or 160"`
}

// RadioState is the observable state this mutation acts on.
type RadioState struct {
	MAC     string `json:"mac"`
	Band    string `json:"band"`
	RadioID int    `json:"radio_id"`
	Channel string `json:"channel"`
	Width   *int   `json:"channel_width,omitempty"`
	// APGroup is part of the state because the API requires it alongside any
	// overrides change, and sending the wrong one moves the device between
	// groups as a side effect.
	APGroup string `json:"ap_group"`
}

type setRadioChannel struct{ p *Plugin }

// Plan reads current configuration and describes the change.
//
// It mutates nothing, and runs twice: once at proposal, and again immediately
// before Apply so the executor can compare preconditions. Because it is the
// same code both times, the diff an approver reads and the state checked at
// execution cannot drift apart.
func (h *setRadioChannel) Plan(ctx context.Context, in SetRadioChannelParams) (plugins.Plan[RadioState], error) {
	var zero plugins.Plan[RadioState]

	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return zero, err
	}
	band := Band(strings.TrimSpace(in.Band))
	if !ValidBand(band) {
		return zero, fmt.Errorf("band must be one of 2.4GHz, 5GHz or 6GHz, got %q", in.Band)
	}
	if !validRadioID(band, in.RadioID) {
		return zero, fmt.Errorf("radio_id %d is not valid for %s; valid ids are %v",
			in.RadioID, band, RadioIDsForBand(band))
	}
	if !ValidChannel(band, in.Channel) {
		return zero, fmt.Errorf("channel %q is not valid for %s; valid channels are %s",
			in.Channel, band, strings.Join(ValidChannels(band), ", "))
	}
	if in.Width != nil && !validWidth(*in.Width) {
		return zero, fmt.Errorf("channel_width %d is not valid; use 20, 40, 80 or 160", *in.Width)
	}

	var device Device
	if err := h.p.client.GetInto(ctx, "/devices/"+mac, nil, &device); err != nil {
		return zero, err
	}
	if device.APGroup == "" {
		// The API rejects an overrides change without it, and guessing would
		// relocate the device.
		return zero, fmt.Errorf(
			"device %s reports no ap_group; cnMaestro requires one to change "+
				"configuration overrides, so this change cannot be proposed", mac)
	}

	current := currentRadio(device.Overrides, in.RadioID)
	before := RadioState{
		MAC: mac, Band: string(band), RadioID: in.RadioID,
		Channel: current.Channel, Width: current.ChannelWidth,
		APGroup: device.APGroup,
	}
	if before.Channel == "" {
		before.Channel = "auto"
	}

	desired := before
	desired.Channel = in.Channel
	if in.Width != nil {
		desired.Width = in.Width
	}

	if before.Channel == desired.Channel &&
		(in.Width == nil || (before.Width != nil && *before.Width == *in.Width)) {
		return zero, fmt.Errorf("radio %d on %s is already configured for channel %s",
			in.RadioID, mac, in.Channel)
	}

	changes := []operations.Change{
		{Field: "channel", From: before.Channel, To: desired.Channel},
	}
	if in.Width != nil && !equalIntPtr(before.Width, desired.Width) {
		changes = append(changes, operations.Change{
			Field: "channel_width", From: before.Width, To: *in.Width,
		})
	}

	return plugins.Plan[RadioState]{
		Before:  before,
		Desired: desired,
		// The precondition covers the ap_group as well as the channel. If the
		// device is moved between groups after proposal, the change is no
		// longer the one that was reviewed.
		Preconditions: map[string]any{
			"channel":       before.Channel,
			"channel_width": before.Width,
			"ap_group":      device.APGroup,
		},
		Changes: changes,
		Impact: fmt.Sprintf(
			"Clients on the %s radio of %s will briefly disconnect while it moves "+
				"from channel %s to %s.",
			band, displayName(device, mac), before.Channel, desired.Channel),
		Rollback: SetRadioChannelParams{
			MAC: mac, Band: string(band), RadioID: in.RadioID,
			Channel: before.Channel, Width: before.Width,
		},
	}, nil
}

// Apply writes the override.
//
// Every override the device already had is read back and resent unchanged.
// Whether the API merges or replaces the overrides object is undocumented, and
// sending only the radio being changed would silently discard the rest if it
// replaces. Resending everything is correct under either behaviour.
func (h *setRadioChannel) Apply(ctx context.Context, in SetRadioChannelParams, plan plugins.Plan[RadioState]) (plugins.ApplyResult, error) {
	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	var device Device
	if err := h.p.client.GetInto(ctx, "/devices/"+mac, nil, &device); err != nil {
		return plugins.ApplyResult{}, err
	}

	overrides, err := mergeRadioOverride(device.Overrides, RadioOverride{
		ID:           in.RadioID,
		Channel:      in.Channel,
		ChannelWidth: in.Width,
	})
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	body := map[string]any{
		// Required whenever overrides are present. It is taken from the plan
		// rather than re-read, so a group change between plan and apply is
		// caught by the precondition check instead of being silently applied.
		"ap_group":  plan.Before.APGroup,
		"overrides": overrides,
	}

	if _, err := h.p.client.Put(ctx, "/devices/"+mac, body); err != nil {
		// A PUT that times out or returns 5xx may still have been applied.
		// Reporting it as a plain failure would invite a retry, and a retry
		// would reapply a configuration change whose first attempt may already
		// be in flight.
		if isAmbiguous(err) {
			return plugins.ApplyResult{}, fmt.Errorf(
				"the configuration change may or may not have been accepted: %w: %w",
				err, operations.ErrIndeterminate)
		}
		return plugins.ApplyResult{}, err
	}

	// cnMaestro applies overrides to the device asynchronously, so acceptance
	// is not application. Verification has to read the device back.
	return plugins.ApplyResult{UpstreamRef: mac, Async: true}, nil
}

// Observe re-reads the configured override for verification.
func (h *setRadioChannel) Observe(ctx context.Context, in SetRadioChannelParams) (RadioState, error) {
	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return RadioState{}, err
	}
	var device Device
	if err := h.p.client.GetInto(ctx, "/devices/"+mac, nil, &device); err != nil {
		return RadioState{}, err
	}
	current := currentRadio(device.Overrides, in.RadioID)
	channel := current.Channel
	if channel == "" {
		channel = "auto"
	}
	return RadioState{
		MAC: mac, Band: in.Band, RadioID: in.RadioID,
		Channel: channel, Width: current.ChannelWidth,
		APGroup: device.APGroup,
	}, nil
}

// --- reboot ----------------------------------------------------------------

// RebootParams identifies the device to restart.
type RebootParams struct {
	MAC    string `json:"mac" jsonschema:"the device MAC address"`
	Reason string `json:"reason" jsonschema:"why this device needs rebooting; recorded in the audit trail"`
}

// RebootState is what a reboot can be verified against.
//
// Uptime is the only observable signal: the endpoint takes no body, returns no
// job id, and offers no other way to tell whether it took effect.
type RebootState struct {
	MAC    string `json:"mac"`
	Status string `json:"status"`
	Uptime *int64 `json:"uptime_seconds,omitempty"`
}

type rebootDevice struct{ p *Plugin }

// Plan captures the device's current uptime.
func (h *rebootDevice) Plan(ctx context.Context, in RebootParams) (plugins.Plan[RebootState], error) {
	var zero plugins.Plan[RebootState]

	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return zero, fmt.Errorf("a reason is required; it is recorded in the audit trail")
	}

	var device Device
	if err := h.p.client.GetInto(ctx, "/devices/"+mac, nil, &device); err != nil {
		return zero, err
	}
	if !strings.EqualFold(device.Status, "online") {
		return zero, fmt.Errorf("device %s is %q; only an online device can be rebooted",
			mac, device.Status)
	}

	var stats DeviceStatistics
	if err := h.p.client.GetInto(ctx, "/devices/"+mac+"/statistics", nil, &stats); err != nil {
		return zero, err
	}

	before := RebootState{MAC: mac, Status: device.Status, Uptime: stats.Uptime}
	clients := 0
	if stats.ClientCount != nil {
		clients = *stats.ClientCount
	}

	return plugins.Plan[RebootState]{
		Before: before,
		// A reboot has no single target state; success is uptime resetting.
		// Leaving Desired empty tells the verifier there is nothing to
		// compare, and the reboot's own Observe reports what happened.
		Preconditions: map[string]any{
			"status": strings.ToLower(device.Status),
		},
		Changes: []operations.Change{
			{Field: "power_state", From: "running", To: "restarting"},
		},
		Impact: fmt.Sprintf(
			"%s will go offline for one to several minutes. %d connected client(s) "+
				"will be disconnected and will need to reassociate.",
			displayName(device, mac), clients),
	}, nil
}

// Apply issues the reboot.
func (h *rebootDevice) Apply(ctx context.Context, in RebootParams, _ plugins.Plan[RebootState]) (plugins.ApplyResult, error) {
	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	if _, err := h.p.client.Post(ctx, "/devices/"+mac+"/reboot", nil); err != nil {
		// This endpoint takes no idempotency key, so a retried reboot is a
		// second reboot. An ambiguous failure must never be reported as a
		// plain one.
		if isAmbiguous(err) {
			return plugins.ApplyResult{}, fmt.Errorf(
				"the reboot request may or may not have been delivered, and it cannot "+
					"be safely retried: %w: %w", err, operations.ErrIndeterminate)
		}
		return plugins.ApplyResult{}, err
	}
	return plugins.ApplyResult{UpstreamRef: mac, Async: true}, nil
}

// Observe reports the device's current state.
func (h *rebootDevice) Observe(ctx context.Context, in RebootParams) (RebootState, error) {
	mac, err := normalizeMAC(in.MAC)
	if err != nil {
		return RebootState{}, err
	}
	var stats DeviceStatistics
	if err := h.p.client.GetInto(ctx, "/devices/"+mac+"/statistics", nil, &stats); err != nil {
		return RebootState{}, err
	}
	return RebootState{MAC: mac, Status: stats.Status, Uptime: stats.Uptime}, nil
}

// --- helpers ---------------------------------------------------------------

// isAmbiguous reports whether a failure leaves the outcome unknown.
//
// A 4xx means the request was understood and refused, so nothing happened. A
// 5xx or a transport failure means the request may have been processed before
// the response was lost, and the caller cannot tell.
func isAmbiguous(err error) bool {
	var apiErr *APIError
	if stdErrorsAs(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	// A transport error: timeout, connection reset, DNS failure. The request
	// may have reached the controller.
	return true
}

// currentRadio finds a radio's existing override, returning a zero value when
// the device has none.
func currentRadio(raw json.RawMessage, id int) RadioOverride {
	if len(raw) == 0 {
		return RadioOverride{ID: id}
	}
	var overrides struct {
		Radios []RadioOverride `json:"radios"`
	}
	if err := json.Unmarshal(raw, &overrides); err != nil {
		return RadioOverride{ID: id}
	}
	for _, r := range overrides.Radios {
		if r.ID == id {
			return r
		}
	}
	return RadioOverride{ID: id}
}

// mergeRadioOverride returns the device's overrides with one radio replaced
// and everything else preserved byte for byte.
func mergeRadioOverride(raw json.RawMessage, update RadioOverride) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("cnmaestro: existing device overrides could not be read, "+
				"so they cannot be preserved: %w", err)
		}
	}

	var radios []RadioOverride
	if existing, ok := out["radios"]; ok {
		if err := json.Unmarshal(existing, &radios); err != nil {
			return nil, fmt.Errorf("cnmaestro: existing radio overrides could not be read: %w", err)
		}
	}

	replaced := false
	for i, r := range radios {
		if r.ID != update.ID {
			continue
		}
		// Preserve fields this mutation does not touch, so changing a channel
		// cannot clear a power setting.
		merged := r
		merged.Channel = update.Channel
		if update.ChannelWidth != nil {
			merged.ChannelWidth = update.ChannelWidth
		}
		radios[i] = merged
		replaced = true
		break
	}
	if !replaced {
		radios = append(radios, update)
	}

	encoded, err := json.Marshal(radios)
	if err != nil {
		return nil, err
	}
	out["radios"] = encoded
	return out, nil
}

func validRadioID(b Band, id int) bool {
	for _, valid := range RadioIDsForBand(b) {
		if valid == id {
			return true
		}
	}
	return false
}

func validWidth(w int) bool {
	switch w {
	case 20, 40, 80, 160:
		return true
	}
	return false
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func displayName(d Device, mac string) string {
	if d.Name != "" {
		return sanitizeText(d.Name) + " (" + mac + ")"
	}
	return mac
}
