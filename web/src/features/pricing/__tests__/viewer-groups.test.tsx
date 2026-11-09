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
import type { CellContext } from '@tanstack/react-table'
import { cleanup, render, renderHook, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ModelCard } from '../components/model-card'
import { ModelDetailsContent } from '../components/model-details'
import { ModelDetailsPerformance } from '../components/model-details-performance'
import { ModelPriceCell } from '../components/model-price-cell'
import { usePricingColumns } from '../components/pricing-columns'
import type { PricingModel } from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

// The backend lists only the groups the viewer can use. A Claude model served
// only in subscription dedicated groups reaches a guest with no groups at all.
const dedicatedOnlyModel: PricingModel = {
  id: 1,
  model_name: 'claude-opus-5-5',
  quota_type: 0,
  model_ratio: 2.5,
  completion_ratio: 5,
  enable_groups: [],
  group_ratio: { default: 1, vip: 0.5 },
}

const sharedModel: PricingModel = {
  ...dedicatedOnlyModel,
  id: 2,
  model_name: 'gpt-shared',
  enable_groups: ['vip'],
}

let client: QueryClient

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'USD' },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

function withQueryClient(children: ReactNode) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

describe('model card groups', () => {
  it('shows no group label and the base price when the viewer can use no group', () => {
    render(<ModelCard model={dedicatedOnlyModel} onClick={vi.fn()} />)

    expect(screen.queryByText('Groups')).not.toBeInTheDocument()
    expect(screen.queryByText(/^\+\d+$/)).not.toBeInTheDocument()
    expect(screen.getByText('$5')).toBeInTheDocument()
    expect(screen.getByText('$25')).toBeInTheDocument()
  })

  it('shows the visible group and its group price as before', () => {
    render(<ModelCard model={sharedModel} onClick={vi.fn()} />)

    expect(screen.getByText('Groups')).toBeInTheDocument()
    expect(screen.getByText('vip')).toBeInTheDocument()
    expect(screen.getByText('$2.5')).toBeInTheDocument()
  })

  it('shows the base price in RMB at the recharge price when the viewer can use no group', () => {
    render(
      <ModelCard
        model={dedicatedOnlyModel}
        onClick={vi.fn()}
        showRechargePrice
        priceRate={6.8}
      />
    )

    expect(screen.getByText('¥34')).toBeInTheDocument()
    expect(screen.queryByText(/NaN/)).not.toBeInTheDocument()
  })

  it('shows an unset price instead of the default ratio when billing has no price', () => {
    const unpriced = { ...dedicatedOnlyModel, price_not_configured: true }
    render(
      <>
        <ModelCard model={unpriced} onClick={vi.fn()} />
        <ModelPriceCell model={unpriced} />
      </>
    )

    expect(screen.getAllByText('Unset price')).toHaveLength(2)
    expect(screen.queryByText('$5')).not.toBeInTheDocument()
  })
})

describe('pricing table groups column', () => {
  function renderGroupsCell(model: PricingModel) {
    const { result } = renderHook(() => usePricingColumns())
    const column = result.current.find(
      (item) => 'accessorKey' in item && item.accessorKey === 'enable_groups'
    )
    const cell = column?.cell as (
      context: CellContext<PricingModel, unknown>
    ) => ReactNode
    return render(
      <div data-testid='groups-cell'>
        {cell({ row: { original: model } } as CellContext<
          PricingModel,
          unknown
        >)}
      </div>
    )
  }

  it('leaves the cell blank when the viewer can use no group', () => {
    renderGroupsCell(dedicatedOnlyModel)

    expect(screen.getByTestId('groups-cell')).toBeEmptyDOMElement()
  })

  it('lists the visible groups', () => {
    renderGroupsCell(sharedModel)

    expect(screen.getByTestId('groups-cell')).toHaveTextContent('vip')
  })
})

describe('model details groups', () => {
  function renderDetails(model: PricingModel) {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
    render(
      withQueryClient(
        <ModelDetailsContent
          model={model}
          groupRatio={{ default: 1, vip: 0.5 }}
          usableGroup={{
            default: { desc: 'Default', ratio: 1 },
            vip: { desc: 'VIP', ratio: 0.5 },
          }}
          endpointMap={{}}
          autoGroups={['default', 'vip']}
          priceRate={1}
          usdExchangeRate={1}
          tokenUnit='M'
        />
      )
    )
  }

  it('shows the base price and no group section when the viewer can use no group', () => {
    renderDetails(dedicatedOnlyModel)

    expect(screen.getByText('Base Price')).toBeInTheDocument()
    expect(screen.getAllByText(/^\$5(?![\d.])/).length).toBeGreaterThan(0)
    expect(screen.queryByText('Pricing by Group')).not.toBeInTheDocument()
    expect(screen.queryByText('Groups')).not.toBeInTheDocument()
    expect(screen.queryByText(/NaN/)).not.toBeInTheDocument()
  })

  it('prices the visible group in the group table', () => {
    renderDetails(sharedModel)

    expect(screen.getByText('Pricing by Group')).toBeInTheDocument()
    expect(screen.getAllByText(/^\$2\.5(?!\d)/).length).toBeGreaterThan(0)
  })
})

describe('model performance without visible groups', () => {
  it('shows the model summary when every sampled group is hidden from the viewer', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          model_name: 'claude-opus-5-5',
          summary: { avg_latency_ms: 1200, success_rate: 50, avg_tps: 30 },
          series: [],
          groups: [],
        },
      },
    })
    render(
      withQueryClient(<ModelDetailsPerformance model={dedicatedOnlyModel} />)
    )

    expect(await screen.findByText('50.00%')).toBeInTheDocument()
    expect(
      screen.queryByText(
        'Performance data is not yet available for this model.'
      )
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Per-group performance')).not.toBeInTheDocument()
  })
})
