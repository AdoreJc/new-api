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

export const AUDIT_POLICY_QUERY_KEY = ['audit-policy', 'config'] as const

export type AuditPolicyAction = 'observe' | 'block' | 'reroute'
export type AuditPolicyCategoryAction = AuditPolicyAction | 'ignore'
export type AuditPolicyLevel = 'controversial' | 'unsafe' | 'off'
export type AuditPolicyFailPolicy = 'open' | 'closed'
export type AuditPolicyListCondition =
  | ''
  | 'allowlist'
  | 'verified'
  | 'blocklist'
  | 'unlisted'

export type AuditPolicyMatch = {
  groups: string[]
  /** Matched against the token's own group only, e.g. "auto". */
  requested_groups?: string[]
  models: string[]
  exclude_models: string[]
  user_ids: number[]
  token_ids: number[]
  list: AuditPolicyListCondition
  formats: string[]
}

export type AuditPolicyPromptAudit = {
  enabled: boolean
  /** Empty uses the scan scope of the prompt audit settings. */
  scan_scope: string
  min_level: 'controversial' | 'unsafe'
  category_levels: Record<string, AuditPolicyLevel>
  action: AuditPolicyAction
  category_actions: Record<string, AuditPolicyCategoryAction>
  fail_policy: AuditPolicyFailPolicy
  count_disposal: boolean
}

export type AuditPolicyContentModeration = {
  enabled: boolean
  action: AuditPolicyAction
  sample_rate: number
  /** Empty uses the keyword mode of the content moderation settings. */
  keyword_mode: string
  /** Null uses the keyword lists of the content moderation settings. */
  keyword_lists: string[] | null
  thresholds: Record<string, number>
  fail_policy: AuditPolicyFailPolicy
  count_disposal: boolean
}

export type AuditPolicyRule = {
  id: string
  name: string
  enabled: boolean
  match: AuditPolicyMatch
  prompt_audit: AuditPolicyPromptAudit
  content_moderation: AuditPolicyContentModeration
  reroute: { model: string; group: string }
  reroute_fallback: 'observe' | 'block'
  /**
   * Set by the server on a stored rule it could not read; the rule only
   * stands in for it. Editing the rule rebuilds it and clears the mark.
   */
  degraded?: boolean
}

export type AuditPolicyListEntry = {
  user_id: number
  /** 0 covers every token of the user. */
  token_id: number
  verified: boolean
  note: string
}

export type AuditPolicyConfig = {
  rules: AuditPolicyRule[]
  default: AuditPolicyRule
  allowlist: AuditPolicyListEntry[]
  blocklist: AuditPolicyListEntry[]
  config_version: number
  updated_at: number
  updated_by: number
}

export type AuditPolicyResponse = {
  config: AuditPolicyConfig
  /** False while the rules are derived from the engine settings. */
  saved: boolean
  /** True when the stored table failed validation and is read degraded. */
  degraded?: boolean
  /** What was dropped or kept degraded, as server log sentences. */
  problems?: string[]
  prompt_audit_enabled: boolean
  prompt_audit_mode: string
  content_moderation_mode: string
  prompt_audit_categories: string[]
  content_moderation_categories: string[]
  relay_formats: string[]
  prompt_audit_scan_scopes: string[]
  content_moderation_keyword_modes: string[]
}

export type AuditPolicyTestRequest = {
  user_id: number
  token_id: number
  group: string
  /** The token's own group, such as auto, when group is the chosen one. */
  requested_group: string
  model: string
  format: string
  /** A draft table to test instead of the rules in use. */
  config?: AuditPolicyConfig
}

export type AuditPolicyEngineMode = 'off' | 'async' | 'sync'

export type AuditPolicyTestResult = {
  rule_id: string
  rule_name: string
  /** -1 is the default rule. */
  rule_index: number
  saved: boolean
  allowlisted: boolean
  verified: boolean
  blocklisted: boolean
  prompt_audit: AuditPolicyEngineMode
  prompt_audit_action: string
  prompt_audit_fail_policy: string
  content_moderation: AuditPolicyEngineMode
  content_moderation_action: string
  content_moderation_fail_policy: string
  reroute?: { model: string; group: string }
  notes: string[]
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

export function getAuditPolicy() {
  return unwrap(
    api.get<ApiResponse<AuditPolicyResponse>>('/api/audit_policy/'),
    t('Failed to load the audit policy')
  )
}

export function updateAuditPolicy(
  config: AuditPolicyConfig,
  expectedConfigVersion: number
) {
  return unwrap(
    api.put<ApiResponse<AuditPolicyResponse>>('/api/audit_policy/', {
      config,
      expected_config_version: expectedConfigVersion,
    }),
    t('Failed to save the audit policy')
  )
}

export function resetAuditPolicy(expectedConfigVersion: number) {
  return unwrap(
    api.post<ApiResponse<AuditPolicyResponse>>('/api/audit_policy/reset', {
      expected_config_version: expectedConfigVersion,
    }),
    t('Failed to reset the audit policy')
  )
}

export function testAuditPolicy(request: AuditPolicyTestRequest) {
  return unwrap(
    api.post<ApiResponse<AuditPolicyTestResult>>(
      '/api/audit_policy/test',
      request
    ),
    t('Failed to test the audit policy')
  )
}
