// Command settingsdemo is a test plugin that declares settings, one of them a
// table, and reports what the host handed it.
//
// It exists to prove the path from the dashboard to a plugin process: a value
// typed on the Plugins page, or a row added to a table there, has to arrive
// in the plugin as a value. For a long time it did not -- the declaration was
// carried over the wire and then nothing on the host asked for it -- and the
// only test that can tell is one where a plugin says what it received.
package main

import (
	"context"

	"github.com/spoked/mcpd/sdk"
)

func main() {
	p := sdk.New("settingsdemo", "1.0.0", "Settings demo",
		"Reports the settings the host handed over.")

	sdk.Settings(p,
		sdk.SettingField{Key: "greeting", Label: "Greeting", Kind: sdk.KindString, Required: true},
		sdk.SettingField{Key: "token", Label: "Token", Kind: sdk.KindSecret},
		sdk.SettingField{Key: "retries", Label: "Retries", Kind: sdk.KindInt, Default: 3},
		sdk.SettingField{
			Key: "hosts", Label: "Hosts", Kind: sdk.KindCollection,
			Columns: []sdk.SettingField{
				{Key: "name", Label: "Name", Kind: sdk.KindString, Required: true},
				{Key: "url", Label: "Address", Kind: sdk.KindString},
				{Key: "password", Label: "Password", Kind: sdk.KindSecret},
			},
		},
	)

	type host struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Password string `json:"password"`
	}
	type report struct {
		Greeting      string   `json:"greeting"`
		TokenSet      bool     `json:"token_set"`
		Retries       string   `json:"retries"`
		Hosts         []string `json:"hosts"`
		HostsWithPass int      `json:"hosts_with_password"`
	}

	sdk.Tool(p, sdk.ToolSpec{
		Name:        "get_config",
		Title:       "Report configuration",
		Description: "Reports what the host configured, with secrets reduced to whether they are set.",
		Idempotent:  true,
	}, func(context.Context, struct{}) (report, error) {
		out := report{Hosts: []string{}}
		out.Greeting, _ = p.Configured("greeting")
		_, out.TokenSet = p.Configured("token")
		out.Retries, _ = p.Configured("retries")
		var hosts []host
		_, _ = p.ConfiguredJSON("hosts", &hosts)
		for _, h := range hosts {
			out.Hosts = append(out.Hosts, h.Name)
			if h.Password != "" {
				out.HostsWithPass++
			}
		}
		return out, nil
	})

	sdk.Serve(p)
}
