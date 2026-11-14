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
  bindableChannelStatusNoteKey,
  boundAccountLabelKey,
  isSubscriptionBound,
  isSubscriptionUsageWindow,
  parseSubscriptionUsageWindows,
  subscriptionBoundGroupName,
} from '../lib/channel-binding'
import type { SubscriptionUsageData } from '../types'

describe('boundAccountLabelKey', () => {
  test('maps the two OAuth channel types and falls back for anything else', () => {
    expect(boundAccountLabelKey(64)).toBe('Claude Subscription (OAuth)')
    expect(boundAccountLabelKey(57)).toBe('ChatGPT Subscription (Codex)')
    expect(boundAccountLabelKey(1)).toBe('OAuth account')
    expect(boundAccountLabelKey(undefined)).toBe('OAuth account')
  })
})

describe('subscriptionBoundGroupName', () => {
  test('names the dedicated group by channel type and rejects other types', () => {
    expect(subscriptionBoundGroupName({ id: 12, type: 64 })).toBe('claude-ch12')
    expect(subscriptionBoundGroupName({ id: 12, type: 57 })).toBe('codex-ch12')
    expect(subscriptionBoundGroupName({ id: 12, type: 1 })).toBe('')
  })
})

describe('bindableChannelStatusNoteKey', () => {
  test('a manually disabled channel says binding enables it and release disables it again', () => {
    expect(bindableChannelStatusNoteKey({ id: 12, status: 2 }, 0)).toBe(
      'The channel is manually disabled. Binding enables it, and releasing the binding disables it again.'
    )
  })

  test('an auto-disabled channel asks for a new authorization, also when it is the bound one', () => {
    const note =
      'The channel is auto-disabled. Authorize the account again and enable it manually before it can serve requests.'
    expect(bindableChannelStatusNoteKey({ id: 12, status: 3 }, 0)).toBe(note)
    expect(bindableChannelStatusNoteKey({ id: 12, status: 3 }, 12)).toBe(note)
  })

  test('an enabled channel and the already bound manually disabled channel get no note', () => {
    expect(bindableChannelStatusNoteKey({ id: 12, status: 1 }, 0)).toBe('')
    expect(bindableChannelStatusNoteKey({ id: 12, status: 2 }, 12)).toBe('')
  })
})

describe('isSubscriptionBound', () => {
  test('treats a positive channel id as bound and 0, undefined or null as unbound', () => {
    expect(isSubscriptionBound({ bound_channel_id: 3 })).toBe(true)
    expect(isSubscriptionBound({ bound_channel_id: 0 })).toBe(false)
    expect(isSubscriptionBound({})).toBe(false)
    expect(isSubscriptionBound(null)).toBe(false)
  })
})

describe('parseSubscriptionUsageWindows', () => {
  test('Claude keeps the base windows in order and adds optional ones only when present', () => {
    const data: SubscriptionUsageData = {
      subscription_id: 1,
      provider: 'claude',
      channel_type: 64,
      windows: {
        seven_day_sonnet: {
          utilization: 150,
          resets_at: '2026-11-09T00:00:00Z',
        },
        five_hour: { utilization: 12.5, resets_at: '2026-11-05T10:00:00Z' },
      },
      limit_reached: true,
      fetched_at: 1,
    }

    const windows = parseSubscriptionUsageWindows(data)

    expect(windows.map((w) => w.key)).toEqual([
      'five_hour',
      'seven_day',
      'seven_day_sonnet',
    ])
    expect(windows[0]).toMatchObject({
      utilization: 12.5,
      resetsAt: '2026-11-05T10:00:00Z',
      windowSeconds: null,
    })
    expect(isSubscriptionUsageWindow(windows[1])).toBe(false)
    expect(windows[2]).toMatchObject({ utilization: 100 })
  })

  test('Codex exposes the primary and secondary windows with their length', () => {
    const data: SubscriptionUsageData = {
      subscription_id: 1,
      provider: 'codex',
      channel_type: 57,
      windows: {
        primary: { utilization: 40, resets_at: '', window_seconds: 18000 },
      },
      plan_type: 'plus',
      limit_reached: false,
      fetched_at: 1,
    }

    const windows = parseSubscriptionUsageWindows(data)

    expect(windows.map((w) => w.key)).toEqual(['primary', 'secondary'])
    expect(windows[0]).toMatchObject({
      utilization: 40,
      resetsAt: null,
      windowSeconds: 18000,
    })
    expect(isSubscriptionUsageWindow(windows[1])).toBe(false)
  })

  test('returns nothing without data', () => {
    expect(parseSubscriptionUsageWindows(null)).toEqual([])
    expect(parseSubscriptionUsageWindows(undefined)).toEqual([])
  })
})
