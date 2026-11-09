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

// Production settings: the site settles in USD (exchange rate 1) and the
// recharge channel sells $1 of credit for ¥6.8.
const PRICE_RATE = 6.8
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

  it('shows token, cached and group token prices as RMB at the recharge price', () => {
    expect(formatPrice(tokenModel, 'input', 'M', true, PRICE_RATE, 7)).toBe(
      '¥13.6'
    )
    expect(formatPrice(tokenModel, 'output', 'M', true, PRICE_RATE, 7)).toBe(
      '¥40.8'
    )
    expect(formatPrice(tokenModel, 'cache', 'M', true, PRICE_RATE, 7)).toBe(
      '¥1.36'
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
    ).toBe('¥27.2')
  })

  it('shows per-request and group per-request prices as RMB at the recharge price', () => {
    expect(formatRequestPrice(requestModel, true, PRICE_RATE, 7)).toBe('¥3.4')
    expect(
      formatFixedPrice(requestModel, 'vip', true, PRICE_RATE, 7, groupRatio)
    ).toBe('¥6.8')
  })

  it('shows tiered expression and task usage prices as RMB at the recharge price', () => {
    const tiered = getDynamicPricingSummary(tieredModel, recharge)
    expect(tiered?.primaryEntries.map((entry) => entry.formatted)).toEqual([
      '¥13.6',
      '¥40.8',
    ])
    const task = getDynamicPricingSummary(taskModel, recharge)
    expect(task?.primaryEntries[0]?.formatted).toBe('¥3.4')
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

describe('recharge price currency label', () => {
  it('labels token prices CNY per 1M tokens in recharge mode on a USD site', () => {
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('CNY / 1M tokens')).toBeInTheDocument()
    expect(screen.getByText('13.6')).toBeInTheDocument()
  })

  it('labels per-request prices CNY per request in recharge mode on a USD site', () => {
    render(
      <ModelPriceCell
        model={requestModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('CNY / request')).toBeInTheDocument()
    expect(screen.getByText('3.4')).toBeInTheDocument()
  })

  it('keeps the USD label when the recharge switch is off', () => {
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: false, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('USD / 1M tokens')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
  })

  it('labels recharge prices CNY on a TOKENS site', () => {
    useSite({ quotaDisplayType: 'TOKENS' })
    render(
      <ModelPriceCell
        model={tokenModel}
        options={{ showRechargePrice: true, priceRate: PRICE_RATE }}
      />
    )
    expect(screen.getByText('CNY / 1M tokens')).toBeInTheDocument()
  })
})

describe('recharge price in the tiered pricing breakdown', () => {
  it('shows tier token prices as RMB at the recharge price on a USD site', () => {
    render(
      <DynamicPricingBreakdown
        billingExpr={tieredModel.billing_expr}
        taskPriceOptions={{
          showRechargePrice: true,
          priceRate: PRICE_RATE,
          usdExchangeRate: 1,
        }}
      />
    )
    expect(screen.getAllByText('¥13.6000').length).toBeGreaterThan(0)
    expect(screen.queryByText('$2.0000')).not.toBeInTheDocument()
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
  })
})
