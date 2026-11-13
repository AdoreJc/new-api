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
import { t } from 'i18next'

import type { ApiResponse } from '@/features/profile/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

export type PromptAuditEvent = {
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
  mode: string
  decision: string
  risk_level: string
  action: string
  safety: string
  categories: string
  matched_scanners: string
  unknown_categories: string
  guard_endpoint_id: string
  scanner_version: string
  config_version: number
  chunk_total: number
  chunks_scanned: number
  scan_incomplete: boolean
  /** JSON list of the chunks that were not passed; never prompt text. */
  hit_chunks?: string
  latency_ms: number
  prompt_hash: string
  redacted_preview: string
  prompt_length: number
  message_count: number
  full_prompt?: string
  /** Audit policy rule that applied; empty for older events. */
  rule_id?: string
  /** observe, block, reroute or ignore; empty for older events. */
  policy_action?: string
  /** Only the single event endpoint returns the issues. */
  issues?: PromptAuditIssue[]
}

/** One finding in reviewer terms; title and action are English i18n keys. */
export type PromptAuditIssue = {
  category: string
  title: string
  severity: 'critical' | 'high' | 'medium' | 'low'
  action: string
  /** 1-based scan positions of the chunks that named it. */
  chunks: number[]
}

export type PromptAuditKeywordMode = 'plain' | 'regex'

/**
 * One page of events. A plain search on the default fields pages with a
 * total; a regex or full prompt search scans rows and pages with a cursor
 * instead: when truncated, rows below next_cursor were not read yet.
 */
export type PromptAuditEventsPage = {
  items: PromptAuditEvent[]
  total?: number
  truncated?: boolean
  next_cursor?: number
  scanned?: number
  stop_reason?: 'page_full' | 'scan_limit' | 'time_budget'
}

/** Filters shared by listing and filtered deletion. */
export type PromptAuditEventFilter = {
  decision?: string
  risk_level?: string
  user_id?: number
  group?: string
  model_name?: string
  request_id?: string
  prompt_hash?: string
  keyword?: string
  /** plain (default) matches a substring; regex matches an RE2 expression. */
  keyword_mode?: PromptAuditKeywordMode
  /** Comma-separated search fields; empty searches every field but full_prompt. */
  keyword_fields?: string
  /** Regex mode only; plain mode always ignores case. */
  case_sensitive?: boolean
  start_time?: number
  end_time?: number
}

async function unwrap<T>(
  request: Promise<{ data: ApiResponse<T> }>,
  fallback: string
): Promise<T> {
  const response = await request
  if (!response.data.success || response.data.data === undefined) {
    throw createServerError(response.data, fallback)
  }
  return response.data.data
}

export function getPromptAuditEvents(
  filter: PromptAuditEventFilter,
  page: number,
  pageSize: number,
  cursor?: number
) {
  return unwrap(
    api.get<ApiResponse<PromptAuditEventsPage>>('/api/prompt_audit/events', {
      params: { ...filter, p: page, page_size: pageSize, cursor },
    }),
    t('Failed to load prompt audit events')
  )
}

export function getPromptAuditEvent(id: number) {
  return unwrap(
    api.get<ApiResponse<PromptAuditEvent>>(`/api/prompt_audit/events/${id}`),
    t('Failed to load the prompt audit event')
  )
}

export function deletePromptAuditEvent(id: number) {
  return unwrap(
    api.delete<ApiResponse<{ deleted: number }>>(
      `/api/prompt_audit/events/${id}`
    ),
    t('Failed to delete prompt audit events')
  )
}

export function batchDeletePromptAuditEvents(ids: number[]) {
  return unwrap(
    api.post<ApiResponse<{ deleted: number }>>(
      '/api/prompt_audit/events/batch-delete',
      { ids }
    ),
    t('Failed to delete prompt audit events')
  )
}

/**
 * A filtered deletion preview. The confirmation token expires after a few
 * minutes and only deletes events up to snapshot_max_id that still match the
 * same filter.
 */
export type PromptAuditDeletionPreview = {
  matched_count: number
  snapshot_max_id: number
  filter_hash: string
  confirmation_token: string
  expires_at: number
}

export function previewPromptAuditDeletion(filter: PromptAuditEventFilter) {
  return unwrap(
    api.post<ApiResponse<PromptAuditDeletionPreview>>(
      '/api/prompt_audit/events/delete-preview',
      filter
    ),
    t('Failed to preview the deletion')
  )
}

export function deletePromptAuditEventsByFilter(
  filter: PromptAuditEventFilter,
  preview: PromptAuditDeletionPreview
) {
  return unwrap(
    api.post<ApiResponse<{ deleted: number }>>(
      '/api/prompt_audit/events/delete-by-filter',
      {
        filter,
        snapshot_max_id: preview.snapshot_max_id,
        filter_hash: preview.filter_hash,
        confirmation_token: preview.confirmation_token,
      }
    ),
    t('Failed to delete prompt audit events')
  )
}

/**
 * One system prompt fingerprint of the request structure statistics. Only
 * hashes, counts, lengths, times and categories; never prompt text.
 */
export type PromptAuditStructureFingerprint = {
  /** sha256 hex, or "none" for requests without a system prompt. */
  fingerprint: string
  /** sha256 hex of the leading part of the system prompt, or "none". */
  prefix_hash: string
  first_seen_at: number
  last_seen_at: number
  request_count: number
  /** Distinct users, counted exactly up to a cap. */
  user_count: number
  user_count_capped: boolean
  system_length: number
  last_request_format: string
  last_client_class: string
}

export type PromptAuditStructureFingerprintSort = 'users' | 'requests'

export type PromptAuditStructureFingerprintsPage = {
  page: number
  page_size: number
  total: number
  items: PromptAuditStructureFingerprint[]
}

export function getPromptAuditStructureFingerprints(
  page: number,
  pageSize: number,
  sort: PromptAuditStructureFingerprintSort
) {
  return unwrap(
    api.get<ApiResponse<PromptAuditStructureFingerprintsPage>>(
      '/api/prompt_audit/structure/fingerprints',
      { params: { p: page, page_size: pageSize, sort } }
    ),
    t('Failed to load structure statistics')
  )
}

/** Counters that are request counts; the rate of each is count / requests. */
export type PromptAuditStructureSignalCounts = {
  with_system: number
  with_developer: number
  with_history_user: number
  with_history_assistant: number
  trailing_assistant: number
  with_agent_context: number
  with_tool: number
  assistant_turns_10_plus: number
  mid_system: number
  control_tokens: number
  role_markers: number
  invisible_requests: number
  encoded_requests: number
  system_under_1k: number
  system_under_4k: number
  system_under_16k: number
  system_under_64k: number
  system_64k_plus: number
}

/** Counters that are sums over the requests of the day. */
export type PromptAuditStructureSignalSums = {
  assistant_turns: number
  user_turns: number
  invisible_chars: number
  encoded_runs: number
}

/** One day of structure signals, summed over the unfiltered dimensions. */
export type PromptAuditStructureSignalDay = PromptAuditStructureSignalCounts &
  PromptAuditStructureSignalSums & {
    /** Server local date as yyyymmdd, for example 20261113. */
    stat_day: number
    requests: number
  }

export type PromptAuditStructureSignals = {
  items: PromptAuditStructureSignalDay[]
  request_formats: string[]
  client_classes: string[]
}

export type PromptAuditStructureSignalFilter = {
  start_day: number
  end_day: number
  request_format?: string
  client_class?: string
}

export function getPromptAuditStructureSignals(
  filter: PromptAuditStructureSignalFilter
) {
  return unwrap(
    api.get<ApiResponse<PromptAuditStructureSignals>>(
      '/api/prompt_audit/structure/signals',
      { params: filter }
    ),
    t('Failed to load structure statistics')
  )
}
