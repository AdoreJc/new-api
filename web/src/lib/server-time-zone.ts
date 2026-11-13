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
/**
 * The server's time zone, which splits calendar days for cost accounting.
 * `name` is an IANA zone (from the server's TZ); when the server reports
 * none, the current UTC offset is used for every date.
 */
export type ServerTimeZone = {
  name?: string
  utcOffsetSeconds?: number
}

function isValidTimeZone(name: string | undefined): name is string {
  if (!name) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: name })
    return true
  } catch {
    return false
  }
}

/** Offset of the zone from UTC at an instant, in seconds. */
function zoneOffsetSeconds(zone: ServerTimeZone, unix: number): number {
  if (!isValidTimeZone(zone.name)) {
    return (
      zone.utcOffsetSeconds ?? -new Date(unix * 1000).getTimezoneOffset() * 60
    )
  }
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: zone.name,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(new Date(unix * 1000))
  const value = (type: string) =>
    Number(parts.find((part) => part.type === type)?.value ?? 0)
  const asUTC = Date.UTC(
    value('year'),
    value('month') - 1,
    value('day'),
    value('hour'),
    value('minute'),
    value('second')
  )
  return Math.round(asUTC / 1000 - unix)
}

/** Unix time of 00:00 on the given calendar date in the zone. */
export function zonedMidnightUnix(
  year: number,
  month: number,
  day: number,
  zone: ServerTimeZone
): number {
  const wall = Date.UTC(year, month - 1, day) / 1000
  // Two passes settle the offset across a DST change.
  let unix = wall - zoneOffsetSeconds(zone, wall)
  unix = wall - zoneOffsetSeconds(zone, unix)
  return unix
}

/** Calendar date (YYYY-MM-DD) of an instant in the zone. */
export function zonedDateString(unix: number, zone: ServerTimeZone): string {
  const shifted = new Date((unix + zoneOffsetSeconds(zone, unix)) * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${shifted.getUTCFullYear()}-${pad(shifted.getUTCMonth() + 1)}-${pad(shifted.getUTCDate())}`
}

/** Readable zone label for notes, e.g. "America/Indiana/Indianapolis". */
export function serverTimeZoneLabel(zone: ServerTimeZone): string {
  if (isValidTimeZone(zone.name)) return zone.name
  const offset = zone.utcOffsetSeconds ?? 0
  const sign = offset < 0 ? '-' : '+'
  const minutes = Math.abs(offset) / 60
  const pad = (n: number) => String(Math.floor(n)).padStart(2, '0')
  return `UTC${sign}${pad(minutes / 60)}:${pad(minutes % 60)}`
}

/**
 * Maps a range picked in the browser's calendar onto the server's calendar.
 * Days are matched by their distance from today, so the browser's "today" is
 * the server's "today" even when the two zones are on different dates: the
 * start becomes server midnight of the same day back, and an end before now
 * becomes the end of that server day (never later than now).
 */
export function toServerDayRange(
  range: { start_timestamp: number; end_timestamp: number },
  zone: ServerTimeZone,
  now: number = Math.floor(Date.now() / 1000)
): { start_timestamp: number; end_timestamp: number } {
  const browserDay = (unix: number) => {
    const date = new Date(unix * 1000)
    return Math.round(
      Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / 86_400_000
    )
  }
  const today = browserDay(now)
  const [year, month, day] = zonedDateString(now, zone).split('-').map(Number)
  const serverMidnight = (daysBack: number) =>
    zonedMidnightUnix(year, month, day - daysBack, zone)

  const start = serverMidnight(today - browserDay(range.start_timestamp))
  if (range.end_timestamp >= now - 60) {
    return { start_timestamp: start, end_timestamp: range.end_timestamp }
  }
  const endDaysBack = today - browserDay(range.end_timestamp)
  const end = endDaysBack <= 0 ? now : serverMidnight(endDaysBack - 1) - 1
  return { start_timestamp: start, end_timestamp: Math.min(end, now) }
}
