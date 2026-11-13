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
  AuditPolicyCategoryAction,
  AuditPolicyLevel,
  AuditPolicyListCondition,
  AuditPolicyRule,
} from '../api'
import { INHERIT } from './constants'

export const DEFAULT_RULE_ID = 'default'
const MAX_RULE_ID_LENGTH = 64

const POSITIVE_ID = /^[1-9]\d*$/

const patternList = z
  .array(z.string())
  .refine(
    (patterns) =>
      patterns.every(
        (pattern) =>
          !/[\s,]/.test(pattern.trim()) &&
          (pattern.trim() === '*' || pattern.replaceAll('*', '').trim() !== '')
      ),
    'Patterns cannot contain commas or spaces, and need more than *'
  )

const idList = z
  .array(z.string())
  .refine(
    (ids) => ids.every((id) => POSITIVE_ID.test(id.trim())),
    'Enter positive numeric IDs'
  )

const rerouteTarget = z
  .string()
  .trim()
  .refine((value) => !value.includes('*'), 'A reroute target cannot use *')

const ruleFormObject = z.object({
  id: z.string().trim(),
  name: z.string().trim().max(128),
  enabled: z.boolean(),
  match: z.object({
    groups: patternList,
    models: patternList,
    exclude_models: patternList,
    user_ids: idList,
    token_ids: idList,
    list: z.string(),
    formats: z.array(z.string()),
  }),
  prompt_audit: z.object({
    enabled: z.boolean(),
    scan_scope: z.string(),
    min_level: z.enum(['controversial', 'unsafe']),
    category_levels: z.record(z.string(), z.string()),
    action: z.enum(['observe', 'block', 'reroute']),
    category_actions: z.record(z.string(), z.string()),
    fail_policy: z.enum(['open', 'closed']),
    count_disposal: z.boolean(),
  }),
  content_moderation: z.object({
    enabled: z.boolean(),
    action: z.enum(['observe', 'block', 'reroute']),
    sample_rate: z.coerce
      .number()
      .int()
      .min(0, 'Sample rate must be between 0 and 100')
      .max(100, 'Sample rate must be between 0 and 100'),
    keyword_mode: z.string(),
    override_keyword_lists: z.boolean(),
    keyword_lists: z.array(z.string()),
    thresholds: z.record(z.string(), z.string()),
    fail_policy: z.enum(['open', 'closed']),
    count_disposal: z.boolean(),
  }),
  reroute: z.object({ model: rerouteTarget, group: rerouteTarget }),
  reroute_fallback: z.enum(['observe', 'block']),
})

export type RuleFormInput = z.input<typeof ruleFormObject>
export type RuleFormValues = z.output<typeof ruleFormObject>

/**
 * Whether any action of the rule reroutes. Mirrors the server: the actions of
 * a disabled engine do not count.
 */
export function ruleUsesReroute(
  rule: Pick<RuleFormInput, 'prompt_audit' | 'content_moderation'>
): boolean {
  const promptAudit = rule.prompt_audit
  if (
    promptAudit.enabled &&
    (promptAudit.action === 'reroute' ||
      Object.values(promptAudit.category_actions).includes('reroute'))
  ) {
    return true
  }
  return (
    rule.content_moderation.enabled &&
    rule.content_moderation.action === 'reroute'
  )
}

/**
 * Validates one rule of the table. otherIds are the ids of the other rules,
 * which a rule id must not repeat; the default rule keeps its fixed id.
 */
export function createRuleFormSchema(options: {
  otherIds: string[]
  isDefault: boolean
}) {
  return ruleFormObject.superRefine((values, context) => {
    if (values.id === '' || values.id.length > MAX_RULE_ID_LENGTH) {
      context.addIssue({
        code: 'custom',
        path: ['id'],
        message: 'Enter a rule ID of at most 64 characters',
      })
    } else if (
      !options.isDefault &&
      (values.id === DEFAULT_RULE_ID || options.otherIds.includes(values.id))
    ) {
      context.addIssue({
        code: 'custom',
        path: ['id'],
        message: 'Another rule already uses this ID',
      })
    }
    const invalidThreshold = Object.values(
      values.content_moderation.thresholds
    ).some((threshold) => {
      if (threshold.trim() === '') return false
      const value = Number(threshold)
      return !Number.isFinite(value) || value < 0 || value > 1
    })
    if (invalidThreshold) {
      context.addIssue({
        code: 'custom',
        path: ['content_moderation', 'thresholds'],
        message: 'Thresholds must be between 0 and 1',
      })
    }
    if (
      ruleUsesReroute(values) &&
      values.reroute.model === '' &&
      values.reroute.group === ''
    ) {
      context.addIssue({
        code: 'custom',
        path: ['reroute', 'model'],
        message: 'Enter a model or group to reroute to',
      })
    }
  })
}

function withoutInherit<T extends string>(
  values: Record<string, string>
): Record<string, T> {
  const result: Record<string, T> = {}
  for (const [key, value] of Object.entries(values)) {
    if (value !== INHERIT && value !== '') result[key] = value as T
  }
  return result
}

function cleanList(items: string[]): string[] {
  return items.map((item) => item.trim()).filter(Boolean)
}

export function toRuleFormValues(rule: AuditPolicyRule): RuleFormValues {
  const thresholds: Record<string, string> = {}
  for (const [category, value] of Object.entries(
    rule.content_moderation.thresholds ?? {}
  )) {
    thresholds[category] = String(value)
  }
  const promptAudit = rule.prompt_audit
  const moderation = rule.content_moderation
  return {
    id: rule.id,
    name: rule.name,
    enabled: rule.enabled,
    match: {
      groups: rule.match.groups ?? [],
      models: rule.match.models ?? [],
      exclude_models: rule.match.exclude_models ?? [],
      user_ids: (rule.match.user_ids ?? []).map(String),
      token_ids: (rule.match.token_ids ?? []).map(String),
      list: rule.match.list || INHERIT,
      formats: rule.match.formats ?? [],
    },
    prompt_audit: {
      enabled: promptAudit.enabled,
      scan_scope: promptAudit.scan_scope || INHERIT,
      min_level: promptAudit.min_level || 'unsafe',
      category_levels: { ...promptAudit.category_levels },
      action: promptAudit.action || 'observe',
      category_actions: { ...promptAudit.category_actions },
      fail_policy: promptAudit.fail_policy || 'open',
      count_disposal: promptAudit.count_disposal,
    },
    content_moderation: {
      enabled: moderation.enabled,
      action: moderation.action || 'observe',
      sample_rate: moderation.sample_rate,
      keyword_mode: moderation.keyword_mode || INHERIT,
      override_keyword_lists: moderation.keyword_lists !== null,
      keyword_lists: moderation.keyword_lists ?? [],
      thresholds,
      fail_policy: moderation.fail_policy || 'open',
      count_disposal: moderation.count_disposal,
    },
    reroute: {
      model: rule.reroute?.model ?? '',
      group: rule.reroute?.group ?? '',
    },
    reroute_fallback: rule.reroute_fallback || 'observe',
  }
}

/** Builds the rule sent to the server from validated editor values. */
export function toRule(values: RuleFormValues): AuditPolicyRule {
  const thresholds: Record<string, number> = {}
  for (const [category, value] of Object.entries(
    values.content_moderation.thresholds
  )) {
    if (value.trim() !== '') thresholds[category] = Number(value)
  }
  const moderation = values.content_moderation
  const list =
    values.match.list === INHERIT
      ? ''
      : (values.match.list as AuditPolicyListCondition)
  return {
    id: values.id,
    name: values.name,
    enabled: values.enabled,
    match: {
      groups: cleanList(values.match.groups),
      models: cleanList(values.match.models),
      exclude_models: cleanList(values.match.exclude_models),
      user_ids: cleanList(values.match.user_ids).map(Number),
      token_ids: cleanList(values.match.token_ids).map(Number),
      list,
      formats: values.match.formats,
    },
    prompt_audit: {
      enabled: values.prompt_audit.enabled,
      scan_scope:
        values.prompt_audit.scan_scope === INHERIT
          ? ''
          : values.prompt_audit.scan_scope,
      min_level: values.prompt_audit.min_level,
      category_levels: withoutInherit<AuditPolicyLevel>(
        values.prompt_audit.category_levels
      ),
      action: values.prompt_audit.action,
      category_actions: withoutInherit<AuditPolicyCategoryAction>(
        values.prompt_audit.category_actions
      ),
      fail_policy: values.prompt_audit.fail_policy,
      count_disposal: values.prompt_audit.count_disposal,
    },
    content_moderation: {
      enabled: moderation.enabled,
      action: moderation.action,
      sample_rate: moderation.sample_rate,
      keyword_mode:
        moderation.keyword_mode === INHERIT ? '' : moderation.keyword_mode,
      keyword_lists: moderation.override_keyword_lists
        ? cleanList(moderation.keyword_lists)
        : null,
      thresholds,
      fail_policy: moderation.fail_policy,
      count_disposal: moderation.count_disposal,
    },
    reroute: { model: values.reroute.model, group: values.reroute.group },
    reroute_fallback: values.reroute_fallback,
  }
}

/** Returns base, or base with the lowest free numeric suffix. */
export function uniqueRuleId(base: string, existingIds: string[]): string {
  const taken = new Set([...existingIds, DEFAULT_RULE_ID])
  if (!taken.has(base)) return base
  for (let index = 2; ; index++) {
    const suffix = `-${index}`
    const candidate = base.slice(0, MAX_RULE_ID_LENGTH - suffix.length) + suffix
    if (!taken.has(candidate)) return candidate
  }
}

/** A new rule audits prompts in the background and blocks nothing. */
export function createRule(existingIds: string[]): AuditPolicyRule {
  return {
    id: uniqueRuleId('rule', existingIds),
    name: '',
    enabled: true,
    match: {
      groups: [],
      models: [],
      exclude_models: [],
      user_ids: [],
      token_ids: [],
      list: '',
      formats: [],
    },
    prompt_audit: {
      enabled: true,
      scan_scope: '',
      min_level: 'unsafe',
      category_levels: {},
      action: 'observe',
      category_actions: {},
      fail_policy: 'open',
      count_disposal: false,
    },
    content_moderation: {
      enabled: false,
      action: 'observe',
      sample_rate: 100,
      keyword_mode: '',
      keyword_lists: null,
      thresholds: {},
      fail_policy: 'open',
      count_disposal: false,
    },
    reroute: { model: '', group: '' },
    reroute_fallback: 'observe',
  }
}

export function duplicateRule(
  rule: AuditPolicyRule,
  existingIds: string[]
): AuditPolicyRule {
  const copy = structuredClone(rule)
  copy.id = uniqueRuleId(`${rule.id}-copy`, existingIds)
  return copy
}

/** Moves one item of a list; the order of the rules is their priority. */
export function moveItem<T>(items: T[], from: number, to: number): T[] {
  if (from === to || to < 0 || to >= items.length) return items
  const next = [...items]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  return next
}
