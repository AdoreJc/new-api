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
  editorKey,
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

describe('rule form limits', () => {
  const schema = createRuleFormSchema({ otherIds: [], isDefault: false })
  const base = () => ({ ...toRuleFormValues(createRule([])), id: 'limits' })

  it('counts a rule name in characters, as the server does', () => {
    expect(
      schema.safeParse({ ...base(), name: '规'.repeat(128) }).success
    ).toBe(true)
    expect(
      issuePaths(schema.safeParse({ ...base(), name: '规'.repeat(129) }))
    ).toEqual(['name'])
  })

  it('rejects more patterns, ids or a longer reroute group than the server accepts', () => {
    const values = base()
    const patterns = Array.from({ length: 201 }, (_, index) => `g${index}`)
    const ids = Array.from({ length: 1001 }, (_, index) => String(index + 1))
    expect(
      issuePaths(
        schema.safeParse({
          ...values,
          match: { ...values.match, groups: patterns },
        })
      )
    ).toEqual(['match.groups'])
    expect(
      issuePaths(
        schema.safeParse({
          ...values,
          match: { ...values.match, user_ids: ids },
        })
      )
    ).toEqual(['match.user_ids'])
    expect(
      issuePaths(
        schema.safeParse({
          ...rerouteValues({ model: '', group: 'g'.repeat(65) }),
        })
      )
    ).toEqual(['reroute.group'])
  })

  it('needs a keyword list when a rule sets its own keyword mode', () => {
    const values = base()
    const moderation = {
      ...values.content_moderation,
      keyword_mode: 'keyword_only',
      override_keyword_lists: true,
      keyword_lists: [],
    }
    expect(
      issuePaths(
        schema.safeParse({ ...values, content_moderation: moderation })
      )
    ).toEqual(['content_moderation.keyword_lists'])
  })
})

describe('editor key', () => {
  const config = (version: number, groups: string[]) => ({
    config_version: version,
    rules: [{ ...createRule([]), match: { ...createRule([]).match, groups } }],
  })

  it('changes with the content of derived rules, which keep their version', () => {
    expect(editorKey({ saved: false, config: config(2, ['a']) })).not.toBe(
      editorKey({ saved: false, config: config(2, ['b']) })
    )
  })

  it('follows the version of a saved table', () => {
    expect(editorKey({ saved: true, config: config(3, ['a']) })).toBe(
      editorKey({ saved: true, config: config(3, ['b']) })
    )
    expect(editorKey({ saved: true, config: config(3, ['a']) })).not.toBe(
      editorKey({ saved: true, config: config(4, ['a']) })
    )
  })
})
