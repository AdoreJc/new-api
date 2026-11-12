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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'

import { PaymentMethodsVisualEditor } from '../payment-methods-visual-editor'
import { isPaymentMethodEnabled, setPaymentMethodEnabled } from '../utils'

const payMethods = JSON.stringify([
  { name: 'Alipay', type: 'alipay' },
  { name: 'WeChat', type: 'wxpay', provider: 'stripe', enabled: 'false' },
])

function renderEditor(value: string) {
  const onChange = vi.fn<(value: string) => void>()
  render(<PaymentMethodsVisualEditor value={value} onChange={onChange} />)
  return onChange
}

// The editor renders a desktop table and a mobile card list; both carry the
// same switch, so the first match is the desktop one.
function getRowSwitch(name: string) {
  return screen.getAllByRole('switch', {
    name: `Enable payment method ${name}`,
  })[0]
}

describe('payment method switches', () => {
  test('entries without the enabled field render as switched on', () => {
    renderEditor(payMethods)

    expect(getRowSwitch('Alipay')).toHaveAttribute('aria-checked', 'true')
    expect(getRowSwitch('WeChat')).toHaveAttribute('aria-checked', 'false')
  })

  test('switching a method off stores enabled "false" on that entry only', () => {
    const onChange = renderEditor(payMethods)

    fireEvent.click(getRowSwitch('Alipay'))

    expect(JSON.parse(onChange.mock.calls[0][0])).toEqual([
      { name: 'Alipay', type: 'alipay', enabled: 'false' },
      { name: 'WeChat', type: 'wxpay', provider: 'stripe', enabled: 'false' },
    ])
  })

  test('switching a method back on removes the enabled field', () => {
    const onChange = renderEditor(payMethods)

    fireEvent.click(getRowSwitch('WeChat'))

    expect(JSON.parse(onChange.mock.calls[0][0])).toEqual([
      { name: 'Alipay', type: 'alipay' },
      { name: 'WeChat', type: 'wxpay', provider: 'stripe' },
    ])
  })

  test('only the value "false" switches a method off', () => {
    expect(isPaymentMethodEnabled({})).toBe(true)
    expect(isPaymentMethodEnabled({ enabled: 'true' })).toBe(true)
    expect(isPaymentMethodEnabled({ enabled: 'false' })).toBe(false)
  })

  test('invalid JSON is returned unchanged instead of being overwritten', () => {
    expect(
      setPaymentMethodEnabled('[{', { name: 'Alipay', type: 'alipay' }, false)
    ).toBe('[{')
  })
})
