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
import type { ApiResponse } from '@/features/profile/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type {
  ContentAuditConfigResponse,
  ContentAuditEvent,
  ContentAuditEventFilters,
  ContentAuditEventPage,
  ContentAuditProbeRequest,
  ContentAuditProbeResult,
  ContentAuditRuntime,
  ContentAuditSaveRequest,
} from './types'

const BASE = '/api/content_audit'

/** Unwraps the standard envelope, rejecting `success: false` with its source. */
function unwrap<T>(body: ApiResponse<T>): T {
  if (!body.success) throw createServerError(body)
  return body.data as T
}

export async function getContentAuditConfig(): Promise<ContentAuditConfigResponse> {
  const res = await api.get<ApiResponse<ContentAuditConfigResponse>>(
    `${BASE}/config`
  )
  return unwrap(res.data)
}

export async function updateContentAuditConfig(
  body: ContentAuditSaveRequest
): Promise<ContentAuditConfigResponse> {
  const res = await api.put<ApiResponse<ContentAuditConfigResponse>>(
    `${BASE}/config`,
    body
  )
  return unwrap(res.data)
}

export async function probeContentAuditEndpoint(
  body: ContentAuditProbeRequest
): Promise<ContentAuditProbeResult> {
  const res = await api.post<ApiResponse<ContentAuditProbeResult>>(
    `${BASE}/endpoints/probe`,
    body
  )
  return unwrap(res.data)
}

export async function getContentAuditRuntime(): Promise<ContentAuditRuntime> {
  const res = await api.get<ApiResponse<ContentAuditRuntime>>(`${BASE}/runtime`)
  return unwrap(res.data)
}

export async function getContentAuditEvents(
  params: ContentAuditEventFilters
): Promise<ContentAuditEventPage> {
  const res = await api.get<ApiResponse<ContentAuditEventPage>>(
    `${BASE}/events`,
    { params }
  )
  return unwrap(res.data)
}

export async function getContentAuditEvent(
  id: number
): Promise<ContentAuditEvent> {
  const res = await api.get<ApiResponse<ContentAuditEvent>>(
    `${BASE}/events/${id}`
  )
  return unwrap(res.data)
}

export async function deleteContentAuditEvent(id: number): Promise<void> {
  const res = await api.delete<ApiResponse>(`${BASE}/events/${id}`)
  unwrap(res.data)
}

export async function batchDeleteContentAuditEvents(
  ids: number[]
): Promise<number> {
  const res = await api.post<ApiResponse<{ deleted: number }>>(
    `${BASE}/events/batch-delete`,
    { ids }
  )
  return unwrap(res.data)?.deleted ?? 0
}
