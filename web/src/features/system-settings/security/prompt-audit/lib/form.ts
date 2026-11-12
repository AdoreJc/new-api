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
  PromptAuditConfig,
  PromptAuditConfigRequest,
  PromptAuditConfigResponse,
  PromptAuditTokenChange,
} from '../api'

/** Qwen3Guard risk categories with their English i18n keys. */
export const PROMPT_AUDIT_SCANNER_LABELS: Record<string, string> = {
  violent: 'Violent',
  non_violent_illegal_acts: 'Non-violent illegal acts',
  sexual_content_or_sexual_acts: 'Sexual content or sexual acts',
  pii: 'Personal identifying information',
  suicide_and_self_harm: 'Suicide and self-harm',
  unethical_acts: 'Unethical acts',
  politically_sensitive_topics: 'Politically sensitive topics',
  copyright_violation: 'Copyright violation',
  jailbreak: 'Jailbreak or prompt injection',
}

export const DEFAULT_GUARD_MODEL = 'sileader/qwen3guard:0.6b'

/** What is sent to the Guard, with English i18n keys. */
export const PROMPT_AUDIT_SCAN_SCOPES = [
  {
    value: 'latest_user',
    label: 'Latest user message',
    description:
      'Only the newest user input. Agent system prompts, tool output and earlier turns are not scanned.',
  },
  {
    value: 'user',
    label: 'All user messages',
    description:
      'Every user message in the conversation, without system, developer, assistant or tool text.',
  },
  {
    value: 'all',
    label: 'Entire conversation',
    description:
      'Every client text, including system prompts, assistant replies and tool output. Agent prompts are often flagged as jailbreaks.',
  },
] as const

export type PromptAuditScanScope =
  (typeof PROMPT_AUDIT_SCAN_SCOPES)[number]['value']

const DEFAULT_SCAN_SCOPE: PromptAuditScanScope = 'latest_user'

function toScanScope(value: string | undefined): PromptAuditScanScope {
  return (
    PROMPT_AUDIT_SCAN_SCOPES.find((scope) => scope.value === value)?.value ??
    DEFAULT_SCAN_SCOPE
  )
}

const endpointSchema = z.object({
  id: z.string().min(1),
  name: z.string().max(128),
  base_url: z
    .string()
    .trim()
    .regex(/^https?:\/\/[^\s?#@]+$/i, 'Enter an http or https address'),
  model: z.string().trim().min(1, 'Enter the Guard model name'),
  timeout_ms: z.coerce.number().int().min(100).max(30000),
  input_limit: z.coerce.number().int().min(128).max(100000),
  enabled: z.boolean(),
  has_token: z.boolean(),
  token: z.string(),
  clear_token: z.boolean(),
})

export const promptAuditFormSchema = z
  .object({
    enabled: z.boolean(),
    all_groups: z.boolean(),
    groups: z.array(z.string()),
    scanners: z.array(z.string()),
    store_full_prompt: z.boolean(),
    retention_days: z.coerce.number().int().min(1).max(365),
    worker_count: z.coerce.number().int().min(1).max(8),
    queue_capacity: z.coerce.number().int().min(1).max(10000),
    max_chunks_per_request: z.coerce.number().int().min(1).max(100),
    scan_scope: z.enum(['latest_user', 'user', 'all']),
    strict_jailbreak_mapping: z.boolean(),
    endpoints: z.array(endpointSchema).max(8),
  })
  .superRefine((values, context) => {
    if (!values.all_groups && values.groups.length === 0) {
      context.addIssue({
        code: 'custom',
        path: ['groups'],
        message: 'Select at least one group or audit all groups',
      })
    }
    const invalidGroup = values.groups.find(
      (group) =>
        group.replace(/\*$/, '').length === 0 ||
        /[*\s,]/.test(group.replace(/\*$/, ''))
    )
    if (invalidGroup !== undefined) {
      context.addIssue({
        code: 'custom',
        path: ['groups'],
        message: 'Only a trailing * is allowed in a group pattern',
      })
    }
    if (values.enabled && !values.endpoints.some((item) => item.enabled)) {
      context.addIssue({
        code: 'custom',
        path: ['endpoints'],
        message:
          'Enable at least one Guard endpoint before turning on prompt audit',
      })
    }
  })

export type PromptAuditFormInput = z.input<typeof promptAuditFormSchema>
export type PromptAuditFormValues = z.output<typeof promptAuditFormSchema>
export type PromptAuditEndpointFormValues =
  PromptAuditFormValues['endpoints'][number]

export function newEndpointFormValues(
  id: string
): PromptAuditEndpointFormValues {
  return {
    id,
    name: '',
    base_url: 'http://127.0.0.1:11434',
    model: DEFAULT_GUARD_MODEL,
    timeout_ms: 3000,
    input_limit: 4000,
    enabled: true,
    has_token: false,
    token: '',
    clear_token: false,
  }
}

export function toPromptAuditFormValues(
  response: PromptAuditConfigResponse
): PromptAuditFormValues {
  const config = response.config
  return {
    enabled: config.enabled,
    all_groups: config.all_groups,
    groups: config.groups ?? [],
    scanners: config.scanners ?? [],
    store_full_prompt: config.store_full_prompt,
    retention_days: config.retention_days,
    worker_count: config.worker_count,
    queue_capacity: config.queue_capacity,
    max_chunks_per_request: config.max_chunks_per_request || 8,
    // Settings saved before these options existed read as the defaults.
    scan_scope: toScanScope(config.scan_scope),
    strict_jailbreak_mapping: config.strict_jailbreak_mapping ?? false,
    endpoints: (config.endpoints ?? []).map((endpoint) => ({
      ...endpoint,
      has_token: response.has_token?.[endpoint.id] ?? false,
      token: '',
      clear_token: false,
    })),
  }
}

/**
 * Builds the save request. A blank token keeps the stored one (the server
 * drops it when the endpoint address changes); clear removes it.
 */
export function toPromptAuditConfigRequest(
  values: PromptAuditFormValues,
  current: PromptAuditConfig
): PromptAuditConfigRequest {
  const tokenChanges: Record<string, PromptAuditTokenChange> = {}
  for (const endpoint of values.endpoints) {
    if (endpoint.clear_token) {
      tokenChanges[endpoint.id] = { clear: true }
    } else if (endpoint.token.trim() !== '') {
      tokenChanges[endpoint.id] = { value: endpoint.token.trim() }
    }
  }
  return {
    expected_config_version: current.config_version,
    token_changes: tokenChanges,
    config: {
      ...current,
      enabled: values.enabled,
      all_groups: values.all_groups,
      groups: values.groups.map((group) => group.trim()).filter(Boolean),
      scanners: values.scanners,
      store_full_prompt: values.store_full_prompt,
      retention_days: values.retention_days,
      worker_count: values.worker_count,
      queue_capacity: values.queue_capacity,
      max_chunks_per_request: values.max_chunks_per_request,
      scan_scope: values.scan_scope,
      strict_jailbreak_mapping: values.strict_jailbreak_mapping,
      endpoints: values.endpoints.map((endpoint) => ({
        id: endpoint.id,
        name: endpoint.name.trim(),
        base_url: endpoint.base_url.trim(),
        model: endpoint.model.trim(),
        timeout_ms: endpoint.timeout_ms,
        input_limit: endpoint.input_limit,
        enabled: endpoint.enabled,
      })),
    },
  }
}
