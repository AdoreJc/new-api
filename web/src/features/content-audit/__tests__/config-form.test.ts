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
import { describe, expect, it } from 'vitest'

import {
  buildContentAuditFormValues,
  buildContentAuditSaveRequest,
  contentAuditConfigSchema,
  createContentAuditEndpointId,
} from '../lib/config-form'
import type { ContentAuditConfig } from '../types'

const loadedConfig: ContentAuditConfig = {
  enabled: false,
  all_groups: false,
  groups: ['default'],
  scanners: ['violent', 'jailbreak'],
  worker_count: 2,
  queue_capacity: 1000,
  queue_max_mb: 64,
  timeout_ms: 3000,
  max_chunk_runes: 4000,
  max_body_mb: 8,
  retention_days: 30,
  endpoints: [
    {
      id: 'ep_stored01',
      name: 'Primary',
      base_url: 'https://guard.example.com',
      model: 'guard-model',
      enabled: true,
      has_token: true,
    },
  ],
  config_version: 7,
  updated_at: 1_700_000_000,
  updated_by: 1,
}

function formValues() {
  return buildContentAuditFormValues(loadedConfig)
}

describe('content audit config schema', () => {
  it('accepts plain group names and trailing-* prefix patterns', () => {
    const values = { ...formValues(), groups: ['default', 'claude-ch*'] }

    expect(contentAuditConfigSchema.safeParse(values).success).toBe(true)
  })

  it.each([['claude *'], ['*claude'], ['cl*ude*'], ['']])(
    'rejects the invalid group pattern %j',
    (group) => {
      const result = contentAuditConfigSchema.safeParse({
        ...formValues(),
        groups: [group],
      })

      expect(result.success).toBe(false)
      expect(result.error?.issues[0]?.path).toEqual(['groups'])
    }
  )

  it.each([
    ['worker_count', 0],
    ['worker_count', 9],
    ['queue_capacity', 20001],
    ['timeout_ms', 99],
    ['max_chunk_runes', 127],
    ['retention_days', 366],
    ['max_body_mb', 1.5],
    ['queue_max_mb', Number.NaN],
  ])('rejects %s = %s outside the allowed bounds', (field, value) => {
    const result = contentAuditConfigSchema.safeParse({
      ...formValues(),
      [field]: value,
    })

    expect(result.success).toBe(false)
    expect(result.error?.issues[0]?.path).toEqual([field])
  })

  it('rejects enabling audit when no guard endpoint is enabled', () => {
    const values = formValues()
    values.enabled = true
    values.endpoints[0].enabled = false

    const result = contentAuditConfigSchema.safeParse(values)

    expect(result.success).toBe(false)
    expect(result.error?.issues[0]?.path).toEqual(['enabled'])
  })

  it('rejects more than eight guard endpoints', () => {
    const endpoint = formValues().endpoints[0]
    const endpoints = Array.from({ length: 9 }, (_, index) => ({
      ...endpoint,
      id: `ep_${index}`,
    }))

    const result = contentAuditConfigSchema.safeParse({
      ...formValues(),
      endpoints,
    })

    expect(result.success).toBe(false)
  })
})

describe('content audit save request', () => {
  it('sends the loaded config version and omits token fields for an untouched endpoint', () => {
    const request = buildContentAuditSaveRequest(formValues(), loadedConfig)

    expect(request.expected_config_version).toBe(7)
    expect(request.config.endpoints[0]).toEqual({
      id: 'ep_stored01',
      name: 'Primary',
      base_url: 'https://guard.example.com',
      model: 'guard-model',
      enabled: true,
    })
    expect(request.config.endpoints[0]).not.toHaveProperty('has_token')
  })

  it('sends a trimmed new token instead of clearing when both are set', () => {
    const values = formValues()
    values.endpoints[0].token = '  sk-new  '
    values.endpoints[0].clear_token = true

    const endpoint = buildContentAuditSaveRequest(values, loadedConfig).config
      .endpoints[0]

    expect(endpoint.token).toBe('sk-new')
    expect(endpoint).not.toHaveProperty('clear_token')
  })

  it('sends clear_token when the stored token is cleared without a replacement', () => {
    const values = formValues()
    values.endpoints[0].clear_token = true

    const endpoint = buildContentAuditSaveRequest(values, loadedConfig).config
      .endpoints[0]

    expect(endpoint.clear_token).toBe(true)
    expect(endpoint).not.toHaveProperty('token')
  })
})

describe('content audit endpoint id', () => {
  it('generates an ep_ prefixed id with eight lowercase alphanumerics', () => {
    expect(createContentAuditEndpointId()).toMatch(/^ep_[a-z0-9]{8}$/)
  })
})
