package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Defaults for a generated deployment. They are overridable from the
// environment because the container generates its config on first start and
// has to produce one that matches its own port mapping -- a file advertising
// 9080 in a container publishing 8080 tells the dashboard to hand out an
// address nothing answers on.
const (
	defaultInitListen         = "127.0.0.1:9080"
	defaultInitFrontendListen = "127.0.0.1:9090"
	defaultInitPublicURL      = "http://localhost:9080"
)

// initialize scaffolds a working deployment: directories, a configuration
// file, generated secrets, and a .env holding them.
//
// It exists because the first ten minutes with a new service are where most
// deployments go wrong -- a token pasted into the config file, a data
// directory the service user cannot write, a port already in use. Generating
// all of it removes the chance to get any of those wrong.
//
// It refuses to overwrite anything. Re-running it on a live deployment must
// never replace a token that credentials in the field are already using, and
// must never replace the key every stored credential was encrypted under.
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

	// Generated here rather than left to the operator: a deployment without a
	// secret key cannot store credentials from the dashboard, and discovering
	// that later is worse than one extra line in a file.
	//
	// A secret the environment already supplies is left where it is. Copying
	// it into a second file would be one more place to leak it from, and
	// generating a *different* one would be worse than that: the file would
	// quietly take over the moment the environment stopped supplying one, and
	// every credential encrypted under the environment's key would be
	// unreadable with nothing saying why.
	token, err := generateUnlessInEnv("MCPD_TOKEN_LOCAL")
	if err != nil {
		return err
	}
	secretKey, err := generateUnlessInEnv("MCPD_SECRET_KEY")
	if err != nil {
		return err
	}

	gen := generatedConfig{
		dbPath:         dbPath,
		pluginsDir:     pluginsDir,
		listen:         envOr("MCPD_LISTEN", defaultInitListen),
		frontendListen: envOr("MCPD_FRONTEND_LISTEN", defaultInitFrontendListen),
		publicURL:      envOr("MCPD_PUBLIC_URL", defaultInitPublicURL),
	}

	if err := writeFile(configPath, 0o640, initialConfig(gen)); err != nil {
		return err
	}
	// 0600: this file holds a bearer token and the encryption key.
	if err := writeFile(envPath, 0o600, initialEnv(token, secretKey)); err != nil {
		return err
	}

	fmt.Printf(`Created a deployment in %s

  config.yaml   configuration (no secrets in it)
  .env          generated secrets, mode 0600
  plugins/      drop out-of-process plugins here
  mcpd.db       created on first start

Start it:

  mcpd -config %s

The dashboard is on %s and the MCP endpoint on %s. Open the dashboard and it
asks you to create the first account.

Put TLS in front of it before it is reachable from any network you do not
control.
`, abs, configPath, displayAddr(gen.frontendListen), gen.publicURL)

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

// generateUnlessInEnv returns a fresh secret, or the empty string when the
// environment already supplies one under this name. Empty tells initialEnv to
// leave the variable out of the generated file rather than write a second
// copy of it or a competing value.
func generateUnlessInEnv(name string) (string, error) {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return "", nil
	}
	return generateToken()
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// displayAddr turns a listen address into one somebody can paste into a
// browser. A bare ":9090" is a valid thing to bind and not a valid thing to
// visit.
func displayAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	// A wildcard bind answers on every interface, so any of them is a correct
	// thing to print and loopback is the one that always works.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
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

// generatedConfig is what varies between one generated deployment and the
// next. Everything else in the template is the same everywhere.
type generatedConfig struct {
	dbPath         string
	pluginsDir     string
	listen         string
	frontendListen string
	publicURL      string
}

func initialConfig(g generatedConfig) string {
	tmpl := `# mcpd configuration.
#
# Generated on first start, and yours to edit -- mcpd never writes this file.
#
# No secrets belong in it. Credentials are referenced by name and resolved at
# startup from the environment, the .env beside this file, or systemd's
# LoadCredential directory.

server:
  # Loopback by default. mcpd speaks plain HTTP and carries bearer tokens, so
  # put a TLS-terminating proxy in front of it before exposing it further.
  #
  # Under Docker this is overridden from the environment, because what the
  # process binds inside the container is decided by the port mapping rather
  # than by this file.
  listen: "__LISTEN__"

  # The operator dashboard, on its own port so a firewall rule can distinguish
  # it from the MCP endpoint.
  frontend_listen: "__FRONTEND_LISTEN__"
  frontend_enabled: true

  # The address assistants reach the MCP endpoint at -- the "listen" port
  # above, not the dashboard. It is what the dashboard shows as a connection
  # address, so it must match what clients actually use.
  #
  # Behind a reverse proxy, set it to the address people type. It is not
  # cosmetic there: mcpd is reached over plain HTTP in that shape, so the
  # scheme here is the only way it can know the session cookie needs Secure.
  public_url: "__PUBLIC_URL__"

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
# It was generated into the .env beside this file. Keep it: replace it and
# every credential already stored becomes unreadable.
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
	return strings.NewReplacer(
		"__DB__", g.dbPath,
		"__PLUGINS__", g.pluginsDir,
		"__LISTEN__", g.listen,
		"__FRONTEND_LISTEN__", g.frontendListen,
		"__PUBLIC_URL__", g.publicURL,
	).Replace(tmpl)
}

// initialEnv writes the generated secrets. An empty value means the
// environment already supplies that one, and the file says so rather than
// carrying a second copy that could later diverge.
func initialEnv(token, secretKey string) string {
	var b strings.Builder
	b.WriteString(`# Secrets for mcpd. Mode 0600; never commit this file.
#
# Real environment variables take precedence over anything here, so an
# orchestrator injecting a rotated secret is not overridden by a stale value.

# Bearer token for machine callers. People sign in to the dashboard with an
# email and password instead.
`)
	b.WriteString(secretLine("MCPD_TOKEN_LOCAL", token))
	b.WriteString(`
# Encrypts secrets stored in the database, so they can be set from the
# dashboard. Keep it: without it, those secrets cannot be read back.
`)
	b.WriteString(secretLine("MCPD_SECRET_KEY", secretKey))
	return b.String()
}

func secretLine(name, value string) string {
	if value == "" {
		return fmt.Sprintf("# %s comes from the environment; not duplicated here.\n", name)
	}
	return fmt.Sprintf("%s=%s\n", name, value)
}
