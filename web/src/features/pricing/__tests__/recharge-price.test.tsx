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
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
  type CurrencyConfig,
} from '@/stores/system-config-store'

import { DynamicPricingBreakdown } from '../components/dynamic-pricing-breakdown'
import { ModelPriceCell } from '../components/model-price-cell'
import { getDynamicPricingSummary } from '../lib/dynamic-price'
import {
  formatFixedPrice,
  formatGroupPrice,
  formatPrice,
  formatRequestPrice,
} from '../lib/price'
import type { PricingModel } from '../types'

// The site settles in USD. PRICE_RATE is the "Price" setting (US dollars paid
// for $1 of credit); a value other than 1 shows that recharge prices still
// multiply by it. Production runs with Price = 1 (see the production case).
const PRICE_RATE = 1.5
const groupRatio = { default: 1, vip: 2 }

const tokenModel: PricingModel = {
  id: 1,
  model_name: 'token-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 3,
  cache_ratio: 0.1,
  enable_groups: ['default'],
  group_ratio: groupRatio,
}

const requestModel: PricingModel = {
  id: 2,
  model_name: 'request-model',
  quota_type: 1,
  model_ratio: 0,
  completion_ratio: 1,
  model_price: 0.5,
  enable_groups: ['default'],
  group_ratio: groupRatio,
}

const tieredModel: PricingModel = {
  id: 3,
  model_name: 'tiered-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  group_ratio: groupRatio,
  billing_mode: 'tiered_expr',
  billing_expr: 'tier("base", p * 2 + c * 6 + cr * 0.2)',
}

const taskModel: PricingModel = {
  id: 4,
  model_name: 'task-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  group_ratio: groupRatio,
  billing_mode: 'tiered_expr',
  billing_expr: 'tier("base", u("seconds") * 0.5)',
  billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
}

type Site = { name: string; currency: Partial<CurrencyConfig> }

const sites: Array<Site & { standardInput: string; standardRequest: string }> =
  [
    {
      name: 'USD site',
      currency: { quotaDisplayType: 'USD', usdExchangeRate: 1 },
      standardInput: '$2',
      standardRequest: '$0.5',
    },
    {
      name: 'CNY site',
      currency: { quotaDisplayType: 'CNY', usdExchangeRate: 7 },
      standardInput: '¥14',
      standardRequest: '¥3.5',
    },
    {
      name: 'CUSTOM site',
      currency: {
        quotaDisplayType: 'CUSTOM',
        customCurrencySymbol: 'pts',
        customCurrencyExchangeRate: 10,
      },
      standardInput: 'pts 20',
      standardRequest: 'pts 5',
    },
    {
      name: 'TOKENS site',
      currency: { quotaDisplayType: 'TOKENS', usdExchangeRate: 1 },
      standardInput: '$2',
      standardRequest: '$0.5',
    },
  ]

function useSite(currency: Partial<CurrencyConfig>) {
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG, ...currency } })
}

const recharge = {
  tokenUnit: 'M' as const,
  showRechargePrice: true,
  priceRate: PRICE_RATE,
  usdExchangeRate: 1,
}

beforeEach(() => useSite({ quotaDisplayType: 'USD', usdExchangeRate: 1 }))
afterEach(() => {
  cleanup()
  useSite({})
})

describe.each(sites)('recharge price on a $name', (site) => {
  beforeEach(() => useSite(site.currency))

  it('shows token, cached and group token prices in USD at the recharge price', () => {
    expect(formatPrice(tokenModel, 'input', 'M', true, PRICE_RATE, 7)).toBe(
      '$3'
    )
    expect(formatPrice(tokenModel, 'output', 'M', true, PRICE_RATE, 7)).toBe(
      '$9'
    )
    expect(formatPrice(tokenModel, 'cache', 'M', true, PRICE_RATE, 7)).toBe(
      '$0.3'
    )
    expect(
      formatGroupPrice(
        tokenModel,
        'vip',
        'input',
        'M',
        true,
        PRICE_RATE,
        7,
        groupRatio
      )
    ).toBe('$6')
  })

  it('shows per-request and group per-request prices in USD at the recharge price', () => {
    expect(formatRequestPrice(requestModel, true, PRICE_RATE, 7)).toBe('$0.75')
    expect(
      formatFixedPrice(requestModel, 'vip', true, PRICE_RATE, 7, groupRatio)
    ).toBe('$1.5')
  })

  it('shows tiered expression and task usage prices in USD at the recharge price', () => {
    const tiered = getDynamicPricingSummary(tieredModel, recharge)
    expect(tiered?.primaryEntries.map((entry) => entry.formatted)).toEqual([
      '$3',
      '$9',
    ])
    const task = getDynamicPricingSummary(taskModel, recharge)
    expect(task?.primaryEntries[0]?.formatted).toBe('$0.75')
  })

  it('keeps standard prices in the site display currency when the recharge switch is off', () => {
    expect(formatPrice(tokenModel, 'input', 'M', false, PRICE_RATE, 7)).toBe(
      site.standardInput
    )
    expect(formatRequestPrice(requestModel, false, PRICE_RATE, 7)).toBe(
      site.standardRequest
    )
    const tiered = getDynamicPricingSummary(tieredModel, {
      ...recharge,
      showRechargePrice: false,
    })
    expect(tiered?.primaryEntries[0]?.formatted).toBe(site.standardInput)
  })
})

describe('recharge price with the production settings', () => {
  it('shows the USD price unchanged when Price is 1 on a USD site', () => {
    expect(formatPrice(tokenModel, 'input', 'M', true, 1, 1)).toBe('$2')
    expect(formatRequestPrice(requestModel, true, 1, 1)).toBe('$0.5')
    expect(
      formatGroupPrice(tokenModel, 'vip', 'input', 'M', true, 1, 1, groupRatio)
    ).toBe('$4')
  })
})

describe('recharge price currency label', () => {
  it('labels token prices USD per 1M tokens in recharge mode', () => {
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('USD / 1M tokens')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('labels per-request prices USD per request in recharge mode', () => {
    render(
      <ModelPriceCell
        model={requestModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('USD / request')).toBeInTheDocument()
    expect(screen.getByText('0.75')).toBeInTheDocument()
  })

  it('shows the plain USD price when the recharge switch is off', () => {
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: false, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('USD / 1M tokens')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
  })

  it('labels recharge prices USD on a CNY site without converting to RMB', () => {
    useSite({ quotaDisplayType: 'CNY', usdExchangeRate: 7 })
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{
          showRechargePrice: true,
          priceRate: PRICE_RATE,
          usdExchangeRate: 7,
        }}
      />
    )
    expect(screen.getByText('USD / 1M tokens')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.queryByText(/CNY/)).not.toBeInTheDocument()
  })

  it('labels recharge prices USD on a TOKENS site', () => {
    useSite({ quotaDisplayType: 'TOKENS' })
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('USD / 1M tokens')).toBeInTheDocument()
  })
})

describe('recharge price in the tiered pricing breakdown', () => {
  it('shows tier token prices in USD at the recharge price on a CNY site', () => {
    useSite({ quotaDisplayType: 'CNY', usdExchangeRate: 7 })
    render(
      <DynamicPricingBreakdown
        billingExpr={tieredModel.billing_expr}
        taskPriceOptions={{
          showRechargePrice: true,
          priceRate: PRICE_RATE,
          usdExchangeRate: 7,
        }}
      />
    )
    expect(screen.getAllByText('$3.0000').length).toBeGreaterThan(0)
    expect(screen.queryByText(/¥/)).not.toBeInTheDocument()
  })

  it('keeps tier token prices in USD when the recharge switch is off', () => {
    render(
      <DynamicPricingBreakdown
        billingExpr={tieredModel.billing_expr}
        taskPriceOptions={{
          showRechargePrice: false,
          priceRate: PRICE_RATE,
          usdExchangeRate: 1,
        }}
      />
    )
    expect(screen.getAllByText('$2.0000').length).toBeGreaterThan(0)
    expect(screen.queryByText('$3.0000')).not.toBeInTheDocument()
  })
})
