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
  isClaudeUsageWindow,
  parseClaudeResetCredits,
  parseClaudeUsageWindows,
} from '../claude-usage'

describe('parseClaudeUsageWindows', () => {
  test('returns both base windows as empty when the payload has no windows', () => {
    const windows = parseClaudeUsageWindows({})

    expect(windows.map((w) => w.key)).toEqual(['five_hour', 'seven_day'])
    expect(windows.some(isClaudeUsageWindow)).toBe(false)
  })

  test('keeps optional windows only when upstream returned them and clamps utilization', () => {
    const windows = parseClaudeUsageWindows({
      five_hour: { utilization: 142, resets_at: '2026-11-05T03:00:00Z' },
      seven_day: { utilization: -5, resets_at: null },
      seven_day_sonnet: null,
      seven_day_overage_included: { utilization: '12.5' },
    })

    expect(windows.map((w) => w.key)).toEqual([
      'five_hour',
      'seven_day',
      'seven_day_overage_included',
    ])
    const [fiveHour, sevenDay, overage] = windows
    expect(fiveHour).toMatchObject({
      utilization: 100,
      resetsAt: '2026-11-05T03:00:00Z',
    })
    expect(sevenDay).toMatchObject({ utilization: 0, resetsAt: null })
    expect(overage).toMatchObject({ utilization: 12.5, resetsAt: null })
  })

  test('ignores a non-object payload', () => {
    expect(parseClaudeUsageWindows('oops').map((w) => w.key)).toEqual([
      'five_hour',
      'seven_day',
    ])
  })
})

describe('parseClaudeResetCredits', () => {
  test('returns null for a non-object payload', () => {
    expect(parseClaudeResetCredits(undefined)).toBeNull()
    expect(parseClaudeResetCredits('x')).toBeNull()
  })

  test('normalizes credits and drops malformed entries', () => {
    const view = parseClaudeResetCredits({
      eligible: true,
      at_limit: false,
      available_count: '2',
      cooldown_until: null,
      weekly_resets_at: '2026-11-10T00:00:00Z',
      credits: [
        {
          label: 'Weekly reset',
          resets_left: 2,
          clears: ['five_hour', 7],
          percent_used: { five_hour: '80', seven_day: 'n/a' },
          blocking: [],
          use_requires_limit: true,
          redeemable: true,
        },
        'bad',
        { resets_left: 'x', redeemable: 'yes' },
      ],
    })

    expect(view).not.toBeNull()
    expect(view?.available_count).toBe(2)
    expect(view?.weekly_resets_at).toBe('2026-11-10T00:00:00Z')
    expect(view?.credits).toHaveLength(2)
    expect(view?.credits[0]).toEqual({
      label: 'Weekly reset',
      resets_left: 2,
      starts_at: null,
      expires_at: null,
      clears: ['five_hour'],
      percent_used: { five_hour: 80 },
      blocking: [],
      use_requires_limit: true,
      redeemable: true,
    })
    expect(view?.credits[1]).toMatchObject({
      label: '',
      resets_left: 0,
      redeemable: false,
    })
  })
})
