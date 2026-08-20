package cnmaestro

import "encoding/json"

// The models here mirror the typed response schemas the 6.3.0 specification
// introduced. Fields are pointers or omitempty where the API may omit them:
// cnMaestro returns different shapes per device type, and a zero value that
// looks like real data is worse than an absent one.

// Device is the normalised view of any managed device.
type Device struct {
	MAC              string `json:"mac"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Product          string `json:"product,omitempty"`
	Status           string `json:"status,omitempty"`
	IP               string `json:"ip,omitempty"`
	Network          string `json:"network,omitempty"`
	Site             string `json:"site,omitempty"`
	Tower            string `json:"tower,omitempty"`
	SoftwareVersion  string `json:"software_version,omitempty"`
	HardwareVersion  string `json:"hardware_version,omitempty"`
	MSN              string `json:"msn,omitempty"`
	Description      string `json:"description,omitempty"`
	APGroup          string `json:"ap_group,omitempty"`
	ManagedAccount   string `json:"managed_account,omitempty"`
	RegistrationDate string `json:"registration_date,omitempty"`
	LastRebootReason string `json:"last_reboot_reason,omitempty"`

	// Overrides holds device-level configuration overrides. It is kept raw
	// because its shape varies by device type and a mutation needs to echo
	// back exactly what it read.
	Overrides json.RawMessage `json:"overrides,omitempty"`
}

// DeviceStatistics is the operational state of a device, as distinct from its
// configuration.
type DeviceStatistics struct {
	MAC    string `json:"mac"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	// Uptime is seconds since boot. A reboot is verified by watching this
	// fall, which is the only observable signal the reboot endpoint gives.
	Uptime          *int64   `json:"uptime,omitempty"`
	CPUPercent      *float64 `json:"cpu,omitempty"`
	MemoryPercent   *float64 `json:"memory,omitempty"`
	ClientCount     *int     `json:"connected_clients,omitempty"`
	SoftwareVersion string   `json:"software_version,omitempty"`
	IP              string   `json:"ip,omitempty"`

	// Radios reports per-radio operating state. This is where the *operating*
	// channel appears, as distinct from the configured override.
	Radios []RadioStatistics `json:"radio,omitempty"`

	// Raw preserves the full payload so a tool can surface fields these
	// structs do not model. cnMaestro returns different shapes per device
	// type, and hard-coding a subset would silently drop the rest.
	Raw json.RawMessage `json:"-"`
}

// RadioStatistics is one radio's operating state.
type RadioStatistics struct {
	Band         string   `json:"band,omitempty"`
	Channel      *int     `json:"channel,omitempty"`
	ChannelWidth *int     `json:"channel_width,omitempty"`
	Power        *float64 `json:"power,omitempty"`
	NoiseFloor   *float64 `json:"noise_floor,omitempty"`
	ClientCount  *int     `json:"num_clients,omitempty"`
	AirtimeUsage *float64 `json:"airtime_utilization,omitempty"`
	TxBytes      *int64   `json:"tx_bytes,omitempty"`
	RxBytes      *int64   `json:"rx_bytes,omitempty"`
}

// WirelessClient is a connected wireless client. It is not named Client to
// leave that name for the API client itself, which callers reach for far more
// often.
type WirelessClient struct {
	MAC          string   `json:"mac"`
	Name         string   `json:"name,omitempty"`
	IP           string   `json:"ip,omitempty"`
	APMAC        string   `json:"ap_mac,omitempty"`
	APName       string   `json:"ap_name,omitempty"`
	SSID         string   `json:"ssid,omitempty"`
	Band         string   `json:"band,omitempty"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	OS           string   `json:"os,omitempty"`
	RSSI         *float64 `json:"rssi,omitempty"`
	SNR          *float64 `json:"snr,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Site         string   `json:"site,omitempty"`
	Network      string   `json:"network,omitempty"`
	LastSeen     string   `json:"last_seen,omitempty"`
}

// Alarm is an active or historical alarm.
type Alarm struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Status      string `json:"status,omitempty"`
	Source      string `json:"source,omitempty"`
	SourceMAC   string `json:"source_mac,omitempty"`
	Message     string `json:"message,omitempty"`
	Network     string `json:"network,omitempty"`
	Site        string `json:"site,omitempty"`
	RaisedTime  string `json:"raised_time,omitempty"`
	ClearedTime string `json:"cleared_time,omitempty"`
}

// Event is a logged occurrence.
type Event struct {
	Name      string `json:"name,omitempty"`
	Message   string `json:"message,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Source    string `json:"source,omitempty"`
	SourceMAC string `json:"source_mac,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Network   string `json:"network,omitempty"`
	Site      string `json:"site,omitempty"`
}

// Network is a top-level grouping. Note that networks, sites and towers are
// addressed by NAME in 6.3.0, not by an immutable id.
type Network struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// Site is a location within a network.
type Site struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Network string `json:"network,omitempty"`
	Country string `json:"country,omitempty"`
}

// APGroup is a configuration group for Enterprise Wi-Fi APs.
type APGroup struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// WLAN is a wireless network definition.
type WLAN struct {
	Name        string `json:"name"`
	SSID        string `json:"ssid,omitempty"`
	Description string `json:"description,omitempty"`
	Security    string `json:"security,omitempty"`
	VLAN        *int   `json:"vlan,omitempty"`
}

// Job is an asynchronous operation such as a software update.
type Job struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_time,omitempty"`
	UpdatedAt string `json:"updated_time,omitempty"`
}

// RadioOverride is one entry in a device's overrides.radios array.
//
// Two details matter and both differ from what an integration would naturally
// assume. channel and power are STRING enums, not integers -- "36", "149",
// "auto". And the id alone is ambiguous: 2.4 GHz is id 1, 5 GHz is 1-2, and
// 6 GHz is 2-3, so a radio must be identified by band as well.
type RadioOverride struct {
	ID           int    `json:"id"`
	Channel      string `json:"channel,omitempty"`
	ChannelWidth *int   `json:"channel_width,omitempty"`
	Power        string `json:"power,omitempty"`
	Shutdown     *bool  `json:"shutdown,omitempty"`
}

// DeviceOverrides is the overrides object on a device.
//
// Everything beyond radios is preserved verbatim in Extra so that a mutation
// touching one radio cannot discard the rest. Whether the API merges or
// replaces this object is undocumented, so the client always sends back
// everything it read.
type DeviceOverrides struct {
	Radios []RadioOverride            `json:"radios,omitempty"`
	Extra  map[string]json.RawMessage `json:"-"`
}

// Band names the radio bands cnMaestro exposes.
type Band string

const (
	Band24 Band = "2.4GHz"
	Band5  Band = "5GHz"
	Band6  Band = "6GHz"
)

// RadioIDsForBand returns the radio ids valid for a band.
//
// The ranges overlap, which is precisely why a mutation must specify a band:
// "radio 2" means the second 5 GHz radio on one AP and the first 6 GHz radio
// on another.
func RadioIDsForBand(b Band) []int {
	switch b {
	case Band24:
		return []int{1}
	case Band5:
		return []int{1, 2}
	case Band6:
		return []int{2, 3}
	default:
		return nil
	}
}

// ValidChannels returns the channels cnMaestro accepts for a band, as the
// string values the API requires.
func ValidChannels(b Band) []string {
	switch b {
	case Band24:
		return []string{"auto", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"}
	case Band5:
		return []string{"auto", "36", "40", "44", "48", "52", "56", "60", "64",
			"100", "104", "108", "112", "116", "132", "136", "140", "144",
			"149", "153", "157", "161", "165"}
	case Band6:
		return []string{"auto", "1", "5", "9", "13", "17", "21", "25", "29", "33",
			"37", "41", "45", "49", "53", "57", "61", "65", "69", "73", "77", "81", "85"}
	default:
		return nil
	}
}

// ValidChannel reports whether a channel is accepted for a band.
func ValidChannel(b Band, channel string) bool {
	for _, c := range ValidChannels(b) {
		if c == channel {
			return true
		}
	}
	return false
}

// ValidBand reports whether b is a band this plugin understands.
func ValidBand(b Band) bool {
	switch b {
	case Band24, Band5, Band6:
		return true
	}
	return false
}
