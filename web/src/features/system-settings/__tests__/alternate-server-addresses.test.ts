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
import assert from 'node:assert/strict'

import { describe, test } from 'vitest'

import {
  isValidAlternateServerAddresses,
  splitAlternateServerAddresses,
} from '../general/alternate-server-addresses'

describe('alternate server addresses', () => {
  test('splits entries on commas and line breaks and drops blanks', () => {
    assert.deepEqual(
      splitAlternateServerAddresses(
        ' https://line2.example.com ,\nhttp://10.0.0.8:3000\r\n\n'
      ),
      ['https://line2.example.com', 'http://10.0.0.8:3000']
    )
  })

  test('accepts an empty list or bare HTTP(S) origins', () => {
    for (const value of [
      '',
      'https://line2.example.com',
      'https://line2.example.com/',
      'https://line2.example.com\nhttp://10.0.0.8:3000',
    ]) {
      assert.equal(isValidAlternateServerAddresses(value), true, value)
    }
  })

  test('rejects paths, wildcards, credentials, queries, and non-HTTP entries', () => {
    for (const value of [
      'line2.example.com',
      'https://line2.example.com/console',
      'https://*.example.com',
      'https://user@line2.example.com',
      'https://line2.example.com?a=1',
      'https://line2.example.com#top',
      'ftp://line2.example.com',
      'https://line2.example.com,https://line3.example.com/app',
    ]) {
      assert.equal(isValidAlternateServerAddresses(value), false, value)
    }
  })
})
