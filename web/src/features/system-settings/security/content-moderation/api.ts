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

export const RUNTIME_QUERY_KEY = ['content-moderation', 'runtime'] as const

export type ContentModerationMode = 'off' | 'observe' | 'pre_block'

export type ContentModerationModelFilter = {
  type: 'all' | 'include' | 'exclude'
  models: string[]
}

export type ContentModerationConfig = {
  mode: ContentModerationMode
  base_url: string
  model: string
  timeout_ms: number
  retry_count: number
  include_images: boolean
  sample_rate: number
  all_groups: boolean
  groups: string[]
  model_filter: ContentModerationModelFilter
  thresholds: Record<string, number>
  worker_count: number
  queue_size: number
  record_non_hits: boolean
  hit_retention_days: number
  non_hit_retention_days: number
  hash_blocklist_enabled: boolean
  email_on_hit: boolean
  auto_disable_enabled: boolean
  disable_threshold: number
  violation_window_hours: number
  allowlist_user_ids: number[]
  config_version: number
  updated_at: number
  updated_by: number
}

/** API keys are never returned; each one is identified by a hash prefix. */
export type ContentModerationConfigResponse = {
  config: ContentModerationConfig
  keys: Array<{ id: string }>
  categories: string[]
  default_thresholds: Record<string, number>
}

export type ContentModerationKeyChanges = {
  add: string[]
  remove: string[]
  clear: boolean
}

export type ContentModerationConfigRequest = {
  config: ContentModerationConfig
  key_changes: ContentModerationKeyChanges
  expected_config_version: number
}

export type ContentModerationKeyStatus = {
  id: string
  status: 'untested' | 'healthy' | 'failing' | 'frozen'
  frozen_until?: number
  successes: number
  failures: number
  last_http_status: number
  last_error?: string
  last_latency_ms: number
  last_checked_at?: number
}

export type ContentModerationRuntime = {
  mode: ContentModerationMode
  config_version: number
  worker_count: number
  active_workers: number
  queue_depth: number
  queue_size: number
  queued_bytes: number
  queued_bytes_limit: number
  hash_blocklist: number
  checked: number
  skipped: number
  skipped_too_large: number
  skipped_empty: number
  sampled_out: number
  no_keys: number
  enqueued: number
  dropped: number
  passed: number
  flagged: number
  blocked: number
  hash_blocked: number
  failed: number
  timeouts: number
  calls: number
  auto_disabled: number
  emails_sent: number
  emails_failed: number
  recorded: number
  record_failed: number
  latency_avg_ms: number
  latency_max_ms: number
  sync_avg_ms: number
  keys: ContentModerationKeyStatus[]
}

export type ContentModerationProbeRequest = {
  base_url: string
  model: string
  timeout_ms: number
  keys: string[]
  text: string
}

export type ContentModerationProbeResult = {
  keys: Array<{
    id: string
    ok: boolean
    http_status?: number
    latency_ms: number
    error?: string
  }>
  flagged: boolean
  highest_category?: string
  highest_score: number
  scores?: Record<string, number>
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

export function getContentModerationConfig() {
  return unwrap(
    api.get<ApiResponse<ContentModerationConfigResponse>>(
      '/api/content_moderation/config'
    ),
    t('Failed to load content moderation settings')
  )
}

export function updateContentModerationConfig(
  request: ContentModerationConfigRequest
) {
  return unwrap(
    api.put<ApiResponse<ContentModerationConfigResponse>>(
      '/api/content_moderation/config',
      request
    ),
    t('Failed to save content moderation settings')
  )
}

export function getContentModerationRuntime() {
  return unwrap(
    api.get<ApiResponse<ContentModerationRuntime>>(
      '/api/content_moderation/runtime'
    ),
    t('Failed to load content moderation status')
  )
}

export function probeContentModeration(request: ContentModerationProbeRequest) {
  return unwrap(
    api.post<ApiResponse<ContentModerationProbeResult>>(
      '/api/content_moderation/probe',
      request
    ),
    t('Failed to test the moderation API')
  )
}

export function clearContentModerationHashes() {
  return unwrap(
    api.delete<ApiResponse<{ deleted: number }>>(
      '/api/content_moderation/hashes'
    ),
    t('Failed to clear the hash blocklist')
  )
}
