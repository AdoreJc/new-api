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
  createRule,
  createRuleFormSchema,
  toRule,
  toRuleFormValues,
  type RuleFormValues,
} from '../lib/rule-form'

function rerouteValues(target: { model: string; group: string }) {
  const values = toRuleFormValues(createRule([]))
  return {
    ...values,
    id: 'claude-reroute',
    prompt_audit: { ...values.prompt_audit, action: 'reroute' },
    reroute: target,
  } satisfies RuleFormValues
}

function issuePaths(
  result: ReturnType<ReturnType<typeof createRuleFormSchema>['safeParse']>
) {
  return result.success
    ? []
    : result.error.issues.map((issue) => issue.path.join('.'))
}

describe('rule form schema', () => {
  const schema = createRuleFormSchema({ otherIds: [], isDefault: false })

  it('rejects a reroute rule without a target model or group', () => {
    const result = schema.safeParse(rerouteValues({ model: '', group: '' }))

    expect(issuePaths(result)).toEqual(['reroute.model'])
  })

  it('accepts a reroute rule that names only a group', () => {
    const result = schema.safeParse(
      rerouteValues({ model: '', group: 'safe-pool' })
    )

    expect(result.success).toBe(true)
  })

  it('rejects an id another rule or the default rule already uses', () => {
    const withOthers = createRuleFormSchema({
      otherIds: ['claude-reroute'],
      isDefault: false,
    })

    expect(
      issuePaths(withOthers.safeParse(rerouteValues({ model: 'm', group: '' })))
    ).toEqual(['id'])
    expect(
      issuePaths(
        schema.safeParse({
          ...rerouteValues({ model: 'm', group: '' }),
          id: 'default',
        })
      )
    ).toEqual(['id'])
  })

  it('rejects thresholds outside 0 to 1 and a sample rate above 100', () => {
    const values = toRuleFormValues(createRule([]))
    const result = schema.safeParse({
      ...values,
      content_moderation: {
        ...values.content_moderation,
        sample_rate: 101,
        thresholds: { harassment: '1.5' },
      },
    })

    expect(issuePaths(result).sort()).toEqual([
      'content_moderation.sample_rate',
      'content_moderation.thresholds',
    ])
  })
})

describe('rule conversion', () => {
  it('sends inherited options as empty and keeps engine keyword lists as null', () => {
    const values = toRuleFormValues(createRule([]))
    const rule = toRule({
      ...values,
      match: { ...values.match, user_ids: ['7', ' 9 '] },
      prompt_audit: {
        ...values.prompt_audit,
        category_actions: { pii: 'inherit', jailbreak: 'block' },
      },
    })

    expect(rule.match.user_ids).toEqual([7, 9])
    expect(rule.match.list).toBe('')
    expect(rule.prompt_audit.scan_scope).toBe('')
    expect(rule.prompt_audit.category_actions).toEqual({ jailbreak: 'block' })
    expect(rule.content_moderation.keyword_lists).toBeNull()
  })
})
