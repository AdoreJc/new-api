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

import type { ContentModerationConfigResponse } from '../api'
import {
  contentModerationFormSchema,
  toContentModerationConfigRequest,
  toContentModerationFormValues,
} from '../lib/form'

const response: ContentModerationConfigResponse = {
  config: {
    mode: 'off',
    base_url: 'https://api.openai.com',
    model: 'omni-moderation-latest',
    timeout_ms: 3000,
    retry_count: 2,
    include_images: true,
    sample_rate: 100,
    all_groups: true,
    groups: [],
    model_filter: { type: 'all', models: [] },
    thresholds: { hate: 0.5 },
    worker_count: 4,
    queue_size: 1024,
    record_non_hits: false,
    hit_retention_days: 180,
    non_hit_retention_days: 3,
    hash_blocklist_enabled: false,
    email_on_hit: false,
    auto_disable_enabled: false,
    disable_threshold: 10,
    violation_window_hours: 720,
    keyword_mode: 'api_only',
    keyword_lists: [],
    allowlist_user_ids: [3, 9],
    config_version: 4,
    updated_at: 0,
    updated_by: 1,
  },
  keys: [{ id: 'aaaaaaaaaaaa' }, { id: 'bbbbbbbbbbbb' }],
  categories: ['hate', 'violence'],
  default_thresholds: { hate: 0.65, violence: 0.95 },
}

describe('content moderation settings form', () => {
  it('adds new keys and removes ticked saved keys without echoing them', () => {
    const values = toContentModerationFormValues(response)
    expect(values.new_keys).toBe('')
    expect(values.thresholds).toEqual({ hate: 0.5, violence: 0.95 })
    values.removed_keys = ['bbbbbbbbbbbb']
    values.new_keys = ' sk-one \n sk-two '
    values.allowlist = '9, 12 3'

    const request = toContentModerationConfigRequest(values, response.config)

    expect(request.expected_config_version).toBe(4)
    expect(request.key_changes).toEqual({
      add: ['sk-one', 'sk-two'],
      remove: ['bbbbbbbbbbbb'],
      clear: false,
    })
    expect(request.config.allowlist_user_ids).toEqual([9, 12, 3])
    expect(JSON.stringify(request.config)).not.toContain('sk-one')
  })

  it.each([
    ['an active mode without any key', { mode: 'pre_block', stored_keys: [] }],
    [
      'an active mode whose only key is removed',
      { mode: 'observe', stored_keys: ['a'], removed_keys: ['a'] },
    ],
    ['no group selected', { all_groups: false, groups: [] }],
    ['a model filter without models', { model_filter_type: 'include' }],
    ['a non-numeric allowlist entry', { allowlist: '3, abc' }],
    ['a threshold above 1', { thresholds: { hate: 1.5 } }],
    ['more than 3 days for other records', { non_hit_retention_days: 4 }],
    [
      'the gated keyword mode without any API key',
      {
        mode: 'pre_block',
        stored_keys: [],
        keyword_mode: 'keyword_gated_api',
        keyword_lists: ['default'],
      },
    ],
    [
      'a keyword mode without any list',
      { keyword_mode: 'keyword_and_api', keyword_lists: [] },
    ],
  ])('rejects %s', (_name, patch) => {
    const values = { ...toContentModerationFormValues(response), ...patch }
    expect(contentModerationFormSchema.safeParse(values).success).toBe(false)
  })

  it('accepts keyword-only moderation without any API key', () => {
    const values = {
      ...toContentModerationFormValues(response),
      mode: 'pre_block',
      stored_keys: [],
      keyword_mode: 'keyword_only',
      keyword_lists: ['default'],
    }
    expect(contentModerationFormSchema.safeParse(values).success).toBe(true)
    const request = toContentModerationConfigRequest(
      contentModerationFormSchema.parse(values),
      response.config
    )
    expect(request.config.keyword_mode).toBe('keyword_only')
    expect(request.config.keyword_lists).toEqual(['default'])
  })

  it('accepts an active mode with a newly entered key', () => {
    const values = {
      ...toContentModerationFormValues(response),
      mode: 'pre_block',
      stored_keys: [],
      new_keys: 'sk-new',
    }
    expect(contentModerationFormSchema.safeParse(values).success).toBe(true)
  })
})
