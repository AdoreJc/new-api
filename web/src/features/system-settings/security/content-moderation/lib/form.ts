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

import type {
  ContentModerationConfig,
  ContentModerationConfigRequest,
  ContentModerationConfigResponse,
} from '../api'

/** OpenAI moderation categories with their English i18n keys. */
export const CONTENT_MODERATION_CATEGORY_LABELS: Record<string, string> = {
  harassment: 'Harassment',
  keyword: 'Keyword list',
  hash: 'Remembered input',
  'harassment/threatening': 'Harassment (threatening)',
  hate: 'Hate',
  'hate/threatening': 'Hate (threatening)',
  illicit: 'Illicit',
  'illicit/violent': 'Illicit (violent)',
  'self-harm': 'Self-harm',
  'self-harm/intent': 'Self-harm (intent)',
  'self-harm/instructions': 'Self-harm (instructions)',
  sexual: 'Sexual',
  'sexual/minors': 'Sexual (minors)',
  violence: 'Violence',
  'violence/graphic': 'Violence (graphic)',
}

/** Splits a list typed with commas, spaces or new lines. */
export function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

const numberField = (min: number, max: number) =>
  z.coerce.number().int().min(min).max(max)

export const contentModerationFormSchema = z
  .object({
    mode: z.enum(['off', 'observe', 'pre_block']),
    base_url: z
      .string()
      .trim()
      .regex(/^https?:\/\/[^\s?#@]+$/i, 'Enter an http or https address'),
    model: z.string().trim().min(1, 'Enter the moderation model name'),
    timeout_ms: numberField(100, 30000),
    retry_count: numberField(0, 5),
    include_images: z.boolean(),
    sample_rate: numberField(0, 100),
    all_groups: z.boolean(),
    groups: z.array(z.string()),
    model_filter_type: z.enum(['all', 'include', 'exclude']),
    model_filter_models: z.array(z.string()),
    thresholds: z.record(z.string(), z.coerce.number().min(0).max(1)),
    worker_count: numberField(1, 32),
    queue_size: numberField(1, 10000),
    record_non_hits: z.boolean(),
    hit_retention_days: numberField(1, 3650),
    non_hit_retention_days: numberField(1, 3),
    hash_blocklist_enabled: z.boolean(),
    email_on_hit: z.boolean(),
    auto_disable_enabled: z.boolean(),
    disable_threshold: numberField(1, 10000),
    violation_window_hours: numberField(1, 8760),
    keyword_mode: z.enum([
      'api_only',
      'keyword_only',
      'keyword_and_api',
      'keyword_gated_api',
    ]),
    keyword_lists: z.array(z.string()),
    allowlist: z.string(),
    stored_keys: z.array(z.string()),
    removed_keys: z.array(z.string()),
    new_keys: z.string(),
  })
  .superRefine((values, context) => {
    if (!values.all_groups && values.groups.length === 0) {
      context.addIssue({
        code: 'custom',
        path: ['groups'],
        message: 'Select at least one group or moderate all groups',
      })
    }
    if (
      values.model_filter_type !== 'all' &&
      values.model_filter_models.length === 0
    ) {
      context.addIssue({
        code: 'custom',
        path: ['model_filter_models'],
        message: 'Add at least one model to the filter',
      })
    }
    if (splitList(values.allowlist).some((id) => !/^[1-9]\d*$/.test(id))) {
      context.addIssue({
        code: 'custom',
        path: ['allowlist'],
        message: 'Enter positive user IDs separated by commas or spaces',
      })
    }
    const remainingKeys =
      values.stored_keys.filter((id) => !values.removed_keys.includes(id))
        .length + splitList(values.new_keys).length
    if (
      values.keyword_mode !== 'api_only' &&
      values.keyword_lists.length === 0
    ) {
      context.addIssue({
        code: 'custom',
        path: ['keyword_lists'],
        message: 'Select at least one keyword list for the keyword mode',
      })
    }
    // Keyword-only moderation never calls the API, so it needs no key.
    if (
      values.mode !== 'off' &&
      values.keyword_mode !== 'keyword_only' &&
      remainingKeys === 0
    ) {
      context.addIssue({
        code: 'custom',
        path: ['new_keys'],
        message:
          'Add at least one moderation API key before turning on content moderation',
      })
    }
  })

export type ContentModerationFormInput = z.input<
  typeof contentModerationFormSchema
>
export type ContentModerationFormValues = z.output<
  typeof contentModerationFormSchema
>

export function toContentModerationFormValues(
  response: ContentModerationConfigResponse
): ContentModerationFormValues {
  const config = response.config
  return {
    mode: config.mode,
    base_url: config.base_url,
    model: config.model,
    timeout_ms: config.timeout_ms,
    retry_count: config.retry_count,
    include_images: config.include_images,
    sample_rate: config.sample_rate,
    all_groups: config.all_groups,
    groups: config.groups ?? [],
    model_filter_type: config.model_filter?.type ?? 'all',
    model_filter_models: config.model_filter?.models ?? [],
    thresholds: { ...response.default_thresholds, ...config.thresholds },
    worker_count: config.worker_count,
    queue_size: config.queue_size,
    record_non_hits: config.record_non_hits,
    hit_retention_days: config.hit_retention_days,
    non_hit_retention_days: config.non_hit_retention_days,
    hash_blocklist_enabled: config.hash_blocklist_enabled,
    email_on_hit: config.email_on_hit,
    auto_disable_enabled: config.auto_disable_enabled,
    disable_threshold: config.disable_threshold,
    violation_window_hours: config.violation_window_hours,
    keyword_mode: config.keyword_mode ?? 'api_only',
    keyword_lists: config.keyword_lists ?? [],
    allowlist: (config.allowlist_user_ids ?? []).join(', '),
    stored_keys: (response.keys ?? []).map((key) => key.id),
    removed_keys: [],
    new_keys: '',
  }
}

/**
 * Builds the save request. New keys are added and removed key ids dropped;
 * the server also drops stored keys when the API address changes.
 */
export function toContentModerationConfigRequest(
  values: ContentModerationFormValues,
  current: ContentModerationConfig
): ContentModerationConfigRequest {
  return {
    expected_config_version: current.config_version,
    key_changes: {
      add: splitList(values.new_keys),
      remove: values.removed_keys,
      clear: false,
    },
    config: {
      ...current,
      mode: values.mode,
      base_url: values.base_url.trim(),
      model: values.model.trim(),
      timeout_ms: values.timeout_ms,
      retry_count: values.retry_count,
      include_images: values.include_images,
      sample_rate: values.sample_rate,
      all_groups: values.all_groups,
      groups: values.groups.map((group) => group.trim()).filter(Boolean),
      model_filter: {
        type: values.model_filter_type,
        models: values.model_filter_models
          .map((model) => model.trim())
          .filter(Boolean),
      },
      thresholds: values.thresholds,
      worker_count: values.worker_count,
      queue_size: values.queue_size,
      record_non_hits: values.record_non_hits,
      hit_retention_days: values.hit_retention_days,
      non_hit_retention_days: values.non_hit_retention_days,
      hash_blocklist_enabled: values.hash_blocklist_enabled,
      email_on_hit: values.email_on_hit,
      auto_disable_enabled: values.auto_disable_enabled,
      disable_threshold: values.disable_threshold,
      violation_window_hours: values.violation_window_hours,
      keyword_mode: values.keyword_mode,
      keyword_lists: values.keyword_lists,
      allowlist_user_ids: splitList(values.allowlist).map(Number),
    },
  }
}
