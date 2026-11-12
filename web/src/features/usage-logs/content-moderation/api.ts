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

export type ContentModerationEvent = {
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
  action: string
  flagged: boolean
  allowlisted: boolean
  highest_category: string
  highest_score: number
  /** JSON object of category scores. */
  category_scores: string
  /** JSON object of the thresholds in effect. */
  thresholds: string
  input_hash: string
  input_excerpt: string
  input_length: number
  image_count: number
  engine_model: string
  latency_ms: number
  queue_delay_ms: number
  error: string
  violation_count: number
  auto_disabled: boolean
  email_sent: boolean
  config_version: number
}

/** Filters shared by listing and filtered deletion. */
export type ContentModerationEventFilter = {
  flagged?: 'true' | 'false'
  action?: string
  category?: string
  user_id?: number
  group?: string
  model_name?: string
  request_id?: string
  input_hash?: string
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

export function getContentModerationEvents(
  filter: ContentModerationEventFilter,
  page: number,
  pageSize: number
) {
  return unwrap(
    api.get<ApiResponse<{ items: ContentModerationEvent[]; total: number }>>(
      '/api/content_moderation/events',
      { params: { ...filter, p: page, page_size: pageSize } }
    ),
    t('Failed to load content moderation events')
  )
}

export function deleteContentModerationEvent(id: number) {
  return unwrap(
    api.delete<ApiResponse<{ deleted: number }>>(
      `/api/content_moderation/events/${id}`
    ),
    t('Failed to delete content moderation events')
  )
}

export function previewContentModerationDeletion(
  filter: ContentModerationEventFilter
) {
  return unwrap(
    api.post<ApiResponse<{ total: number; max_id: number }>>(
      '/api/content_moderation/events/delete-preview',
      filter
    ),
    t('Failed to preview the deletion')
  )
}

export function deleteContentModerationEventsByFilter(
  filter: ContentModerationEventFilter,
  maxId: number
) {
  return unwrap(
    api.post<ApiResponse<{ deleted: number }>>(
      '/api/content_moderation/events/delete-by-filter',
      { filter, max_id: maxId }
    ),
    t('Failed to delete content moderation events')
  )
}

export function updateContentModerationAllowlist(
  userId: number,
  action: 'add' | 'remove'
) {
  return unwrap(
    api.post<ApiResponse<unknown>>('/api/content_moderation/allowlist', {
      user_id: userId,
      action,
    }),
    t('Failed to update the allowlist')
  )
}

export function deleteContentModerationHash(hash: string) {
  return unwrap(
    api.delete<ApiResponse<{ deleted: number }>>(
      `/api/content_moderation/hashes/${hash}`
    ),
    t('Failed to remove the hash')
  )
}

/** Labels for event actions, as English i18n keys. */
export const CONTENT_MODERATION_ACTION_LABELS: Record<string, string> = {
  allow: 'Recorded',
  block: 'Blocked',
  hash_block: 'Blocked (remembered input)',
  error: 'API failed',
}

/** Parses a stored JSON score map; a malformed value yields no entries. */
export function parseScores(value: string | undefined): Array<[string, number]> {
  if (!value) return []
  try {
    const parsed: unknown = JSON.parse(value)
    if (parsed === null || typeof parsed !== 'object') return []
    return Object.entries(parsed as Record<string, unknown>)
      .filter((entry): entry is [string, number] => typeof entry[1] === 'number')
      .sort((left, right) => right[1] - left[1])
  } catch {
    return []
  }
}
