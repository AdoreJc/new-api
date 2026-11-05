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
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SubscriptionUsageDialog } from '../components/dialogs/subscription-usage-dialog'
import type { UserSubscription } from '../types'

const subscription: UserSubscription = {
  id: 42,
  user_id: 1,
  plan_id: 1,
  status: 'active',
  start_time: 1,
  end_time: 4102444800,
  amount_total: 0,
  amount_used: 0,
  bound_channel_id: 7,
  bound_group: 'oauth-ch7',
  bound_channel_type: 64,
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('SubscriptionUsageDialog', () => {
  test('opening the dialog loads the subscription usage and renders the windows', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        message: '',
        upstream_status: 200,
        data: {
          subscription_id: 42,
          provider: 'claude',
          channel_type: 64,
          windows: {
            five_hour: { utilization: 33, resets_at: '2026-11-05T10:00:00Z' },
            seven_day: { utilization: 70, resets_at: '2026-11-09T00:00:00Z' },
          },
          limit_reached: false,
          fetched_at: 1,
        },
      },
    })

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={subscription}
      />
    )

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/subscription/self/usage/42')
    )
    expect(await screen.findByText('5-Hour Window')).toBeInTheDocument()
    expect(screen.getByText('7-Day Window')).toBeInTheDocument()
    expect(screen.getByText('33%')).toBeInTheDocument()
    expect(screen.getByText('70%')).toBeInTheDocument()
    expect(screen.getByText('Available')).toBeInTheDocument()
    expect(screen.getByText('Claude Subscription (OAuth)')).toBeInTheDocument()
  })

  test('a failed upstream query shows the server message instead of windows', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: false,
        message: 'Upstream returned status 403',
        upstream_status: 403,
      },
    })

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={subscription}
      />
    )

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Upstream returned status 403'
    )
    expect(screen.queryByText('5-Hour Window')).not.toBeInTheDocument()
    expect(screen.getByText('HTTP 403')).toBeInTheDocument()
  })
})
