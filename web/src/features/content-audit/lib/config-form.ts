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
import * as z from 'zod'

import { CONTENT_AUDIT_MAX_ENDPOINTS } from '../constants'
import type { ContentAuditConfig, ContentAuditSaveRequest } from '../types'

/**
 * A group name, or a prefix pattern ending in a single `*` (e.g. `claude-ch*`).
 * Whitespace and a `*` anywhere but the end are rejected.
 */
export const CONTENT_AUDIT_GROUP_PATTERN = /^[^\s*]+\*?$/

function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

const OUT_OF_RANGE = 'Enter a whole number within the allowed range'

const boundedInt = (min: number, max: number) =>
  z
    .number({ error: OUT_OF_RANGE })
    .int(OUT_OF_RANGE)
    .min(min, OUT_OF_RANGE)
    .max(max, OUT_OF_RANGE)

const endpointSchema = z.object({
  id: z.string().min(1),
  name: z.string().trim().max(64, 'Name must be at most 64 characters'),
  base_url: z
    .string()
    .trim()
    .min(1, 'Base URL is required')
    .refine(isHttpUrl, 'Provide a valid URL starting with http:// or https://'),
  model: z.string().trim().min(1, 'Model is required'),
  enabled: z.boolean(),
  has_token: z.boolean(),
  token: z.string(),
  clear_token: z.boolean(),
})

export const contentAuditConfigSchema = z
  .object({
    enabled: z.boolean(),
    all_groups: z.boolean(),
    // Validated as a whole so the error shows under the single groups control.
    groups: z
      .array(z.string())
      .refine(
        (groups) =>
          groups.every((group) => CONTENT_AUDIT_GROUP_PATTERN.test(group)),
        'Group names cannot contain spaces; only a trailing * is allowed'
      ),
    scanners: z.array(z.string()),
    worker_count: boundedInt(1, 8),
    queue_capacity: boundedInt(1, 20000),
    queue_max_mb: boundedInt(1, 1024),
    timeout_ms: boundedInt(100, 30000),
    max_chunk_runes: boundedInt(128, 100000),
    max_body_mb: boundedInt(1, 64),
    retention_days: boundedInt(1, 365),
    endpoints: z
      .array(endpointSchema)
      .max(CONTENT_AUDIT_MAX_ENDPOINTS, 'Too many guard endpoints'),
  })
  .refine(
    (config) =>
      !config.enabled || config.endpoints.some((endpoint) => endpoint.enabled),
    {
      path: ['enabled'],
      message:
        'Enable at least one guard endpoint before turning on content audit',
    }
  )

export type ContentAuditFormValues = z.infer<typeof contentAuditConfigSchema>
export type ContentAuditEndpointFormValues =
  ContentAuditFormValues['endpoints'][number]

export function buildContentAuditFormValues(
  config: ContentAuditConfig
): ContentAuditFormValues {
  return {
    enabled: config.enabled,
    all_groups: config.all_groups,
    groups: config.groups ?? [],
    scanners: config.scanners ?? [],
    worker_count: config.worker_count,
    queue_capacity: config.queue_capacity,
    queue_max_mb: config.queue_max_mb,
    timeout_ms: config.timeout_ms,
    max_chunk_runes: config.max_chunk_runes,
    max_body_mb: config.max_body_mb,
    retention_days: config.retention_days,
    endpoints: (config.endpoints ?? []).map((endpoint) => ({
      id: endpoint.id,
      name: endpoint.name,
      base_url: endpoint.base_url,
      model: endpoint.model,
      enabled: endpoint.enabled,
      has_token: endpoint.has_token,
      token: '',
      clear_token: false,
    })),
  }
}

/**
 * Builds the PUT body. Tokens are write-only: a non-empty token replaces the
 * stored one, `clear_token` removes it, and leaving both unset keeps it.
 */
export function buildContentAuditSaveRequest(
  values: ContentAuditFormValues,
  loaded: ContentAuditConfig
): ContentAuditSaveRequest {
  return {
    expected_config_version: loaded.config_version,
    config: {
      enabled: values.enabled,
      all_groups: values.all_groups,
      groups: values.groups,
      scanners: values.scanners,
      worker_count: values.worker_count,
      queue_capacity: values.queue_capacity,
      queue_max_mb: values.queue_max_mb,
      timeout_ms: values.timeout_ms,
      max_chunk_runes: values.max_chunk_runes,
      max_body_mb: values.max_body_mb,
      retention_days: values.retention_days,
      config_version: loaded.config_version,
      updated_at: loaded.updated_at,
      updated_by: loaded.updated_by,
      endpoints: values.endpoints.map((endpoint) => {
        const token = endpoint.token.trim()
        return {
          id: endpoint.id,
          name: endpoint.name.trim(),
          base_url: endpoint.base_url.trim(),
          model: endpoint.model.trim(),
          enabled: endpoint.enabled,
          ...(token ? { token } : {}),
          ...(!token && endpoint.clear_token ? { clear_token: true } : {}),
        }
      }),
    },
  }
}

const ENDPOINT_ID_ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789'

/** Client-side id for a new guard endpoint row: `ep_` + 8 random chars. */
export function createContentAuditEndpointId(): string {
  const bytes = new Uint8Array(8)
  crypto.getRandomValues(bytes)
  let suffix = ''
  for (const byte of bytes) {
    suffix += ENDPOINT_ID_ALPHABET[byte % ENDPOINT_ID_ALPHABET.length]
  }
  return `ep_${suffix}`
}
