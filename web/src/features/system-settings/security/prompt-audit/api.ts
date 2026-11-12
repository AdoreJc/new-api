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

export type PromptAuditEndpoint = {
  id: string
  name: string
  base_url: string
  model: string
  timeout_ms: number
  input_limit: number
  enabled: boolean
}

export type PromptAuditConfig = {
  enabled: boolean
  mode: string
  all_groups: boolean
  groups: string[]
  scanners: string[]
  store_full_prompt: boolean
  retention_days: number
  worker_count: number
  queue_capacity: number
  max_chunks_per_request: number
  scan_scope: string
  strict_jailbreak_mapping: boolean
  endpoints: PromptAuditEndpoint[]
  config_version: number
  updated_at: number
  updated_by: number
}

export type PromptAuditConfigResponse = {
  config: PromptAuditConfig
  has_token: Record<string, boolean>
  scanners: string[]
}

export type PromptAuditTokenChange = { value?: string; clear?: boolean }

export type PromptAuditConfigRequest = {
  config: PromptAuditConfig
  token_changes: Record<string, PromptAuditTokenChange>
  expected_config_version: number
}

export type PromptAuditRuntime = {
  enabled: boolean
  mode: string
  config_version: number
  worker_count: number
  active_workers: number
  queue_depth: number
  queue_capacity: number
  queued_bytes: number
  queued_bytes_limit: number
  queue_in_memory: boolean
  enqueued: number
  dropped: number
  skipped: number
  skipped_too_large: number
  skipped_no_text: number
  skipped_agent_context_only: number
  scanned: number
  passed: number
  flagged: number
  critical: number
  failed: number
  timeouts: number
  failovers: number
  retries: number
  recorded: number
  record_failed: number
  chunks_cached: number
  chunks_over_budget: number
  latency_avg_ms: number
  latency_max_ms: number
}

export type PromptAuditProbeResult = {
  ok: boolean
  latency_ms: number
  safety?: string
  error?: string
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

export function getPromptAuditConfig() {
  return unwrap(
    api.get<ApiResponse<PromptAuditConfigResponse>>('/api/prompt_audit/config'),
    t('Failed to load prompt audit settings')
  )
}

export function updatePromptAuditConfig(request: PromptAuditConfigRequest) {
  return unwrap(
    api.put<ApiResponse<PromptAuditConfigResponse>>(
      '/api/prompt_audit/config',
      request
    ),
    t('Failed to save prompt audit settings')
  )
}

export function getPromptAuditRuntime() {
  return unwrap(
    api.get<ApiResponse<PromptAuditRuntime>>('/api/prompt_audit/runtime'),
    t('Failed to load prompt audit status')
  )
}

export function probePromptAuditEndpoint(
  endpoint: PromptAuditEndpoint,
  token: string
) {
  return unwrap(
    api.post<ApiResponse<PromptAuditProbeResult>>(
      '/api/prompt_audit/endpoints/probe',
      { endpoint, token }
    ),
    t('Failed to test the Guard endpoint')
  )
}
