// The dashboard's typed view of the mcpd admin API, mirroring the Go DTOs in
// internal/admin. Hand-written: the surface is small.

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
  /**
   * The rule that approved this with nobody asked, and the discriminator for
   * that case -- never `approved_by`, which is the non-account "system:policy".
   */
  authorized_by_rule?: string;
  execute_by?: string;
  terminal_at?: string;
  /**
   * Three values, not two: true confirmed, false checked and did not match,
   * absent nobody checked. Null is in the type so a call site cannot narrow it
   * to a boolean and render "not checked" as a tick.
   */
  verified?: boolean | null;
  /**
   * `reviewed_change` carries exact fields, a drift check and a confirmed
   * outcome; `gated_call` carries an authorisation and nothing else. The
   * console must not let the second wear the first's name.
   */
  assurance: "reviewed_change" | "gated_call";
  /** False means no drift check ran, which is not one that found nothing. */
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

/** A user reads, proposes and approves; an administrator also administers. */
export type Role = "user" | "admin";

/** The signed-in person, as returned by the session endpoints. */
export interface Session {
  email: string;
  /** What to render. Never empty; the server falls back to the address. */
  name: string;
  /** The raw stored value, which may be empty. Only for an edit field. */
  display_name: string;
  role: Role;
  plugins: string[];
  csrf_token: string;
  expires_at: string;
  /**
   * "pending" is an account waiting for an administrator. It is signed in and
   * holds no capability at all; the console draws a screen saying so.
   *
   * This is not what enforces it — the server refuses every call a pending
   * principal makes. It only decides which screen to draw instead of a console
   * whose every fetch comes back 403.
   */
  status: AccountStatus;
  /** False for an account that only signs in through a provider. */
  has_password: boolean;
}

/** What has been decided about an account. Not the same axis as `disabled`. */
export type AccountStatus = "active" | "pending";

/** An identity provider mcpd will sign somebody in through. */
export type ProviderName = "google" | "github" | "entra" | "oidc";

export interface ProviderDescriptor {
  provider: ProviderName;
  label: string;
}

/**
 * What the signed-out page may offer, before anybody has signed in.
 *
 * There is deliberately no "will it wait for approval" here. A password
 * registration always waits: the setting that switches approval off applies to
 * the providers, which check the address, and nothing checks one typed into a
 * form. A field saying otherwise would have the form promise something false.
 */
export interface AuthOptions {
  providers: ProviderDescriptor[];
  /** Whether somebody without an account may ask for one. */
  registration: boolean;
}

/**
 * An account waiting for an administrator, and how it got here.
 *
 * The providers are on the row because approving is a privilege grant and the
 * provider is what decides how much the address is worth: "alice@corp.com,
 * proved by your directory" and "alice@corp.com, typed into a form" are the
 * same string and completely different facts.
 */
export interface PendingRegistration extends User {
  /** Provider labels. Empty means somebody typed the address into the form. */
  providers: string[];
}

/** A provider linked to the signed-in account. */
export interface LinkedIdentity {
  provider: ProviderName;
  label: string;
  email: string;
  linked_at: string;
}

export interface User {
  id: string;
  email: string;
  /** Resolved, and never empty. See `Session.name`. */
  name: string;
  /** Raw, and only for an edit field. See `Session.display_name`. */
  display_name: string;
  role: Role;
  /** The account's own grant, for an edit field. See `reaches`. */
  plugins: string[];
  /** What it actually reaches: its own grant plus every group's. */
  reaches: string[];
  groups: GroupRef[];
  disabled: boolean;
  /** "pending" is waiting for an administrator; `disabled` is switched off. */
  status: AccountStatus;
  /** False for an account that only signs in through a provider. */
  has_password: boolean;
  created_at: string;
  last_login_at?: string;
  /** True for the account making the request. */
  self: boolean;
}

/**
 * A group is a name and a set of systems. Membership is what hands those
 * systems to an account or a key; nothing else about a group grants anything.
 */
export interface Group {
  id: string;
  name: string;
  description: string;
  /** Empty means the group hands out nothing, which is what a new one does. */
  plugins: string[];
  members: number;
  created_by: string;
  created_at: string;
}

/** A group as it appears beside a member, where only the grant matters. */
export interface GroupRef {
  id: string;
  name: string;
  plugins: string[];
}

export interface GroupMember {
  kind: "user" | "key";
  id: string;
  /** An address for an account, a name for a key. */
  label: string;
  added_by: string;
  added_at: string;
}

/** A key is active until it is revoked or its expiry passes. */
export type KeyStatus = "active" | "expired" | "revoked";

export interface ApiKey {
  id: string;
  name: string;
  role: Role;
  /** The key's own grant, for an edit field. See `reaches`. */
  plugins: string[];
  /** What it actually reaches: its own grant plus its groups'. */
  reaches: string[];
  groups: GroupRef[];
  status: KeyStatus;
  created_by: string;
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked_at?: string;
  revoked_by?: string;
}

export interface CreateKey {
  name: string;
  role: Role;
  plugins: string[];
  groups: string[];
  /** RFC 3339, or absent for a key that never expires. */
  expires_at?: string;
}

export interface UpdateKey {
  name?: string;
  role?: Role;
  plugins?: string[];
  /** A date to set one, "" to clear it, absent to leave it alone. */
  expires_at?: string;
}

export interface CreateUser {
  email: string;
  password: string;
  display_name?: string;
  role: Role;
  plugins: string[];
  groups?: string[];
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
  /** Read this rather than inferring from `type`, which is not reliable. */
  runtime?: "builtin" | "mcp";
  /**
   * Declared in the configuration file. It can still be removed here: mcpd
   * never writes that file, so a removal is an override, not an edit.
   */
  from_file: boolean;
  /** The file's `required: true`. Removing one takes a second, explicit yes. */
  required?: boolean;
  enabled: boolean;
  /** Serving now. False until every required setting has a value. */
  mounted: boolean;
  /** Required settings still to be filled in. Empty when it is ready. */
  missing?: string[];
  /** Why a fully configured instance still is not serving. */
  problem?: string;
  /** Removed here while the file still declares it. Listed, so it can be restored. */
  removed?: boolean;
  removed_by?: string;
  removed_at?: string;
  /** What the configuration file says about it, when it says anything. */
  declaration?: PluginDeclaration;
}

/** The configuration file's entry, shown read-only. Keys without values. */
export interface PluginDeclaration {
  type: string;
  enabled: boolean;
  required: boolean;
  settings_keys?: string[];
}

/**
 * A removal whose declaration has left the file. Kept, so one bad deploy does
 * not resurrect every plugin an operator removed.
 */
export interface StaleRemoval {
  name: string;
  declared_type: string;
  removed_by: string;
  removed_at: string;
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
   * Which system each tunnel is pointed at, "" meaning all. From the stored
   * configuration, so it lists tunnels `tunnels` does not.
   */
  assignments?: Record<string, string>;
  /** Which ChatGPT account each tunnel connects with, by tunnel id. A tunnel
   *  absent from here has not been given one, which on a host with several
   *  accounts is why it is not running. */
  account_assignments?: Record<string, string>;
  /** Every account, so a tunnel can be told whose credential to use. */
  accounts: ChatGPTAccount[];
  /** The systems a tunnel can be pointed at. */
  plugins: string[];
  /**
   * Workspaces in use by a tunnel here. OpenAI publishes no endpoint listing
   * them, so these are read off the tunnels that have one.
   */
  workspaces: string[];
}

export interface OpenAITunnel {
  id: string;
  name: string;
  description?: string;
  workspace_ids?: string[];
  /** Which account this tunnel was listed from. Two accounts are two
   *  organisations, so an id alone does not say whose a tunnel is. */
  account_id?: string;
  account_name?: string;
}

/**
 * One ChatGPT account, without its credentials.
 *
 * No key is ever sent to the browser -- `has_admin_key` is the only thing said
 * about one. An operator who has forgotten a key replaces it; a page that
 * could show one is a page that leaks every key to anyone who reaches it.
 */
export interface ChatGPTAccount {
  id: string;
  name: string;
  /** The identity this account's calls act as, and what history records. */
  principal: string;
  role: "user" | "admin";
  /** What this account may reach, or ["*"]. */
  plugins: string[];
  /** Calls per second across every tunnel it owns. 0 is unlimited. */
  rate_per_sec: number;
  enabled: boolean;
  organization_id?: string;
  /** Whether tunnels can be made from this account, not what the key is. */
  has_admin_key: boolean;
  /** This account's own access to its organisation. Per account: one
   *  workspace's expired admin key says nothing about another's. */
  can_manage: boolean;
  missing?: string;
  problem?: string;
  created_at: string;
}

/**
 * What the account form sends.
 *
 * Every field optional, so "not sent" and "set to empty" stay different. It
 * matters most for the keys: an edit that changes only the rate limit sends no
 * key at all, and a blank string would read as an instruction to erase one.
 */
export interface ChatGPTAccountBody {
  name?: string;
  api_key?: string;
  admin_key?: string;
  organization_id?: string;
  role?: "user" | "admin";
  plugins?: string[];
  rate_per_sec?: number;
  enabled?: boolean;
}

export type SettingKind =
  | "string" | "secret" | "bool" | "int" | "duration" | "enum" | "list";

export type SettingApply = "live" | "reconnect" | "restart";

/**
 * Which page owns a group of settings.
 *
 * Mirrors the constants in internal/settings/schema.go. A group declares its
 * own section there, so the dashboard renders where it is told rather than
 * guessing from a name.
 */
export type SettingSection =
  | "settings"
  | "plugins"
  | "tunnels"
  | "authentication"
  | "chatgpt"
  | "approvals"
  | "advanced"
  | "diagnostics";

export interface SettingField {
  key: string;
  label: string;
  help?: string;
  kind: SettingKind;
  group: string;
  apply: SettingApply;
  default?: unknown;
  options?: string[];
  /** Human-readable names for those values. A value absent here shows as itself. */
  option_labels?: Record<string, string>;
  min?: number;
  max?: number;
  /** What a duration counts in. Absent means minutes. */
  unit?: "seconds" | "minutes" | "hours";
  required?: boolean;
  placeholder?: string;
  /**
   * Hides this field unless another field in the same form holds one of the
   * named values. Presentation only: a hidden field keeps its value, still
   * submits it, and the server still stores it. Never treat hidden as unset.
   */
  show_when?: { field: string; equals: string[] };
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

/* -- the approval policy --------------------------------------------------- */

/**
 * A standing rule: a change of this shape may run without anyone being asked.
 *
 * `max_risk` holds two kinds of answer. A level allows up to that risk; empty
 * means always ask, and beats every allow rule it overlaps. They are not points
 * on one scale. A blank selector is refused -- send `policy.wildcard`.
 */
export interface ApprovalRule {
  id: string;
  plugin: string;
  action: string;
  principal: string;
  /** A ceiling, or "" for always-ask. Never "critical"; the server refuses it. */
  max_risk: string;
  note?: string;
}

export interface ApprovalPolicy {
  /** Most specific first, as the server canonicalises them. */
  rules: ApprovalRule[];
  /** The selector meaning "anything", named by the server rather than hardcoded. */
  wildcard: string;
  /**
   * The ceilings an allow rule may be given, least severe first. Not a priority
   * order between rules, and the empty ceiling is deliberately absent.
   */
  ceilings: string[];
  /** What happens where no rule matches, in the server's own words. */
  default: string;
  /** The ceiling authorised when no rule matches; "none" holds everything. */
  unmatched: string;
  /** Rules naming a plugin or action this host does not serve. Advisory. */
  warnings?: string[];
}

/** What the policy would do with a change nobody has proposed yet. */
export interface PolicyEvaluation {
  auto_approve: boolean;
  /** The rule that decided, including when it is the one saying always ask. */
  rule?: ApprovalRule;
  /** Prose meant to be shown as-is. Do not parse it. */
  reason: string;
}

export interface EvaluateRequest {
  plugin: string;
  action: string;
  principal: string;
  risk: string;
  reversible: boolean;
}

/* -- remote MCP servers ---------------------------------------------------- */

/** "pending" is nobody has looked yet; "disabled" is somebody said no. */
export type MCPToolState = "pending" | "enabled" | "disabled";

/**
 * A remote server's *claims* about one of its tools. The specification says a
 * client must not rely on them, so they are labelled as claims and never
 * treated as fact.
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
   * The guard on every state change: a classification carries the hash of the
   * descriptor the administrator read, and a write against a stale one matches
   * no rows and comes back 409.
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
  /** Headers an operator added because the document declared none. No values. */
  extra_headers: MCPExtraHeader[];
  /**
   * The document names no header and no variable of its own. Not a claim that
   * the server is open -- four in five published documents look like this, and
   * most of those answer 401.
   */
  declares_no_credential: boolean;
  /** When this server was last asked what it offers. */
  discovery: MCPDiscovery;
}

/**
 * The age of a server's tool list, and whether checking it is working.
 *
 * Three facts because one cannot say the thing that matters when a server
 * starts failing: `last_succeeded` is how old the tools on screen are and stops
 * advancing while checks fail, `last_attempted` is when checking last ran, and
 * `error` is what the last attempt said. Both absent means nothing has asked
 * this server yet.
 */
export interface MCPDiscovery {
  last_attempted?: string;
  last_succeeded?: string;
  error?: string;
}

/** One operator-added header. The value lives in settings, encrypted. */
export interface MCPExtraHeader {
  name: string;
  description: string;
  secret: boolean;
}

/* -- the public catalogue -------------------------------------------------- */

/** One server a public catalogue offers. The `server.json` is a second call. */
export interface CatalogEntry {
  /** The catalogue's own identifier -- "io.github.example/weather". */
  name: string;
  /** A legal plugin name derived from `name`. Collides often; only a suggestion. */
  suggested_name: string;
  title: string;
  description: string;
  version: string;
  /** From the document this host would dial, absent when there is nothing to dial. */
  transport?: string;
  url?: string;
  updated_at: string;
  /**
   * How many times this entry's own catalogue has been asked to call the
   * server. Absent where the catalogue publishes no such figure, which is
   * three of the four -- and absent is not zero, so render nothing rather
   * than a count.
   */
  uses?: number;
  /** Whether this host would accept the document. False for about half of them. */
  addable: boolean;
  /**
   * Why not. Present exactly when `addable` is false, which a listing does not
   * return -- only the detail endpoint and `?include_unaddable=1` do.
   */
  reason?: string;
  /**
   * Whether importing will ask for a credential: "none" or "api_key". Worked
   * out here from the document, not taken from what a catalogue claims.
   */
  auth?: string;
  /** Which catalogue this entry came from, set on every entry. */
  source: string;
}

/** How one catalogue fared on one request. */
export interface CatalogSource {
  source: string;
  /** False when the catalogue could not be reached and nothing was held for it. */
  ok: boolean;
  stale: boolean;
  retrieved_at?: string;
  /** How many entries it contributed, after deduplication. */
  entries: number;
  /** The measured counts behind `addable`. */
  judged?: number;
  addable?: number;
  /** How many servers this source says it holds. Absent where it does not say. */
  total?: number;
  /**
   * Whether this catalogue publishes how often each of its servers is called,
   * which is what "most used" can be ordered over. Read from the response
   * rather than hard-coded: which catalogue does this is configuration.
   */
  uses?: boolean;
  error?: string;
  /** What the source has to say about an answer it did give. */
  note?: string;
}

/** One page of a browse or a search. */
export interface CatalogPage {
  /** Every catalogue that answered, comma-separated. */
  source: string;
  entries: CatalogEntry[];
  /** Opaque, and absent at the end of the listing. */
  next_cursor?: string;
  /**
   * A floor, not a count: two of four sources report a total and addability is
   * measured over a sample. Render with a "+", never as a precise figure.
   */
  /** How many servers across these catalogues can be imported. Counted. */
  addable?: number;
  /** True when a catalogue could not be reached and what it last said was served. */
  stale: boolean;
  retrieved_at: string;
  sources: CatalogSource[];
}

/** One entry together with the document that would be imported. */
export interface CatalogDetail extends CatalogEntry {
  /** Unknown rather than a shape: the server validates it against the schema. */
  document: unknown;
  stale: boolean;
  retrieved_at: string;
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
     * Field-level complaints. Saving settings answers a bad value with these
     * and no `detail` at all, so without them the form can name no field.
     */
    readonly problems?: string[],
  ) {
    super(detail || code);
    this.name = "ApiError";
  }
}

// Only the CSRF token, never the session: that lives in an HttpOnly cookie
// this code deliberately cannot read.
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

  // "include" rather than same-origin: the dev proxy serves this from another
  // port, where the cookie would not travel.
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


/** One file inside a backup archive. */
export interface BackupFile {
  name: string;
  size: number;
  sha256: string;
  /** False for a file carried for reference. config.yaml is the one. */
  restored: boolean;
}

/** What an archive says about the instance it was taken from. */
export interface BackupManifest {
  created_at: string;
  mcpd_version: string;
  schema_version: number;
  instance?: string;
  key_fingerprint?: string;
  files: BackupFile[];
}

/** A restore that has been checked and is waiting for a restart. */
export interface BackupPending {
  staged_at: string;
  actor: string;
  manifest: BackupManifest;
}

/** What a backup taken now would hold. */
export interface BackupStatus {
  database_bytes: number;
  tls_files: number;
  config_included: boolean;
  /**
   * Identifies the encryption key this host's stored credentials are under. A
   * restore needs the same one, so this is what to compare between two hosts.
   */
  key_fingerprint?: string;
  schema_version: number;
  mcpd_version: string;
  instance?: string;
  pending?: BackupPending;
  /** The floor the host enforces, so the form does not name its own number. */
  min_passphrase: number;
}

/**
 * Reads an error out of a response that was not meant to carry JSON.
 *
 * The download and upload paths below do not go through `request`, because one
 * answers with a file and the other sends multipart rather than JSON. They
 * still have to fail the same way, or a caller would have two kinds of error
 * to handle for no reason a user could see.
 */
async function failure(response: Response): Promise<ApiError> {
  let body: Record<string, unknown> = {};
  try {
    body = await response.json();
  } catch {
    // A non-JSON body from a proxy, or from a stream that was cut off.
  }
  return new ApiError(
    response.status,
    String(body.error ?? `http_${response.status}`),
    String(body.detail ?? body.error ?? response.statusText),
    body.correlation_id as string | undefined,
  );
}

/** The name the host offered the download under, or a sensible fallback. */
function filenameFrom(response: Response): string {
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = /filename="?([^";]+)"?/.exec(disposition);
  return match?.[1] ?? "mcpd-backup.mcpdbak";
}

/**
 * Takes a backup and hands back the file.
 *
 * A blob rather than a link the browser follows, because the request needs the
 * CSRF header and a passphrase in its body, and a plain navigation carries
 * neither.
 */
export async function downloadBackup(
  passphrase: string,
): Promise<{ blob: Blob; filename: string }> {
  const headers = new Headers({ "Content-Type": "application/json" });
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);

  const response = await fetch("/api/backup", {
    method: "POST",
    headers,
    credentials: "include",
    body: JSON.stringify({ passphrase }),
  });
  if (!response.ok) throw await failure(response);
  return { blob: await response.blob(), filename: filenameFrom(response) };
}

/**
 * Uploads an archive to be applied on the next start.
 *
 * The passphrase is appended first on purpose: the host streams the file
 * straight into the decryption rather than holding it anywhere, so it has to
 * have the passphrase by the time the bytes arrive.
 */
export async function stageRestore(
  archive: File,
  passphrase: string,
): Promise<{ status: string; pending: BackupPending; note: string }> {
  const form = new FormData();
  form.append("passphrase", passphrase);
  form.append("archive", archive);

  const headers = new Headers();
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);

  const response = await fetch("/api/backup/restore", {
    method: "POST",
    headers,
    credentials: "include",
    body: form,
  });
  if (!response.ok) throw await failure(response);
  return response.json();
}

/**
 * One recorded tool call.
 *
 * Deliberately without arguments or results. A row says a named principal
 * called a named tool and how it ended; what was asked for is not kept, which
 * is the same line the logging draws.
 */
export interface ToolCall {
  id: number;
  at: string;
  principal: string;
  role?: string;
  plugin: string;
  tool: string;
  outcome: "ok" | "error" | "denied" | "rate_limited";
  /**
   * Microseconds, and absent for a call that never ran — refused by the gate
   * or by a rate limit. Absent is not zero: an in-process plugin answering in
   * 63us is a real measurement that rounds to zero milliseconds.
   */
  duration_us?: number;
  correlation_id?: string;
}

/** One caller's activity, for deciding whether a credential should still exist. */
export interface Caller {
  principal: string;
  role?: string;
  calls: number;
  errors: number;
  denied: number;
  first_seen: string;
  last_seen: string;
  /** What it actually reached, which is not what it is permitted to reach. */
  plugins?: string[];
}

/** One window in which this host approves changes without asking. */
export interface Bypass {
  id: string;
  plugin?: string;
  ceiling: string;
  reason?: string;
  created_by: string;
  created_at: string;
  expires_at: string;
  /** Computed by the host: a window closes by the clock, not by a stored flag. */
  active: boolean;
  seconds_left: number;
  /** How many changes it let through, counted from the operations it authorised. */
  approved: number;
}

export interface BypassStatus {
  active: boolean;
  current?: Bypass;
  recent: Bypass[];
  max_minutes: number;
}

/** One published release, as the update check reports it. */
export interface Release {
  version: string;
  name?: string;
  url?: string;
  published_at?: string;
  notes?: string;
}

export interface UpdateStatus {
  enabled: boolean;
  current: string;
  latest?: string;
  update_available: boolean;
  newer?: Release[];
  checked_at?: string;
  error?: string;
  /** False for a build whose version cannot be ordered, such as `dev`. */
  comparable: boolean;
}

/** Resource usage, read from the Go runtime and /proc. */
/**
 * One histogram, already converted from the cumulative buckets Prometheus
 * exposes to the count that fell in each one. The host does that conversion
 * so the browser never has to know the exposition format's conventions.
 */
export interface Distribution {
  count: number;
  sum: number;
  buckets: Bucket[];
  /** Interpolated within their bucket, so no finer than the boundaries. */
  p50: number;
  p95: number;
}

/** One bar. `le` is null for the overflow bucket, which has no upper bound. */
export interface Bucket {
  le: number | null;
  count: number;
}

/** How a call ended. Kept as four numbers because they call for four actions. */
export interface Outcomes {
  ok: number;
  error: number;
  denied: number;
  rate_limited: number;
}

export interface ToolStats {
  plugin: string;
  tool: string;
  calls: Outcomes;
  duration?: Distribution;
  /** Absent until something has been measured; not the same as zero bytes. */
  result_bytes?: Distribution;
}

export interface UpstreamStats {
  plugin: string;
  outcome: string;
  duration: Distribution;
}

export interface CacheStats {
  plugin: string;
  kind: string;
  hit: number;
  miss: number;
  /** Joined a fetch already in flight rather than starting a second one. */
  shared: number;
}

export interface Performance {
  tools: ToolStats[];
  upstream: UpstreamStats[];
  cache: CacheStats[];
  /** The ceiling a plugin builds against. Sent so this never hardcodes it. */
  result_budget_bytes: number;
}

export interface Resources {
  version: string;
  started_at: string;
  uptime_seconds: number;
  goroutines: number;
  os_threads: number;
  num_cpu: number;
  gomaxprocs: number;
  heap_in_use_bytes: number;
  heap_alloc_bytes: number;
  stack_in_use_bytes: number;
  sys_bytes: number;
  total_alloc_bytes: number;
  resident_bytes?: number;
  memory_limit_bytes?: number;
  gc_cycles: number;
  gc_pause_total_ms: number;
  last_gc?: string;
  gc_cpu_percent: number;
  cpu_seconds?: number;
  open_files?: number;
}

/**
 * A certificate this host trusts on top of the system roots.
 *
 * Public material: the server presents it to everyone who connects, so the PEM
 * is here in full. It is what lets somebody check that what mcpd trusts is what
 * the appliance actually serves.
 */
export interface Certificate {
  id: string;
  name: string;
  pem: string;
  subject: string;
  issuer: string;
  /** SHA-256 of the certificate, lowercase hex. */
  fingerprint: string;
  not_before: string;
  not_after: string;
  status: "valid" | "expiring" | "expired" | "not_yet_valid";
  /** Its own issuer — the ordinary shape of an appliance's certificate. */
  self_signed: boolean;
  is_ca: boolean;
  /**
   * Whether it can be the root of a chain at all. False means trusting it
   * cannot fix anything, which is worth saying before somebody waits to find
   * out.
   */
  anchors: boolean;
  added_by: string;
  added_at: string;
}

/** One of the two fields carries the certificate; never both. */
export interface AddCertificate {
  name: string;
  /** Text as pasted, headers and all. */
  pem?: string;
  /** A binary file, base64 for the trip. */
  base64?: string;
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

  /** What the signed-out page may offer: providers, and whether to show a form. */
  authOptions: () => request<AuthOptions>("/api/auth/options"),

  /** Asks for an account. Not `registerFirst`, which claims an unclaimed host. */
  register: (email: string, password: string, displayName?: string) =>
    request<Session>("/api/register", {
      method: "POST",
      body: JSON.stringify({ email, password, display_name: displayName ?? "" }),
    }),

  /**
   * Begins a provider sign-in. The reply is a URL to send the browser to; the
   * cookie that makes its state usable was set on this response.
   */
  ssoStart: (provider: ProviderName) =>
    request<{ authorization_url: string }>(
      `/api/auth/sso/${encodeURIComponent(provider)}/start`, { method: "POST" }),

  /** The providers this account can sign in with, and the ones it could add. */
  identities: () =>
    request<{ identities: LinkedIdentity[]; available: ProviderDescriptor[] }>(
      "/api/account/identities"),

  /**
   * Begins attaching a provider to the signed-in account. The only way an
   * account that already exists gains one — mcpd never adopts an account on
   * the strength of a matching email address.
   */
  linkIdentity: (provider: ProviderName, returnTo = "/profile") =>
    request<{ authorization_url: string }>(
      `/api/account/identities/${encodeURIComponent(provider)}/start`,
      { method: "POST", body: JSON.stringify({ return_to: returnTo }) }),

  unlinkIdentity: (provider: ProviderName) =>
    request<void>(`/api/account/identities/${encodeURIComponent(provider)}`,
      { method: "DELETE" }),

  /** The exact addresses to paste into each provider's console. */
  redirectURIs: () =>
    request<{
      base: string;
      redirect_uris: Partial<Record<ProviderName, string>>;
      /** Providers that will refuse the address above, and why. */
      refusals?: Partial<Record<ProviderName, string>>;
    }>("/api/auth/redirect-uris"),

  registrations: () =>
    request<{ registrations: PendingRegistration[]; count: number }>("/api/registrations"),

  /**
   * Approving may put the account into groups, which is what keeps it one
   * decision: without it, whoever approves has to go to another page to say
   * what the account may reach.
   */
  approveRegistration: (id: string, groups: string[] = []) =>
    request<User>(`/api/registrations/${encodeURIComponent(id)}/approve`,
      { method: "POST", body: JSON.stringify({ groups }) }),

  rejectRegistration: (id: string) =>
    request<void>(`/api/registrations/${encodeURIComponent(id)}/reject`,
      { method: "POST" }),

  groups: () => request<{ groups: Group[]; count: number }>("/api/groups"),

  group: (id: string) =>
    request<{ group: Group; members: GroupMember[] }>(
      `/api/groups/${encodeURIComponent(id)}`),

  createGroup: (body: { name: string; description?: string; plugins?: string[] }) =>
    request<Group>("/api/groups", { method: "POST", body: JSON.stringify(body) }),

  updateGroup: (
    id: string,
    body: { name?: string; description?: string; plugins?: string[] },
  ) =>
    request<Group>(`/api/groups/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deleteGroup: (id: string) =>
    request<void>(`/api/groups/${encodeURIComponent(id)}`, { method: "DELETE" }),

  addGroupMember: (id: string, kind: "user" | "key", memberId: string) =>
    request<void>(`/api/groups/${encodeURIComponent(id)}/members`, {
      method: "POST",
      body: JSON.stringify({ kind, id: memberId }),
    }),

  removeGroupMember: (id: string, kind: "user" | "key", memberId: string) =>
    request<void>(
      `/api/groups/${encodeURIComponent(id)}/members/${kind}/${encodeURIComponent(memberId)}`,
      { method: "DELETE" }),

  keys: () => request<{ keys: ApiKey[]; count: number }>("/api/keys"),

  /**
   * Creates a key. The `secret` in the reply is the only time it exists — it is
   * stored as a digest and no endpoint reads it back, so a caller that drops it
   * cannot get it again.
   */
  createKey: (body: CreateKey) =>
    request<{ key: ApiKey; secret: string }>("/api/keys", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updateKey: (id: string, body: UpdateKey) =>
    request<ApiKey>(`/api/keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  /** Revokes rather than deletes, so the trail can still name what acted. */
  revokeKey: (id: string) =>
    request<ApiKey>(`/api/keys/${encodeURIComponent(id)}/revoke`, { method: "POST" }),

  certificates: () =>
    request<{ certificates: Certificate[]; count: number }>("/api/certificates"),

  /**
   * Adds a certificate. The host rebuilds what it trusts and remounts the
   * plugins on the spot, so nothing here asks for a restart.
   */
  addCertificate: (body: AddCertificate) =>
    request<Certificate>("/api/certificates", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  /** Deleted rather than disabled: trust is a thing to be able to take back. */
  deleteCertificate: (id: string) =>
    request<void>(`/api/certificates/${encodeURIComponent(id)}`, { method: "DELETE" }),

  users: () => request<{ users: User[]; count: number }>("/api/users"),

  createUser: (body: CreateUser) =>
    request<User>("/api/users", { method: "POST", body: JSON.stringify(body) }),

  updateUser: (id: string, body: UpdateUser) =>
    request<User>(`/api/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  /** Renames the calling account only, which is why it takes `read`. */
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

  /** What this process is costing the machine it runs on. */
  resources: () => request<Resources>("/api/resources"),
  performance: () => request<Performance>("/api/performance"),

  /** The running version against what has been published. */
  updates: () => request<UpdateStatus>("/api/updates"),

  /** Ask now rather than when the cache expires. */
  checkUpdates: () =>
    request<UpdateStatus>("/api/updates/check", { method: "POST" }),

  /**
   * Drain and exit, so the supervisor starts a new process. The reply comes
   * back before the process goes; the page reconnects on its own.
   */
  restart: () =>
    request<{ status: string; note: string }>("/api/restart", { method: "POST" }),

  testNotification: () =>
    request<{ status: string; note: string }>("/api/notifications/test", {
      method: "POST",
    }),

  bypassStatus: () => request<BypassStatus>("/api/approval-policy/bypass"),

  openBypass: (body: {
    minutes: number; ceiling: string; reason: string; plugin?: string;
  }) => request<Bypass>("/api/approval-policy/bypass", {
    method: "POST", body: JSON.stringify(body),
  }),

  revokeBypasses: () =>
    request<{ closed: number }>("/api/approval-policy/bypass", { method: "DELETE" }),

  calls: (params: {
    principal?: string; plugin?: string; outcome?: string;
    hours?: number; limit?: number; before?: string;
  } = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    const query = q.toString();
    return request<{ calls: ToolCall[]; count: number; next: string }>(
      `/api/calls${query ? `?${query}` : ""}`);
  },

  callers: (days = 7) =>
    request<{ callers: Caller[]; count: number; days: number }>(
      `/api/calls/callers?days=${days}`),

  backupStatus: () => request<BackupStatus>("/api/backup"),

  cancelRestore: () =>
    request<void>("/api/backup/restore", { method: "DELETE" }),

  saveSettings: (values: Record<string, string>, clearSecrets: string[] = []) =>
    request<SaveResult>("/api/settings", {
      method: "PUT",
      body: JSON.stringify({ values, clear_secrets: clearSecrets }),
    }),

  tunnelStart: () =>
    request<TunnelStatus>("/api/tunnel/start", { method: "POST" }),

  tunnelStop: () => request<TunnelStatus>("/api/tunnel/stop", { method: "POST" }),

  createTunnel: (name: string, plugin: string, workspaceID?: string, account?: string) =>
    request<OpenAITunnel>("/api/tunnels", {
      method: "POST",
      body: JSON.stringify({
        name, plugin, workspace_id: workspaceID ?? "", account: account ?? "",
      }),
    }),

  assignTunnel: (id: string, plugin: string, account?: string) =>
    request<{ status: string }>(`/api/tunnels/${encodeURIComponent(id)}/assign`, {
      method: "POST",
      body: JSON.stringify({ plugin, account: account ?? "" }),
    }),

  // The account is carried so the tunnel is deleted from the organisation it
  // actually lives in. Deleting from the wrong one cannot be taken back.
  deleteTunnel: (id: string, account?: string) =>
    request<{ status: string }>(
      `/api/tunnels/${encodeURIComponent(id)}` +
        (account ? `?account=${encodeURIComponent(account)}` : ""),
      { method: "DELETE" },
    ),

  chatgptAccounts: () =>
    request<{ accounts: ChatGPTAccount[]; plugins: string[] }>("/api/chatgpt/accounts"),

  addChatGPTAccount: (body: ChatGPTAccountBody) =>
    request<ChatGPTAccount>("/api/chatgpt/accounts", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updateChatGPTAccount: (id: string, body: ChatGPTAccountBody) =>
    request<ChatGPTAccount>(`/api/chatgpt/accounts/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  removeChatGPTAccount: (id: string) =>
    request<{ status: string }>(`/api/chatgpt/accounts/${encodeURIComponent(id)}`, {
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
    request<{
      instances: PluginInstance[];
      count: number;
      stale_removals?: StaleRemoval[];
    }>("/api/instances"),

  addInstance: (name: string, type: string) =>
    request<{ status: string; note?: string }>(
      "/api/instances", { method: "POST", body: JSON.stringify({ name, type }) }),

  setInstanceEnabled: (name: string, enabled: boolean) =>
    request<{ status: string }>(`/api/instances/${encodeURIComponent(name)}`, {
      method: "PATCH", body: JSON.stringify({ enabled }),
    }),

  /**
   * Removes an instance, or overrides the file's declaration of one.
   * `acknowledgeRequired` is needed only where the file marks it required.
   */
  removeInstance: (name: string, acknowledgeRequired = false) =>
    request<{ status: string }>(
      `/api/instances/${encodeURIComponent(name)}`
      + (acknowledgeRequired ? "?acknowledge_required=true" : ""),
      { method: "DELETE" },
    ),

  /** Undoes a removal, putting the plugin back under the file's declaration. */
  restoreInstance: (name: string) =>
    request<{ status: string; note?: string }>(
      `/api/instances/${encodeURIComponent(name)}/restore`, { method: "POST" }),

  /** Browses the public catalogues. `refresh` bypasses the cache for one request. */
  catalog: (q: {
    search?: string;
    cursor?: string;
    limit?: number;
    refresh?: boolean;
    /** "most-used", "recently-updated" or "name". Omitted takes the default. */
    sort?: string;
    /** One catalogue by name. Omitted covers them all. */
    source?: string;
  } = {}) => {
    const params = new URLSearchParams();
    if (q.search) params.set("q", q.search);
    if (q.cursor) params.set("cursor", q.cursor);
    if (q.limit) params.set("limit", String(q.limit));
    if (q.refresh) params.set("refresh", "1");
    if (q.sort) params.set("sort", q.sort);
    if (q.source) params.set("source", q.source);
    const query = params.toString();
    return request<CatalogPage>(query ? `/api/catalog?${query}` : "/api/catalog");
  },

  /**
   * One entry, with the document that would be imported. The name carries a
   * slash and the route is a trailing wildcard, so the separator is left alone
   * -- encoding it would send `%2F` and address a server nobody published.
   */
  catalogEntry: (name: string, refresh = false) => {
    const path = name.split("/").map(encodeURIComponent).join("/");
    return request<CatalogDetail>(
      refresh ? `/api/catalog/${path}?refresh=1` : `/api/catalog/${path}`,
    );
  },

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
   * Declares a header this host must send that the published document did not.
   * Records the declaration only; the value is typed on the settings page.
   */
  addMCPServerHeader: (
    server: string, name: string, description: string, secret: boolean,
  ) =>
    request<{ status: string; note?: string }>(
      `/api/mcp-servers/${encodeURIComponent(server)}/headers`,
      { method: "POST", body: JSON.stringify({ name, description, secret }) },
    ),

  removeMCPServerHeader: (server: string, name: string) =>
    request<{ status: string }>(
      `/api/mcp-servers/${encodeURIComponent(server)}/headers/${encodeURIComponent(name)}`,
      { method: "DELETE" },
    ),

  /**
   * Records a decision about one tool. A 409 means the far end changed the
   * descriptor under the operator: show them what changed, never resend with
   * the new hash.
   */
  classifyMCPTool: (
    server: string, tool: string, state: MCPToolState, descriptorHash: string,
  ) =>
    request<{ status: string }>(
      `/api/mcp-servers/${encodeURIComponent(server)}/tools/${encodeURIComponent(tool)}`,
      { method: "PATCH", body: JSON.stringify({ state, descriptor_hash: descriptorHash }) },
    ),

  approvalPolicy: () => request<ApprovalPolicy>("/api/approval-policy"),

  /**
   * Replaces the whole set, which is the only unit at which "no two rules cover
   * the same thing" can be checked.
   */
  saveApprovalPolicy: (rules: ApprovalRule[]) =>
    request<ApprovalPolicy>("/api/approval-policy", {
      method: "PUT",
      body: JSON.stringify({ rules }),
    }),

  /** Asks what the policy would do. Computes over configuration and changes nothing. */
  evaluateApprovalPolicy: (query: EvaluateRequest) =>
    request<PolicyEvaluation>("/api/approval-policy/evaluate", {
      method: "POST",
      body: JSON.stringify(query),
    }),
};
