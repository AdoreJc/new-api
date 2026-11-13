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
  serverTimeZoneLabel,
  toServerDayRange,
  zonedDateString,
  zonedMidnightUnix,
} from './server-time-zone'

const indianapolis = { name: 'America/Indiana/Indianapolis' }

describe('server time zone dates', () => {
  it('uses the server zone across daylight saving changes', () => {
    // EDT (UTC-4) before 2026-11-01, EST (UTC-5) after.
    expect(zonedMidnightUnix(2026, 10, 15, indianapolis)).toBe(
      Date.UTC(2026, 9, 15, 4) / 1000
    )
    expect(zonedMidnightUnix(2026, 11, 15, indianapolis)).toBe(
      Date.UTC(2026, 10, 15, 5) / 1000
    )
    expect(zonedMidnightUnix(2026, 11, 1, indianapolis)).toBe(
      Date.UTC(2026, 10, 1, 4) / 1000
    )
  })

  it('round-trips dates and handles month ends', () => {
    const start = zonedMidnightUnix(2026, 1, 32, indianapolis)
    expect(zonedDateString(start, indianapolis)).toBe('2026-02-01')
    expect(zonedDateString(start - 1, indianapolis)).toBe('2026-01-31')
  })

  it('falls back to the reported offset without a zone name', () => {
    const zone = { utcOffsetSeconds: 8 * 3600 }
    expect(zonedMidnightUnix(2026, 11, 15, zone)).toBe(
      Date.UTC(2026, 10, 14, 16) / 1000
    )
    expect(serverTimeZoneLabel(zone)).toBe('UTC+08:00')
    expect(serverTimeZoneLabel(indianapolis)).toBe(indianapolis.name)
  })
})

describe('toServerDayRange', () => {
  // Zones a day apart from most browsers, ahead of and behind UTC.
  const zones = [{ name: 'Pacific/Kiritimati' }, { name: 'Pacific/Pago_Pago' }]
  const now = Date.UTC(2026, 10, 13, 2, 30) / 1000

  it('maps the browser\'s today onto the server\'s today', () => {
    const local = new Date(now * 1000)
    const browserMidnight =
      new Date(local.getFullYear(), local.getMonth(), local.getDate()).getTime() / 1000
    for (const zone of zones) {
      const range = toServerDayRange(
        { start_timestamp: browserMidnight, end_timestamp: now },
        zone,
        now
      )
      expect(range.start_timestamp).toBeLessThanOrEqual(now)
      expect(zonedDateString(range.start_timestamp, zone)).toBe(
        zonedDateString(now, zone)
      )
      expect(range.end_timestamp).toBe(now)
    }
  })

  it('keeps past days as whole server days and never ends in the future', () => {
    const local = new Date(now * 1000)
    const day = (back: number) =>
      new Date(local.getFullYear(), local.getMonth(), local.getDate() - back).getTime() / 1000
    for (const zone of zones) {
      const range = toServerDayRange(
        { start_timestamp: day(3), end_timestamp: day(1) - 1 },
        zone,
        now
      )
      const [y, m, d] = zonedDateString(now, zone).split('-').map(Number)
      expect(range.start_timestamp).toBe(zonedMidnightUnix(y, m, d - 3, zone))
      expect(range.end_timestamp).toBe(zonedMidnightUnix(y, m, d - 1, zone) - 1)
      expect(range.end_timestamp).toBeLessThanOrEqual(now)
    }
  })
})
