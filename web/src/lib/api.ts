// The dashboard's typed view of the mcpd admin API.
//
// These mirror the Go DTOs in internal/admin. They are hand-written rather
// than generated because the surface is small and a generator would be a
// build-time dependency for very little.

export type OperationState =
  | "draft"
  | "pending_approval"
  | "approved"
  | "executing"
  | "succeeded"
  | "failed"
  | "indeterminate"
  | "rejected"
  | "expired"
  | "cancelled";

export type RiskLevel = "low" | "medium" | "high" | "critical";

export interface Change {
  field: string;
  from: unknown;
  to: unknown;
}

export interface Operation {
  id: string;
  plugin: string;
  action: string;
  state: OperationState;
  risk: RiskLevel;
  impact: string;
  changes?: Change[];
  target?: unknown;
  before?: unknown;
  desired?: unknown;
  observed?: unknown;
  requested_by: string;
  requested_at: string;
  expires_at: string;
  approved_by?: string;
  approved_at?: string;
  execute_by?: string;
  terminal_at?: string;
  /**
   * Whether re-reading upstream confirmed the change landed.
   *
   * Three values, not two. `true` is confirmed, `false` is checked and did not
   * match, and absent means nobody has checked -- which is the ordinary state
   * of an operation still in flight. Typed to include null so a call site
   * cannot narrow it to a boolean by accident and render "not checked" as a
   * tick.
   */
  verified?: boolean | null;
  /**
   * Which of the two things called an approval this record is.
   *
   * A `reviewed_change` carries all three proofs: exact fields, drift
   * detection, and an outcome that was confirmed. A `gated_call` carries a
   * person's yes and the fact the call was made. The distinction is the
   * server's, computed from the two flags below, and the console must not let
   * the second wear the first's name.
   */
  assurance: "reviewed_change" | "gated_call";
  /**
   * Whether this operation carries a precondition snapshot a re-plan can be
   * compared against. False means no drift check ran -- which is a different
   * fact from one that ran and found nothing.
   */
  drift_checked: boolean;
  /** The mutation's own declaration that re-reading the target proves the write. */
  outcome_verifiable: boolean;
  attempts: number;
  error_code?: string;
  error_detail?: string;
  terminal: boolean;
}

export interface AuditRecord {
  seq: number;
  at: string;
  kind: string;
  actor: string;
  operation_id?: string;
  plugin?: string;
  action?: string;
  from_state?: string;
  to_state?: string;
  risk?: string;
  detail?: unknown;
}

/**
 * Two roles, and the line between them is administering the host rather than
 * operating it. A user reads, proposes, and approves; an administrator also
 * changes settings, makes tunnels, and manages accounts.
 */
export type Role = "user" | "admin";

/** The signed-in person, as returned by the session endpoints. */
export interface Session {
  email: string;
  /**
   * What to render. Never empty: the server resolves it, falling back to the
   * address, and re-checks the stored value against the rules on the way out.
   */
  name: string;
  /**
   * What is stored, which may be empty. It belongs in an edit field so a value
   * round-trips; rendering it anywhere else would persist the fallback into
   * the box somebody then saves.
   */
  display_name: string;
  role: Role;
  plugins: string[];
  csrf_token: string;
  expires_at: string;
}

export interface User {
  id: string;
  email: string;
  /** Resolved, and never empty. See `Session.name`. */
  name: string;
  /** Raw, and only for an edit field. See `Session.display_name`. */
  display_name: string;
  role: Role;
  plugins: string[];
  disabled: boolean;
  created_at: string;
  last_login_at?: string;
  /** True for the account making the request. */
  self: boolean;
}

export interface CreateUser {
  email: string;
  password: string;
  display_name?: string;
  role: Role;
  plugins: string[];
}

export interface UpdateUser {
  display_name?: string;
  role?: Role;
  plugins?: string[];
  disabled?: boolean;
  password?: string;
}

export interface Tool {
  name: string;
  /** "read" looks things up; "propose" suggests a change for approval. */
  kind: "read" | "propose";
}

export interface Setting {
  key: string;
  value: string;
  /** True when the real value was withheld. */
  secret: boolean;
}

export interface Plugin {
  name: string;
  /** The integration this is an instance of; equals name unless there are several. */
  type: string;
  version: string;
  title: string;
  description: string;
  endpoint: string;
  /** Full address to paste into a client. */
  connect_url: string;
  health: "healthy" | "degraded" | "unhealthy";
  health_message?: string;
  tools: Tool[];
  mutations: string[];
  required: boolean;
  /** Names this instance's group in the settings payload, when it has one. */
  settings_group?: string;
  settings: Setting[];
}

/** An integration this build has, offered when adding an instance. */
export interface PluginType {
  name: string;
  title: string;
  description: string;
  /** Whether adding one will ask for settings. */
  configurable: boolean;
}

/** One configured instance, mounted or not. */
export interface PluginInstance {
  name: string;
  type: string;
  /**
   * What kind of thing this is: "builtin" for an integration compiled into
   * this binary, "mcp" for a remote MCP server.
   *
   * Read this field rather than inferring it from `type`. The two are managed
   * in entirely different places -- one has a compiled-in type and a settings
   * form, the other an imported document and a tool list to classify -- and a
   * name has never been a reliable way to tell them apart.
   */
  runtime?: "builtin" | "mcp";
  /** Defined in the config file, so the dashboard cannot remove it. */
  from_file: boolean;
  enabled: boolean;
  /** Serving now. False until every required setting has a value. */
  mounted: boolean;
  /** Required settings still to be filled in. Empty when it is ready. */
  missing?: string[];
  /** Why a fully configured instance still is not serving. */
  problem?: string;
}

export interface HealthCheck {
  name: string;
  status: "up" | "degraded" | "down";
  message?: string;
  critical: boolean;
}

export interface HealthReport {
  status: "up" | "degraded" | "down";
  checks: HealthCheck[];
}

export interface Meta {
  version: string;
  auth_mode: string;
  /** No account exists yet, so the dashboard offers to create the first. */
  needs_setup: boolean;
}

export type TunnelState =
  | "disabled"
  | "stopped"
  | "starting"
  | "connected"
  | "failed";

export interface TunnelStatus {
  state: TunnelState;
  tunnel_id?: string;
  principal?: string;
  role?: string;
  plugins?: string[];
  /** Set when ChatGPT signs people in; absent when everyone shares one identity. */
  mcp_url?: string;
  /** The system this connector reaches, empty for all of them. */
  plugin?: string;
  message?: string;
  connected_at?: string;
}

export interface TunnelVersion {
  embedded: string;
  latest?: string;
  update_available: boolean;
  checked_at?: string;
  note?: string;
}

export interface TunnelInfo {
  /** One per connector: a tunnel carries a single endpoint, so a system with
   *  its own connector has a tunnel of its own. */
  tunnels: TunnelStatus[];
  version?: TunnelVersion;
  /** Whether tunnels can be created and deleted from here. */
  can_manage: boolean;
  /** Every tunnel in the OpenAI account, when an admin key is set. */
  available?: OpenAITunnel[];
  /** Why the list above is missing, when it shouldn't be. */
  problem?: string;
  /** Which credential is needed to manage tunnels, when one is absent. */
  missing?: string;
  /**
   * Which system each tunnel is pointed at, by tunnel id, "" meaning every
   * system. Read from the stored configuration, so a tunnel assigned to a
   * plugin that has not started is here and absent from `tunnels`.
   */
  assignments?: Record<string, string>;
  /** The systems a tunnel can be pointed at. */
  plugins: string[];
  /**
   * ChatGPT workspaces already in use by a tunnel here.
   *
   * OpenAI publishes no endpoint that lists workspaces, so these are read off
   * the tunnels that have one. Empty means either no workspaces (a personal
   * account has none) or no tunnel has been scoped to one yet.
   */
  workspaces: string[];
}

export interface OpenAITunnel {
  id: string;
  name: string;
  description?: string;
  workspace_ids?: string[];
}

export type SettingKind =
  | "string" | "secret" | "bool" | "int" | "duration" | "enum" | "list";

export type SettingApply = "live" | "reconnect" | "restart";

export type SettingSection = "settings" | "plugins" | "tunnels";

export interface SettingField {
  key: string;
  label: string;
  help?: string;
  kind: SettingKind;
  group: string;
  apply: SettingApply;
  default?: unknown;
  options?: string[];
  min?: number;
  max?: number;
  required?: boolean;
  placeholder?: string;
}

export interface SettingGroup {
  name: string;
  title: string;
  help?: string;
  enabled_by?: string;
  /** Which page owns this group. */
  section: SettingSection;
  fields: SettingField[];
}

export interface BootstrapSetting {
  key: string;
  label: string;
  value: string;
  help?: string;
}

export interface SettingsPayload {
  groups: SettingGroup[];
  values: Record<string, unknown>;
  /** Which secret keys hold a value. The values themselves are never sent. */
  secrets_set: Record<string, boolean>;
  encryption_available: boolean;
  bootstrap: BootstrapSetting[];
}

export interface SaveResult {
  applied: string[];
  restart_required?: string[];
  reconnected?: string[];
}

export interface Endpoints {
  /** One address serving everything the token is allowed to reach. */
  aggregate: string;
  /** The shape of a single-system address. */
  per_plugin_example: string;
}

/* -- remote MCP servers ---------------------------------------------------- */

/**
 * Where a discovered tool stands.
 *
 * Three states rather than a boolean: "pending" is nobody has looked at it
 * yet, "disabled" is somebody looked and said no. Only "enabled" is served.
 */
export type MCPToolState = "pending" | "enabled" | "disabled";

/**
 * A remote server's claims about one of its tools.
 *
 * The MCP specification says a client must not rely on annotations from an
 * untrusted server, and a remote server is by definition untrusted. They are
 * shown as the server's claim and may seed a default in the classify form;
 * nothing here treats one as fact.
 */
export interface MCPToolAnnotations {
  title?: string;
  readOnlyHint?: boolean;
  destructiveHint?: boolean;
  idempotentHint?: boolean;
  openWorldHint?: boolean;
}

/** A remote tool as it was last described to us. */
export interface MCPToolDescriptor {
  name: string;
  title?: string;
  description?: string;
  inputSchema?: unknown;
  annotations?: MCPToolAnnotations;
}

export interface MCPTool {
  name: string;
  descriptor: MCPToolDescriptor;
  /**
   * Identifies the descriptor, and is the guard on every state change. A
   * classification carries the hash of the descriptor the administrator was
   * actually looking at; if discovery replaced it in between, the write
   * matches no rows and comes back 409.
   */
  descriptor_hash: string;
  state: MCPToolState;
  /** Why this tool cannot be served even if enabled. */
  problem?: string;
  first_seen_at: string;
  last_seen_at: string;
}

/** One imported remote server. */
export interface MCPServer {
  name: string;
  title: string;
  description: string;
  version: string;
  schema_version: string;
  transport: string;
  /** The URL template as imported, braces intact. */
  url: string;
  enabled: boolean;
  mounted: boolean;
  created_at: string;
  updated_at: string;
  /** False when this build can no longer parse the stored document. */
  readable: boolean;
  pending: number;
  enabled_tools: number;
  disabled: number;
}

/** What one discovery changed. */
export interface MCPDiff {
  added?: string[];
  changed?: string[];
  removed?: string[];
  unchanged?: string[];
}

/** ApiError carries the server's stable error code so callers can branch on it. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly detail: string,
    readonly correlationId?: string,
    /**
     * Field-level complaints, when the endpoint sends them.
     *
     * Saving settings is the one refusal that is a list rather than a
     * sentence: `handlePutSettings` answers a bad value with `problems` and no
     * `detail` at all. Without carrying them the form could only show the bare
     * code `invalid_settings`, which does not say which field.
     */
    readonly problems?: string[],
  ) {
    super(detail || code);
    this.name = "ApiError";
  }
}

/**
 * The session itself lives in an HttpOnly cookie the browser sets and this
 * code cannot read. That is the point: the credential behind a console that
 * approves infrastructure changes should not be reachable from any script that
 * finds its way onto the page.
 *
 * What is held here is only the CSRF token. It is not a credential -- on its
 * own it authenticates nothing -- and it has to be readable by this code in
 * order to be echoed back, which is exactly the property being tested: a
 * cross-site request can cause the cookie to be sent but cannot read this.
 */
let csrfToken: string | null = null;

export function setCSRFToken(token: string | null): void {
  csrfToken = token;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (init?.body) headers.set("Content-Type", "application/json");

  const method = (init?.method ?? "GET").toUpperCase();
  const safe = method === "GET" || method === "HEAD" || method === "OPTIONS";
  if (!safe && csrfToken) headers.set("X-CSRF-Token", csrfToken);

  // credentials: "include" so the session cookie travels. Same-origin would
  // cover the deployed case, but the dashboard is also served through a dev
  // proxy on another port, where it would not.
  const response = await fetch(path, { ...init, headers, credentials: "include" });
  if (response.status === 204) return undefined as T;

  let body: Record<string, unknown> = {};
  try {
    body = await response.json();
  } catch {
    // A non-JSON body from a proxy or gateway.
  }

  if (!response.ok) {
    const problems = Array.isArray(body.problems)
      ? body.problems.map(String)
      : undefined;
    throw new ApiError(
      response.status,
      String(body.error ?? `http_${response.status}`),
      String(body.detail ?? body.error ?? response.statusText),
      body.correlation_id as string | undefined,
      problems,
    );
  }
  return body as T;
}

export const api = {
  meta: () => request<Meta>("/api/meta"),

  signIn: (email: string, password: string) =>
    request<Session>("/api/session", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }),

  session: () => request<Session>("/api/session"),

  /** Claims an instance that has no accounts yet. The first one is admin. */
  registerFirst: (email: string, password: string, displayName?: string) =>
    request<Session>("/api/setup", {
      method: "POST",
      body: JSON.stringify({ email, password, display_name: displayName ?? "" }),
    }),

  signOut: () => request<void>("/api/session", { method: "DELETE" }),

  users: () => request<{ users: User[]; count: number }>("/api/users"),

  createUser: (body: CreateUser) =>
    request<User>("/api/users", { method: "POST", body: JSON.stringify(body) }),

  updateUser: (id: string, body: UpdateUser) =>
    request<User>(`/api/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  /**
   * Renames the account the request authenticated as.
   *
   * It carries no identifier and cannot address another account, which is
   * what lets it take `read` rather than `admin`. Naming somebody else is
   * still `updateUser`, and still administration.
   */
  updateAccount: (displayName: string) =>
    request<User>("/api/account", {
      method: "PATCH",
      body: JSON.stringify({ display_name: displayName }),
    }),

  deleteUser: (id: string) =>
    request<void>(`/api/users/${encodeURIComponent(id)}`, { method: "DELETE" }),

  operations: (state?: OperationState, limit?: number) => {
    const q = new URLSearchParams();
    if (state) q.set("state", state);
    if (limit) q.set("limit", String(limit));
    const query = q.toString();
    return request<{ operations: Operation[]; count: number }>(
      query ? `/api/operations?${query}` : "/api/operations",
    );
  },

  operation: (id: string) =>
    request<{ operation: Operation; audit: AuditRecord[] }>(
      `/api/operations/${encodeURIComponent(id)}`,
    ),

  approve: (id: string, reason: string) =>
    request<Operation>(`/api/operations/${encodeURIComponent(id)}/approve`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),

  reject: (id: string, reason: string) =>
    request<Operation>(`/api/operations/${encodeURIComponent(id)}/reject`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),

  /** Withdrawing a proposal, which the proposer may do without approve. */
  cancel: (id: string, reason: string) =>
    request<Operation>(`/api/operations/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),

  plugins: () => request<{ plugins: Plugin[]; count: number }>("/api/plugins"),

  endpoints: () => request<Endpoints>("/api/endpoints"),

  tunnel: () => request<TunnelInfo>("/api/tunnel"),

  settings: () => request<SettingsPayload>("/api/settings"),

  saveSettings: (values: Record<string, string>, clearSecrets: string[] = []) =>
    request<SaveResult>("/api/settings", {
      method: "PUT",
      body: JSON.stringify({ values, clear_secrets: clearSecrets }),
    }),

  tunnelStart: () =>
    request<TunnelStatus>("/api/tunnel/start", { method: "POST" }),

  tunnelStop: () => request<TunnelStatus>("/api/tunnel/stop", { method: "POST" }),

  createTunnel: (name: string, plugin: string, workspaceID?: string) =>
    request<OpenAITunnel>("/api/tunnels", {
      method: "POST",
      body: JSON.stringify({ name, plugin, workspace_id: workspaceID ?? "" }),
    }),

  assignTunnel: (id: string, plugin: string) =>
    request<{ status: string }>(`/api/tunnels/${encodeURIComponent(id)}/assign`, {
      method: "POST",
      body: JSON.stringify({ plugin }),
    }),

  deleteTunnel: (id: string) =>
    request<{ status: string }>(`/api/tunnels/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  audit: (limit = 100) =>
    request<{ records: AuditRecord[]; count: number }>(`/api/audit?limit=${limit}`),

  verifyAudit: () => request<{ intact: boolean; broken_at: number }>("/api/audit/verify"),

  clearAudit: () =>
    request<{ removed: number }>("/api/audit", { method: "DELETE" }),

  health: () => request<HealthReport>("/api/health"),

  pluginTypes: () =>
    request<{ types: PluginType[]; count: number }>("/api/plugin-types"),

  instances: () =>
    request<{ instances: PluginInstance[]; count: number }>("/api/instances"),

  addInstance: (name: string, type: string) =>
    request<{ status: string; note?: string }>(
      "/api/instances", { method: "POST", body: JSON.stringify({ name, type }) }),

  setInstanceEnabled: (name: string, enabled: boolean) =>
    request<{ status: string }>(`/api/instances/${encodeURIComponent(name)}`, {
      method: "PATCH", body: JSON.stringify({ enabled }),
    }),

  removeInstance: (name: string) =>
    request<{ status: string }>(`/api/instances/${encodeURIComponent(name)}`, {
      method: "DELETE",
    }),

  mcpServers: () => request<{ servers: MCPServer[] }>("/api/mcp-servers"),

  mcpServerTools: (name: string) =>
    request<{ tools: MCPTool[]; count: number }>(
      `/api/mcp-servers/${encodeURIComponent(name)}/tools`,
    ),

  importMCPServer: (name: string, document: unknown) =>
    request<{ status: string; note?: string }>("/api/mcp-servers", {
      method: "POST",
      body: JSON.stringify({ name, document }),
    }),

  discoverMCPServer: (name: string) =>
    request<{ status: string; diff: MCPDiff; note?: string }>(
      `/api/mcp-servers/${encodeURIComponent(name)}/discover`, { method: "POST" },
    ),

  setMCPServerEnabled: (name: string, enabled: boolean) =>
    request<{ status: string }>(`/api/mcp-servers/${encodeURIComponent(name)}`, {
      method: "PATCH",
      body: JSON.stringify({ enabled }),
    }),

  removeMCPServer: (name: string) =>
    request<{ status: string }>(`/api/mcp-servers/${encodeURIComponent(name)}`, {
      method: "DELETE",
    }),

  /**
   * Records a decision about one tool.
   *
   * `descriptorHash` is the descriptor the operator actually read. A 409 means
   * it no longer matches what is stored -- the far end changed its tool under
   * them -- and the answer is to show them what changed, never to resend with
   * the new hash.
   */
  classifyMCPTool: (
    server: string, tool: string, state: MCPToolState, descriptorHash: string,
  ) =>
    request<{ status: string }>(
      `/api/mcp-servers/${encodeURIComponent(server)}/tools/${encodeURIComponent(tool)}`,
      { method: "PATCH", body: JSON.stringify({ state, descriptor_hash: descriptorHash }) },
    ),
};
