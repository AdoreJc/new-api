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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, test } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { SubscriptionPurchaseDialog } from '../components/dialogs/subscription-purchase-dialog'
import type { PlanRecord } from '../types'

const planRecord: PlanRecord = {
  plan: {
    id: 1,
    title: 'Pro',
    price_amount: 10,
    currency: 'USD',
    duration_unit: 'month',
    duration_value: 1,
    enabled: true,
    total_amount: 1000,
    allow_balance_pay: true,
  } as PlanRecord['plan'],
}

function setDisplayCurrency(quotaDisplayType: 'USD' | 'CNY') {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType,
      usdExchangeRate: quotaDisplayType === 'CNY' ? 7 : 1,
    },
  })
}

function renderWithQueryClient(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

afterEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
})

describe('SubscriptionPurchaseDialog Epay amount', () => {
  test('with Epay enabled the dialog shows the plan price converted at the price per USD', () => {
    setDisplayCurrency('CNY')

    renderWithQueryClient(
      <SubscriptionPurchaseDialog
        open
        onOpenChange={() => {}}
        plan={planRecord}
        enableOnlineTopUp
        epayMethods={[{ type: 'alipay', name: 'Alipay' }]}
        priceRatio={7.3}
        userQuota={0}
      />
    )

    const amountRow = screen.getByTestId('subscription-epay-amount')
    expect(within(amountRow).getByText('Online payment amount')).toBeVisible()
    expect(within(amountRow).getByText('$73')).toBeVisible()
    expect(within(amountRow).queryByText(/\$10/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pay $73' })).toBeVisible()
    // The USD list price is still what the balance path charges.
    expect(screen.getByText('$10.00')).toBeVisible()
  })

  test('a price ratio of 1 charges the USD list price', () => {
    setDisplayCurrency('USD')

    renderWithQueryClient(
      <SubscriptionPurchaseDialog
        open
        onOpenChange={() => {}}
        plan={planRecord}
        enableOnlineTopUp
        epayMethods={[{ type: 'alipay', name: 'Alipay' }]}
        priceRatio={1}
        userQuota={0}
      />
    )

    const amountRow = screen.getByTestId('subscription-epay-amount')
    expect(within(amountRow).getByText('$10')).toBeVisible()
    expect(within(amountRow).queryByText(/73/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pay $10' })).toBeVisible()
  })

  test('with the CNY display type the payment amount is still shown in $', () => {
    setDisplayCurrency('CNY')

    renderWithQueryClient(
      <SubscriptionPurchaseDialog
        open
        onOpenChange={() => {}}
        plan={{
          plan: { ...planRecord.plan, price_amount: 27.94 },
        }}
        enableOnlineTopUp
        epayMethods={[{ type: 'alipay', name: 'Alipay' }]}
        priceRatio={7.3}
        userQuota={0}
      />
    )

    // 27.94 USD × 7.3 per USD = 203.962 → charged as $203.96, not ¥ or × 7.
    const amountRow = screen.getByTestId('subscription-epay-amount')
    expect(within(amountRow).getByText('$203.96')).toBeVisible()
    expect(within(amountRow).queryByText(/¥/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pay $203.96' })).toBeVisible()
  })

  test('without Epay no online payment amount is rendered', () => {
    setDisplayCurrency('CNY')

    renderWithQueryClient(
      <SubscriptionPurchaseDialog
        open
        onOpenChange={() => {}}
        plan={planRecord}
        enableOnlineTopUp={false}
        epayMethods={[]}
        priceRatio={7.3}
        userQuota={0}
      />
    )

    expect(
      screen.queryByTestId('subscription-epay-amount')
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Online payment amount')).not.toBeInTheDocument()
  })
})
