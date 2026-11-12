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

import type { PromptAuditConfigResponse } from '../api'
import {
  newEndpointFormValues,
  promptAuditFormSchema,
  toPromptAuditConfigRequest,
  toPromptAuditFormValues,
} from '../lib/form'

const response: PromptAuditConfigResponse = {
  config: {
    enabled: false,
    mode: 'async_audit',
    all_groups: true,
    groups: [],
    scanners: ['jailbreak'],
    store_full_prompt: true,
    retention_days: 30,
    worker_count: 2,
    queue_capacity: 1024,
    max_chunks_per_request: 8,
    scan_scope: 'all',
    strict_jailbreak_mapping: true,
    endpoints: [
      {
        id: 'kept',
        name: 'Local',
        base_url: 'http://127.0.0.1:11434',
        model: 'guard',
        timeout_ms: 3000,
        input_limit: 4000,
        enabled: true,
      },
      {
        id: 'cleared',
        name: 'Old',
        base_url: 'http://10.0.0.2:8000',
        model: 'guard',
        timeout_ms: 3000,
        input_limit: 4000,
        enabled: false,
      },
    ],
    config_version: 4,
    updated_at: 0,
    updated_by: 1,
  },
  has_token: { kept: true, cleared: true },
  scanners: ['jailbreak'],
}

describe('prompt audit settings form', () => {
  it('sends only changed tokens and never echoes saved ones', () => {
    const values = toPromptAuditFormValues(response)
    expect(values.endpoints.map((endpoint) => endpoint.token)).toEqual(['', ''])
    values.endpoints[1].clear_token = true
    values.endpoints.push({
      ...newEndpointFormValues('added'),
      token: ' new-token ',
    })

    const request = toPromptAuditConfigRequest(values, response.config)

    expect(request.expected_config_version).toBe(4)
    expect(request.token_changes).toEqual({
      cleared: { clear: true },
      added: { value: 'new-token' },
    })
    expect(request.config.endpoints.map((endpoint) => endpoint.id)).toEqual([
      'kept',
      'cleared',
      'added',
    ])
    expect(request.config.endpoints[0]).not.toHaveProperty('token')
  })

  it.each([
    ['no group selected', { all_groups: false, groups: [] }],
    ['wildcard inside a group', { all_groups: false, groups: ['a*b'] }],
    ['enabled without an endpoint in use', { enabled: true, endpoints: [] }],
  ])('rejects %s', (_name, patch) => {
    const values = { ...toPromptAuditFormValues(response), ...patch }
    expect(promptAuditFormSchema.safeParse(values).success).toBe(false)
  })

  it('keeps the saved scan scope and jailbreak mapping when saving', () => {
    const values = toPromptAuditFormValues(response)
    expect(values.scan_scope).toBe('all')
    expect(values.strict_jailbreak_mapping).toBe(true)

    const request = toPromptAuditConfigRequest(
      { ...values, scan_scope: 'user', strict_jailbreak_mapping: false },
      response.config
    )

    expect(request.config.scan_scope).toBe('user')
    expect(request.config.strict_jailbreak_mapping).toBe(false)
  })

  it('reads settings saved before the scan options as the new defaults', () => {
    const legacy = {
      ...response,
      config: {
        ...response.config,
        scan_scope: undefined,
        strict_jailbreak_mapping: undefined,
      },
    } as unknown as PromptAuditConfigResponse

    const values = toPromptAuditFormValues(legacy)

    expect(values.scan_scope).toBe('latest_user')
    expect(values.strict_jailbreak_mapping).toBe(false)
  })

  it('accepts a group prefix pattern', () => {
    const values = {
      ...toPromptAuditFormValues(response),
      all_groups: false,
      groups: ['claude-ch*', 'default'],
    }
    expect(promptAuditFormSchema.safeParse(values).success).toBe(true)
  })
})
