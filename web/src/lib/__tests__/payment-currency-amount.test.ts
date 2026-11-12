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
import { afterEach, describe, expect, test } from 'vitest'

import {
  formatLocalCurrencyAmount,
  formatPaymentCurrencyAmount,
} from '@/lib/currency'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
  type CurrencyDisplayType,
} from '@/stores/system-config-store'

function setDisplayType(quotaDisplayType: CurrencyDisplayType) {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType,
      usdExchangeRate: 7,
      customCurrencySymbol: '€',
      customCurrencyExchangeRate: 0.9,
    },
  })
}

afterEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
})

describe('formatPaymentCurrencyAmount', () => {
  test.each<CurrencyDisplayType>(['USD', 'CNY', 'CUSTOM', 'TOKENS'])(
    'always renders payment amounts as $ when the display type is %s',
    (displayType) => {
      setDisplayType(displayType)

      expect(formatPaymentCurrencyAmount(27.94, { locale: 'en-US' })).toBe(
        '$27.94'
      )
    }
  )

  test('does not apply any exchange rate', () => {
    setDisplayType('CNY')

    expect(formatPaymentCurrencyAmount(73, { locale: 'en-US' })).toBe('$73')
  })

  test('differs from formatLocalCurrencyAmount under the CNY display type', () => {
    setDisplayType('CNY')

    expect(formatLocalCurrencyAmount(27.94, { locale: 'en-US' })).toBe('¥27.94')
    expect(formatPaymentCurrencyAmount(27.94, { locale: 'en-US' })).toBe(
      '$27.94'
    )
  })

  test('uses at most two fraction digits and does not abbreviate', () => {
    expect(formatPaymentCurrencyAmount(203.926, { locale: 'en-US' })).toBe(
      '$203.93'
    )
    expect(formatPaymentCurrencyAmount(0.126, { locale: 'en-US' })).toBe(
      '$0.13'
    )
    expect(formatPaymentCurrencyAmount(12345.6, { locale: 'en-US' })).toBe(
      '$12,345.6'
    )
  })

  test('respects explicit digit options', () => {
    expect(
      formatPaymentCurrencyAmount(1.23456, {
        locale: 'en-US',
        digitsLarge: 4,
      })
    ).toBe('$1.2346')
  })

  test('keeps the plain $ symbol for zh locales', () => {
    setDisplayType('CNY')

    expect(formatPaymentCurrencyAmount(27.94, { locale: 'zh-CN' })).toBe(
      '$27.94'
    )
    expect(formatPaymentCurrencyAmount(27.94, { locale: 'zh-TW' })).toBe(
      '$27.94'
    )
  })

  test.each([null, undefined, Number.NaN])('returns "-" for %s', (value) => {
    expect(formatPaymentCurrencyAmount(value)).toBe('-')
  })
})
