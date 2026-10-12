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
 * Splits the alternate website addresses option. The backend accepts the same
 * comma or line-break separators and rejects anything but bare origins.
 */
export function splitAlternateServerAddresses(value: string): string[] {
  return value
    .split(/[,\r\n]+/)
    .map((item) => item.trim())
    .filter((item) => item !== '')
}

export function isValidAlternateServerAddresses(value: string): boolean {
  return splitAlternateServerAddresses(value).every((item) => {
    if (!/^https?:\/\//i.test(item) || item.includes('*')) return false
    try {
      const url = new URL(item)
      return (
        (url.protocol === 'https:' || url.protocol === 'http:') &&
        url.hostname.length > 0 &&
        url.username === '' &&
        url.password === '' &&
        url.pathname === '/' &&
        url.search === '' &&
        url.hash === '' &&
        !item.includes('@')
      )
    } catch {
      return false
    }
  })
}
