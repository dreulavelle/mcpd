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
  verified?: boolean;
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
  settings: Setting[];
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
  /** The systems a tunnel can be pointed at. */
  plugins: string[];
}

export interface OpenAITunnel {
  id: string;
  name: string;
  description?: string;
}

export type SettingKind =
  | "string" | "secret" | "bool" | "int" | "duration" | "enum" | "list";

export type SettingApply = "live" | "reconnect" | "restart";

export type SettingSection = "settings" | "tunnels";

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

/** ApiError carries the server's stable error code so callers can branch on it. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly detail: string,
    readonly correlationId?: string,
  ) {
    super(detail || code);
    this.name = "ApiError";
  }
}

const TOKEN_KEY = "mcpd.token";

/**
 * The token lives in sessionStorage rather than localStorage so it does not
 * outlive the browser session. It is a bearer credential for an interface that
 * approves infrastructure changes, and persisting it would leave it on disk for
 * as long as the browser profile exists.
 */
export function getToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_KEY);
  } catch {
    // Private browsing and some embedded contexts throw on access.
    return null;
  }
}

export function setToken(token: string): void {
  try {
    sessionStorage.setItem(TOKEN_KEY, token);
  } catch {
    // Nothing useful to do; the session just will not survive a reload.
  }
}

export function clearToken(): void {
  try {
    sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init?.body) headers.set("Content-Type", "application/json");

  const response = await fetch(path, { ...init, headers });
  if (response.status === 204) return undefined as T;

  let body: Record<string, unknown> = {};
  try {
    body = await response.json();
  } catch {
    // A non-JSON body from a proxy or gateway.
  }

  if (!response.ok) {
    throw new ApiError(
      response.status,
      String(body.error ?? `http_${response.status}`),
      String(body.detail ?? body.error ?? response.statusText),
      body.correlation_id as string | undefined,
    );
  }
  return body as T;
}

export const api = {
  meta: () => request<Meta>("/api/meta"),

  operations: (state?: OperationState) =>
    request<{ operations: Operation[]; count: number }>(
      state ? `/api/operations?state=${encodeURIComponent(state)}` : "/api/operations",
    ),

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

  createTunnel: (name: string, plugin: string) =>
    request<OpenAITunnel>("/api/tunnels", {
      method: "POST",
      body: JSON.stringify({ name, plugin }),
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
};
