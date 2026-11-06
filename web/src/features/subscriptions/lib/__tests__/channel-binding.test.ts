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
  isSubscriptionUsageWindow,
  parseSubscriptionUsageWindows,
} from '../channel-binding'

describe('parseSubscriptionUsageWindows', () => {
  test('labels the seven_day_overage_included window as the Fable window and keeps API windows as api', () => {
    const windows = parseSubscriptionUsageWindows({
      subscription_id: 1,
      provider: 'claude',
      channel_type: 64,
      windows: {
        five_hour: { utilization: 12, resets_at: '2026-11-06T03:00:00Z' },
        seven_day: { utilization: 40 },
        seven_day_overage_included: { utilization: 37.5 },
      },
      limit_reached: false,
      fetched_at: 0,
    })

    expect(windows.map((w) => w.key)).toEqual([
      'five_hour',
      'seven_day',
      'seven_day_overage_included',
    ])
    const fable = windows[2]
    expect(fable.labelKey).toBe('7-Day Fable Window')
    expect(isSubscriptionUsageWindow(fable) && fable.source).toBe('api')
    expect(isSubscriptionUsageWindow(fable) && fable.sampledAt).toBeNull()
  })

  test('marks a window the server filled from sampled headers as passive with its sample time', () => {
    const windows = parseSubscriptionUsageWindows({
      subscription_id: 1,
      provider: 'claude',
      channel_type: 64,
      windows: {
        five_hour: { utilization: 12 },
        seven_day: { utilization: 40 },
        seven_day_overage_included: {
          utilization: 37.5,
          resets_at: '2026-11-09T00:00:00Z',
          source: 'passive',
          sampled_at: '2026-11-05T12:00:00Z',
        },
      },
      limit_reached: false,
      fetched_at: 0,
    })

    const fable = windows[2]
    expect(isSubscriptionUsageWindow(fable)).toBe(true)
    expect(fable).toMatchObject({
      utilization: 37.5,
      resetsAt: '2026-11-09T00:00:00Z',
      source: 'passive',
      sampledAt: '2026-11-05T12:00:00Z',
    })
  })
})
