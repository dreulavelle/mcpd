package supportinfo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Phone is a handset 3CX does not have a template for.
//
// It still registers and it still makes calls; what it does not get is
// provisioning, so it cannot be configured centrally and it will not pick up a
// firmware or a setting anybody changes on the PBX. On a site with a few
// hundred extensions these are the phones that quietly stay wrong.
type Phone struct {
	Extension string `json:"extension,omitempty"`
	Model     string `json:"model,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	MAC       string `json:"mac,omitempty"`
	IP        string `json:"ip,omitempty"`
}

// readPhones reads unsupportedPhones, which is a JSON array after a heading.
func readPhones(body []byte) []Phone {
	text := clean(body)
	start := strings.Index(text, "[")
	if start < 0 {
		return nil
	}
	var raw []struct {
		Extension string `json:"Extension"`
		Model     string `json:"Model"`
		Firmware  string `json:"Firmware"`
		MAC       string `json:"Mac"`
		IP        string `json:"IP"`
	}
	if err := json.Unmarshal([]byte(text[start:]), &raw); err != nil {
		return nil
	}
	out := make([]Phone, 0, len(raw))
	for _, r := range raw {
		out = append(out, Phone{
			Extension: r.Extension, Model: r.Model,
			Firmware: r.Firmware, MAC: r.MAC, IP: r.IP,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Extension < out[j].Extension })
	return out
}

// phoneFindings summarises the handsets by model, because "31 phones are
// unprovisionable" is a job and "SIP-T29G ×18, SIP-T46S ×13" is the job.
func phoneFindings(phones []Phone) []Finding {
	if len(phones) == 0 {
		return nil
	}
	models := map[string]int{}
	for _, p := range phones {
		name := p.Model
		if name == "" {
			name = "unknown model"
		}
		models[name]++
	}
	listed := counted(models, 5)
	names := make([]string, 0, len(listed))
	for _, m := range listed {
		names = append(names, fmt.Sprintf("%s ×%d", m.Name, m.Count))
	}

	evidence := make([]string, 0, maxEvidence)
	for i, p := range phones {
		if i == maxEvidence {
			break
		}
		evidence = append(evidence, fmt.Sprintf("ext %s — %s, firmware %s, %s", p.Extension, p.Model, p.Firmware, p.IP))
	}

	return []Finding{{
		Severity: Warning,
		Title:    fmt.Sprintf("%s cannot be provisioned", plural(len(phones), "phone", "phones")),
		Detail: "These handsets have no template on this phone system, so they register and make calls but " +
			"take no configuration from it. Nothing anybody changes centrally — a firmware, a codec, a " +
			"button layout — will reach them, and they have to be set up by hand. Mostly " +
			strings.Join(names, ", ") + ".",
		Evidence:    evidence,
		Occurrences: len(phones),
		Source:      "ExtraLogging/unsupportedPhones",
	}}
}

/*
readFQDN pulls the phone system's own name out of the DNS lookup 3CX runs.

This is what makes a bundle self-identifying. Every 3CX instance has an FQDN,
it is the address this integration reaches the PBX at, and it
is written into the bundle by a lookup the capture runs on itself — so a zip
somebody uploads can say which customer it belongs to without anybody choosing
from a list and without anybody choosing wrong.

The file is nslookup output, and it differs between Windows and Linux in
spacing and in whether there is a "Non-authoritative answer" line. What both
have is a "Name:" line carrying the FQDN, after the server's own address.
*/
func readFQDN(body []byte) string {
	for _, line := range strings.Split(clean(body), "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "name") {
			continue
		}
		if host := strings.TrimSpace(value); host != "" {
			return strings.ToLower(host)
		}
	}
	return ""
}
