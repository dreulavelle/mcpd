package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// initialize scaffolds a working deployment: directories, a configuration
// file, a generated token, and a .env holding it.
//
// It exists because the first ten minutes with a new service are where most
// deployments go wrong -- a token pasted into the config file, a data
// directory the service user cannot write, a port already in use. Generating
// all of it removes the chance to get any of those wrong.
//
// It refuses to overwrite anything. Re-running it on a live deployment must
// never replace a token that credentials in the field are already using.
func initialize(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}

	var (
		configPath = filepath.Join(abs, "config.yaml")
		envPath    = filepath.Join(abs, ".env")
		pluginsDir = filepath.Join(abs, "plugins")
		dbPath     = filepath.Join(abs, "mcpd.db")
	)

	for _, path := range []string{configPath, envPath} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf(
				"%s already exists; refusing to overwrite it. "+
					"Remove it first if you really want to start over", path)
		}
	}

	for _, d := range []string{abs, pluginsDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	token, err := generateToken()
	if err != nil {
		return err
	}
	// Generated here rather than left to the operator: a deployment without
	// one cannot store secrets from the dashboard, and discovering that later
	// is worse than one extra line in a file.
	secretKey, err := generateToken()
	if err != nil {
		return err
	}

	if err := writeFile(configPath, 0o640, initialConfig(dbPath, pluginsDir)); err != nil {
		return err
	}
	// 0600: this file holds a bearer token.
	if err := writeFile(envPath, 0o600, initialEnv(token, secretKey)); err != nil {
		return err
	}

	fmt.Printf(`Created a deployment in %s

  config.yaml   configuration (no secrets in it)
  .env          generated token, mode 0600
  plugins/      drop out-of-process plugins here
  mcpd.db       created on first start

Start it:

  mcpd -config %s

The dashboard is on http://localhost:9090 and the MCP endpoint on
http://localhost:9080. Sign in to the dashboard with the token in .env.

Both listeners are on loopback. Change server.listen and
server.frontend_listen to reach it from elsewhere, and put TLS in front of
it before it is reachable from any network you do not control.
`, abs, configPath)

	return nil
}

func writeFile(path string, mode os.FileMode, content string) error {
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile honours the umask, so the mode is set explicitly to guarantee
	// the .env is not group- or world-readable.
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("system entropy unavailable: %w", err)
	}
	// URL-safe and unpadded, so it survives headers, shells and .env files
	// without quoting.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func initialConfig(dbPath, pluginsDir string) string {
	tmpl := `# mcpd configuration.
#
# No secrets belong in this file. Credentials are referenced by name and
# resolved at startup from the environment, a .env beside this file, or
# systemd's LoadCredential directory.

server:
  # Loopback by default. mcpd speaks plain HTTP and carries bearer tokens, so
  # put a TLS-terminating proxy in front of it before exposing it further.
  listen: "127.0.0.1:9080"

  # The operator dashboard, on its own port so a firewall rule can distinguish
  # it from the MCP endpoint.
  frontend_listen: "127.0.0.1:9090"
  frontend_enabled: true

  # The address assistants reach the MCP endpoint at -- the "listen" port
  # above, not the dashboard. It is what the dashboard shows as a connection
  # address, so it must match what clients actually use.
  public_url: "http://localhost:9080"

  read_header_timeout: 10s
  read_timeout: 60s
  write_timeout: 120s
  idle_timeout: 120s
  shutdown_timeout: 30s

storage:
  path: __DB__
  plugins_dir: __PLUGINS__
  busy_timeout: 5s

  # Leave false. Under WAL, relaxed durability can lose the most recent
  # transactions on power loss, and those transactions authorise
  # infrastructure changes.
  relaxed_durability: false

# Key used to encrypt secrets stored in the database, so they can be set from
# the dashboard without landing in plaintext beside the data they protect. The
# key stays outside the database, which is what makes a stolen copy useless.
#
# Generate one with:  openssl rand -base64 32
secret_key_ref: env:MCPD_SECRET_KEY

auth:
  # People sign in to the dashboard with an email and password. The first
  # account is made from the dashboard itself: a host with none offers to
  # create one, and whoever does becomes the administrator.
  accounts:
    session_ttl: 12h

  # Bearer tokens, for machine callers that cannot complete a sign-in form.
  static_tokens:
    - id: local
      secret_ref: env:MCPD_TOKEN_LOCAL
      principal: svc:local
      role: admin
      # Which plugins this credential may reach. Everything else returns 404,
      # so a scoped agent cannot discover what else is deployed.
      plugins: ["*"]

approval:

  proposal_ttl: 30m
  approval_ttl: 15m
  lease_ttl: 2m

  # Highest risk a user may approve from a single yes/no prompt raised by
  # their assistant. Above it the shortcut is withheld, not the decision: the
  # assistant has to show the change in full and be told explicitly. Either way
  # the person decides in the conversation.
  inline_max_risk: medium

logging:
  level: info
  format: json

# OpenAI's Secure MCP Tunnel, running inside mcpd. This is how ChatGPT reaches
# mcpd without an inbound port, public DNS, or a NAT rule.
#
# It runs in this process, so there is no request to authenticate and what the
# tunnel may reach is decided here rather than by a bearer token.
tunnel:
  enabled: false
  tunnel_id: ""
  # A *runtime* API key, not an admin key.
  api_key_ref: env:OPENAI_TUNNEL_API_KEY
  principal: svc:chatgpt
  role: user
  plugins: ["echo"]
  check_for_updates: true

plugins:
  # A test connection for checking that everything works end to end. It
  # touches nothing outside mcpd. Leave it on until you have connected an
  # assistant successfully, then turn it off.
  echo:
    enabled: true
    required: false
`
	tmpl = strings.ReplaceAll(tmpl, "__DB__", dbPath)
	return strings.ReplaceAll(tmpl, "__PLUGINS__", pluginsDir)
}

func initialEnv(token, secretKey string) string {
	return fmt.Sprintf(`# Secrets for mcpd. Mode 0600; never commit this file.
#
# Real environment variables take precedence over anything here, so an
# orchestrator injecting a rotated secret is not overridden by a stale value.

# Bearer token for machine callers. People sign in to the dashboard with an
# email and password instead.
MCPD_TOKEN_LOCAL=%s

# Encrypts secrets stored in the database, so they can be set from the
# dashboard. Keep it: without it, those secrets cannot be read back.
MCPD_SECRET_KEY=%s


`, token, secretKey)
}
