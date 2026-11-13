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
import { describe, expect, test } from 'vitest'

import {
  AUTO_RESET_MAX_NODES,
  type AutoResetRule,
  addAutoResetAlarm,
  canAddAutoResetChild,
  countAutoResetConditions,
  createAutoResetCondition,
  toEditableAutoResetConfig,
  updateAutoResetNode,
} from '../auto-reset-rule'

const fiveHour = createAutoResetCondition('five_hour_used_pct')

describe('updateAutoResetNode', () => {
  test('updating a nested condition leaves the original tree unchanged', () => {
    const root: AutoResetRule = {
      op: 'or',
      children: [fiveHour, { op: 'and', children: [fiveHour] }],
    }

    const next = updateAutoResetNode(root, [1, 0], (node) => ({
      ...node,
      threshold: 75,
    }))

    expect(next.children?.[1].children?.[0].threshold).toBe(75)
    expect(root.children?.[1].children?.[0].threshold).toBe(90)
  })

  test('returning null removes the node from its group', () => {
    const root: AutoResetRule = { op: 'and', children: [fiveHour, fiveHour] }

    const next = updateAutoResetNode(root, [0], () => null)

    expect(next.children).toHaveLength(1)
  })
})

describe('canAddAutoResetChild', () => {
  test('a group at the second level cannot hold another group', () => {
    const root: AutoResetRule = { op: 'or', children: [] }

    expect(canAddAutoResetChild(root, 1, true)).toBe(true)
    expect(canAddAutoResetChild(root, 2, true)).toBe(false)
    expect(canAddAutoResetChild(root, 2, false)).toBe(true)
    expect(canAddAutoResetChild(root, 3, false)).toBe(false)
  })

  test('a full tree accepts no more nodes', () => {
    const root: AutoResetRule = {
      op: 'or',
      children: Array.from(
        { length: AUTO_RESET_MAX_NODES - 1 },
        () => fiveHour
      ),
    }

    expect(canAddAutoResetChild(root, 1, false)).toBe(false)
  })
})

describe('addAutoResetAlarm', () => {
  test('adding the same time twice keeps one sorted entry', () => {
    let node = createAutoResetCondition('at_time')
    node = addAutoResetAlarm(node, '2026-11-13T09:00:00Z')
    node = addAutoResetAlarm(node, '2026-11-13T01:00:00Z')
    node = addAutoResetAlarm(node, '2026-11-13T09:00:00Z')

    expect(node.times).toEqual(['2026-11-13T01:00:00Z', '2026-11-13T09:00:00Z'])
  })
})

describe('toEditableAutoResetConfig', () => {
  test('a stored bare condition is wrapped in an OR group', () => {
    const config = toEditableAutoResetConfig({
      enabled: true,
      rule: fiveHour,
      min_interval_minutes: 60,
      max_per_day: 2,
      max_per_week: 5,
      skip_if_reset_within_minutes: 15,
      alarm_ignores_reset_soon: true,
      expiring_ignores_reset_soon: true,
    })

    expect(config.rule).toMatchObject({
      op: 'or',
      children: [{ type: 'five_hour_used_pct', threshold: 90 }],
    })
    expect(config.rule?.id).toBeTruthy()
    expect(config.rule?.children?.[0].id).toBeTruthy()
    expect(countAutoResetConditions(config.rule)).toBe(1)
  })
})
