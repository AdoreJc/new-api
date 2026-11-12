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
  pageSize: number
) {
  return unwrap(
    api.get<ApiResponse<{ items: PromptAuditEvent[]; total: number }>>(
      '/api/prompt_audit/events',
      { params: { ...filter, p: page, page_size: pageSize } }
    ),
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

export function previewPromptAuditDeletion(filter: PromptAuditEventFilter) {
  return unwrap(
    api.post<ApiResponse<{ total: number; max_id: number }>>(
      '/api/prompt_audit/events/delete-preview',
      filter
    ),
    t('Failed to preview the deletion')
  )
}

export function deletePromptAuditEventsByFilter(
  filter: PromptAuditEventFilter,
  maxId: number
) {
  return unwrap(
    api.post<ApiResponse<{ deleted: number }>>(
      '/api/prompt_audit/events/delete-by-filter',
      { filter, max_id: maxId }
    ),
    t('Failed to delete prompt audit events')
  )
}
