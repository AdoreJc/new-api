/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
export type ContentAuditEndpoint = {
  id: string
  name: string
  base_url: string
  model: string
  enabled: boolean
  has_token: boolean
}

export type ContentAuditConfig = {
  enabled: boolean
  all_groups: boolean
  groups: string[]
  scanners: string[]
  worker_count: number
  queue_capacity: number
  queue_max_mb: number
  timeout_ms: number
  max_chunk_runes: number
  max_body_mb: number
  retention_days: number
  endpoints: ContentAuditEndpoint[]
  config_version: number
  updated_at: number
  updated_by: number
}

export type ContentAuditConfigResponse = {
  config: ContentAuditConfig
  scanners: string[]
}

export type ContentAuditEndpointUpdate = Omit<
  ContentAuditEndpoint,
  'has_token'
> & {
  token?: string
  clear_token?: boolean
}

export type ContentAuditSaveRequest = {
  config: Omit<ContentAuditConfig, 'endpoints'> & {
    endpoints: ContentAuditEndpointUpdate[]
  }
  expected_config_version: number
}

export type ContentAuditProbeRequest = {
  endpoint: {
    id: string
    base_url: string
    model: string
    token?: string
  }
}

export type ContentAuditProbeResult = {
  ok: boolean
  latency_ms: number
  model_listed: boolean
  safety: string
  error: string
}

export type ContentAuditLatency = {
  p50: number
  p95: number
  p99: number
  max: number
  samples: number
}

export type ContentAuditCounters = {
  enqueued: number
  dropped_queue_full: number
  dropped_queue_bytes: number
  skipped_body_too_large: number
  skipped_unsupported_format: number
  skipped_invalid_json: number
  skipped_no_text: number
  skipped_out_of_scope: number
  scanned: number
  allowed: number
  warned: number
  blocked: number
  failed: number
  retries: number
  failovers: number
  credential_pattern_hits: number
  events_written: number
  event_write_failed: number
}

export type ContentAuditRuntime = {
  enabled: boolean
  config_version: number
  workers: number
  started_at: number
  queue_depth: number
  queue_capacity: number
  queue_bytes: number
  queue_max_bytes: number
  counters: ContentAuditCounters
  guard_latency_ms: ContentAuditLatency
  entry_latency_us: ContentAuditLatency
  /** Set when the stored config failed to load; auditing is then off. */
  load_error?: string
}

export type ContentAuditDecision = 'warn' | 'block'

export type ContentAuditEvent = {
  id: number
  created_at: number
  request_id: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  using_group: string
  channel_id: number
  relay_format: string
  endpoint: string
  model_name: string
  decision: ContentAuditDecision
  safety: string
  risk_level: string
  score: number
  categories: string[] | null
  matched_scanners: string[] | null
  unknown_categories: string[] | null
  guard_endpoint_id: string
  scanner_version: string
  config_version: number
  chunk_total: number
  latency_ms: number
  prompt_hash: string
  redacted_preview: string
  prompt_length: number
  message_count: number
}

export type ContentAuditEventFilters = {
  p: number
  page_size: number
  decision?: ContentAuditDecision
  user_id?: string
  group?: string
  request_id?: string
  prompt_hash?: string
  start_timestamp?: number
  end_timestamp?: number
}

export type ContentAuditEventPage = {
  page: number
  page_size: number
  total: number
  items: ContentAuditEvent[]
}
