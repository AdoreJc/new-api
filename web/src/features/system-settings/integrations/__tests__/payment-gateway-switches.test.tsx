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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState, type ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { PaymentSettingsSection } from '../payment-settings-section'

type SectionProps = ComponentProps<typeof PaymentSettingsSection>

const baseDefaults: SectionProps['defaultValues'] = {
  PayAddress: '',
  EpayEnabled: true,
  EpayId: '',
  EpayKey: '',
  EpaySignType: 'MD5',
  EpayMerchantPrivateKey: '',
  EpayPlatformPublicKey: '',
  Price: 7.3,
  MinTopUp: 1,
  CustomCallbackAddress: '',
  PayMethods: '[]',
  AmountOptions: '[]',
  AmountDiscount: '{}',
  StripeEnabled: true,
  StripeApiSecret: '',
  StripeWebhookSecret: '',
  StripePriceId: '',
  StripeUnitPrice: 8,
  StripeMinTopUp: 1,
  StripePromotionCodesEnabled: false,
  CreemEnabled: true,
  CreemApiKey: '',
  CreemWebhookSecret: '',
  CreemTestMode: false,
  CreemProducts: '[]',
  WaffoPancakeEnabled: true,
}

const waffoDefaults: SectionProps['waffoDefaultValues'] = {
  WaffoEnabled: false,
  WaffoApiKey: '',
  WaffoPrivateKey: '',
  WaffoPublicCert: '',
  WaffoSandboxPublicCert: '',
  WaffoSandboxApiKey: '',
  WaffoSandboxPrivateKey: '',
  WaffoSandbox: false,
  WaffoMerchantId: '',
  WaffoCurrency: 'USD',
  WaffoUnitPrice: 1,
  WaffoMinTopUp: 1,
  WaffoNotifyUrl: '',
  WaffoReturnUrl: '',
  WaffoPayMethods: '[]',
}

function PaymentFixture(props: { defaults: SectionProps['defaultValues'] }) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  return (
    <SettingsPageProvider actionsContainer={actions}>
      <div ref={setActions} />
      <PaymentSettingsSection
        defaultValues={props.defaults}
        waffoDefaultValues={waffoDefaults}
        waffoPancakeDefaultValues={{
          WaffoPancakeMerchantID: '',
          WaffoPancakePrivateKey: '',
          WaffoPancakeReturnURL: '',
        }}
        complianceDefaults={{
          confirmed: true,
          termsVersion: 'v1',
          confirmedAt: 0,
          confirmedBy: 1,
        }}
      />
    </SettingsPageProvider>
  )
}

function renderPayment(defaults: Partial<SectionProps['defaultValues']> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <PaymentFixture defaults={{ ...baseDefaults, ...defaults }} />
    </QueryClientProvider>
  )
  return userEvent.setup()
}

afterEach(() => vi.restoreAllMocks())

describe('payment gateway switches', () => {
  it.each([
    ['Epay', 'Enable Epay', 'EpayEnabled'],
    ['Stripe', 'Enable Stripe', 'StripeEnabled'],
    ['Creem', 'Enable Creem', 'CreemEnabled'],
    ['Waffo Pancake', 'Enable Waffo Pancake', 'WaffoPancakeEnabled'],
  ])(
    'switching %s off saves only its switch option',
    async (tab, label, key) => {
      const request = vi
        .spyOn(api, 'put')
        .mockResolvedValue({ data: { success: true } })
      const user = renderPayment()

      await user.click(screen.getByRole('tab', { name: tab }))
      const gatewaySwitch = screen.getByRole('switch', { name: label })
      expect(gatewaySwitch).toHaveAttribute('aria-checked', 'true')
      await user.click(gatewaySwitch)
      await user.click(
        screen.getByRole('button', { name: 'Save all settings' })
      )

      await waitFor(() =>
        expect(request).toHaveBeenCalledWith('/api/option/', {
          key,
          value: false,
        })
      )
      expect(request).toHaveBeenCalledTimes(1)
    }
  )

  it('a gateway switched off on the server renders as off', async () => {
    const user = renderPayment({ StripeEnabled: false })

    await user.click(screen.getByRole('tab', { name: 'Stripe' }))

    expect(
      screen.getByRole('switch', { name: 'Enable Stripe' })
    ).toHaveAttribute('aria-checked', 'false')
  })
})
