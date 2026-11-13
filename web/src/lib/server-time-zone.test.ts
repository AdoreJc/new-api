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
