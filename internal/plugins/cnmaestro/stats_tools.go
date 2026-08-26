package cnmaestro

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// How devices are actually performing, as opposed to whether they are up.
//
// deviceTypes is what the API accepts for a device type filter. Checked here
// because a wrong one returns an empty list rather than an error, which reads
// as "there are none of those" instead of "that is not a type".
var deviceTypes = []string{
	"cnmatrix", "cnreach", "epmp", "pmp", "ptp", "wifi-enterprise",
	"wifi-home", "wifi-xirrus", "cnwave60", "cnvision", "nse",
}

func (p *Plugin) registerStatsTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_device_statistics",
		Title: "List device statistics",
		Description: "Current counters for devices across the estate: throughput, " +
			"client counts, uptime, radio state. This is the fleet-wide view; " +
			"for one device use cnmaestro_get_device_statistics, and for how a " +
			"device behaved over time use cnmaestro_get_device_performance.",
		Idempotent: true,
	}, p.listDeviceStatistics)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_device_statistics",
		Title: "Get one device's statistics",
		Description: "Current counters for a single device by MAC address. Use " +
			"this after cnmaestro_list_devices has narrowed to one.",
		Idempotent: true,
	}, p.getDeviceStatistics)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_device_performance",
		Title: "Get a device's performance over time",
		Description: "A time series for one device -- throughput, signal, " +
			"client counts -- between two timestamps. Both times are required " +
			"and are ISO 8601. Use this to answer whether something got worse, " +
			"which a current-counter reading cannot say.",
		Idempotent: true,
	}, p.getDevicePerformance)
}

// StatisticsInput filters the fleet-wide statistics listing.
type StatisticsInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name from cnmaestro_list_managed_accounts, or Base Infrastructure for the main account; omit to use the configured default"`
	Type    string `json:"type,omitempty" jsonschema:"limit to one device type, such as cnmatrix, epmp, pmp, ptp, wifi-enterprise, cnwave60, or nse"`
	Network string `json:"network,omitempty" jsonschema:"limit to one network, by name"`
	Tower   string `json:"tower,omitempty" jsonschema:"limit to one tower, by name"`
	Site    string `json:"site,omitempty" jsonschema:"limit to one site, by name"`
}

// StatisticsOutput is a list of per-device statistics.
type StatisticsOutput struct {
	Statistics []Record `json:"statistics"`
	Count      int      `json:"count"`
	Total      int      `json:"total,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	Account    string   `json:"account,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (p *Plugin) listDeviceStatistics(ctx context.Context, in StatisticsInput) (StatisticsOutput, error) {
	deviceType, err := oneOf("type", in.Type, deviceTypes...)
	if err != nil {
		return StatisticsOutput{}, err
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	setIf(params, "type", deviceType)
	setIf(params, "network", in.Network)
	setIf(params, "tower", in.Tower)
	setIf(params, "site", in.Site)

	page, note, err := p.collect(ctx, "/devices/statistics", params, account,
		placeScope(in.Network, in.Tower, in.Site), "devices",
		"network, tower, site, or device type")
	if err != nil {
		return StatisticsOutput{}, err
	}
	return StatisticsOutput{
		Statistics: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// DeviceStatisticsInput names one device.
type DeviceStatisticsInput struct {
	MAC     string `json:"mac" jsonschema:"the device's MAC address, with or without separators"`
	Account string `json:"account,omitempty" jsonschema:"which account the device belongs to; omit to use the configured default"`
}

// DeviceStatisticsOutput is one device's counters.
type DeviceStatisticsOutput struct {
	Statistics Record   `json:"statistics"`
	Warnings   []string `json:"warnings,omitempty"`
	Account    string   `json:"account,omitempty"`
}

func (p *Plugin) getDeviceStatistics(ctx context.Context, in DeviceStatisticsInput) (DeviceStatisticsOutput, error) {
	mac, err := deviceMAC(in.MAC)
	if err != nil {
		return DeviceStatisticsOutput{}, err
	}
	account := p.cfg.Account(in.Account)

	// A single-element array rather than an object, the same shape /devices
	// returns for one device.
	var records []Record
	warnings, err := p.client.Get(ctx, "/devices/"+url.PathEscape(mac)+"/statistics",
		accountParams(account), &records)
	p.note(err)
	if err != nil {
		return DeviceStatisticsOutput{}, err
	}
	if len(records) == 0 {
		return DeviceStatisticsOutput{}, fmt.Errorf(
			"cnmaestro: no statistics for MAC %s. The device may belong to "+
				"another account than the one this read", mac)
	}
	return DeviceStatisticsOutput{
		Statistics: records[0], Warnings: warnings, Account: account,
	}, nil
}

// DevicePerformanceInput names one device and a period.
type DevicePerformanceInput struct {
	MAC       string `json:"mac" jsonschema:"the device's MAC address, with or without separators"`
	StartTime string `json:"start_time" jsonschema:"start of the period, ISO 8601 such as 2026-08-01T00:00:00Z"`
	StopTime  string `json:"stop_time" jsonschema:"end of the period, ISO 8601"`
	Account   string `json:"account,omitempty" jsonschema:"which account the device belongs to; omit to use the configured default"`
}

// DevicePerformanceOutput is a time series for one device.
type DevicePerformanceOutput struct {
	Samples   []Record `json:"samples"`
	Count     int      `json:"count"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) getDevicePerformance(ctx context.Context, in DevicePerformanceInput) (DevicePerformanceOutput, error) {
	mac, err := deviceMAC(in.MAC)
	if err != nil {
		return DevicePerformanceOutput{}, err
	}
	// Required by the API rather than defaulted. Saying so here is the
	// difference between a clear message and a 400 that names a parameter the
	// caller never saw.
	start, err := requiredISOTime("start_time", in.StartTime)
	if err != nil {
		return DevicePerformanceOutput{}, err
	}
	stop, err := requiredISOTime("stop_time", in.StopTime)
	if err != nil {
		return DevicePerformanceOutput{}, err
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	params.Set("start_time", start)
	params.Set("stop_time", stop)

	page, note, err := p.collect(ctx, "/devices/"+url.PathEscape(mac)+"/performance",
		params, account, scopeDevice, "samples", "a shorter time range")
	if err != nil {
		return DevicePerformanceOutput{}, err
	}
	return DevicePerformanceOutput{
		Samples: page.Items, Count: len(page.Items),
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}
