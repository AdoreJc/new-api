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
import userEvent from '@testing-library/user-event'
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
  bound_group: 'claude-ch7',
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

const usageUrl = '/api/subscription/self/usage/42'
const creditsUrl = '/api/subscription/self/usage/42/reset-credits'
const resetUrl = '/api/subscription/self/usage/42/reset'
const staleText =
  'The last reset request did not complete. Refresh the reset credits before applying another reset.'

function usageResponse(provider: 'claude' | 'codex', canReset: boolean) {
  return {
    data: {
      success: true,
      message: '',
      upstream_status: 200,
      data: {
        subscription_id: 42,
        provider,
        channel_type: provider === 'claude' ? 64 : 57,
        windows: {},
        limit_reached: true,
        fetched_at: 1,
        can_reset: canReset,
      },
    },
  }
}

const claudeCredits = {
  data: {
    success: true,
    message: '',
    upstream_status: 200,
    data: {
      eligible: true,
      at_limit: true,
      available_count: 1,
      credits: [
        {
          label: 'Weekly reset',
          resets_left: 1,
          clears: ['five_hour'],
          percent_used: {},
          blocking: [],
          use_requires_limit: true,
          redeemable: true,
        },
      ],
    },
  },
}

const codexCredits = {
  data: {
    success: true,
    message: '',
    upstream_status: 200,
    data: {
      available_count: 1,
      total_earned_count: 2,
      credits: [{ status: 'available', title: 'Weekly reset' }],
    },
  },
}

function mockGet(provider: 'claude' | 'codex', canReset: boolean) {
  return vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url === usageUrl) return usageResponse(provider, canReset)
    if (url === creditsUrl)
      return provider === 'claude' ? claudeCredits : codexCredits
    throw new Error(`unexpected GET ${url}`)
  })
}

function applyResetButton() {
  // The panel button; the confirm dialog's button only exists while open.
  return screen.getAllByRole('button', { name: /Apply reset|Resetting/ })[0]
}

async function openCreditsAndConfirmReset(
  user: ReturnType<typeof userEvent.setup>
) {
  await user.click(await screen.findByRole('button', { name: /Reset Credits/ }))
  await waitFor(() => expect(applyResetButton()).toBeEnabled())
  await user.click(applyResetButton())
  const confirmButtons = await screen.findAllByRole('button', {
    name: 'Apply reset',
  })
  await user.click(confirmButtons.at(-1) as HTMLElement)
}

describe('SubscriptionUsageDialog reset credits', () => {
  test('with the switch off there is no reset section and no reset-credit request', async () => {
    const get = mockGet('claude', false)

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={subscription}
      />
    )

    expect(await screen.findByText('Limited')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Reset Credits/ })
    ).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledExactlyOnceWith(usageUrl)
  })

  test('with the switch on the button stays disabled while the reset runs, then reports success', async () => {
    const get = mockGet('claude', true)
    let finishReset: (value: unknown) => void = () => {}
    const post = vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((resolve) => {
          finishReset = resolve
        })
    )
    const user = userEvent.setup()

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={subscription}
      />
    )
    await openCreditsAndConfirmReset(user)

    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        resetUrl,
        {},
        expect.anything()
      )
    )
    expect(applyResetButton()).toBeDisabled()
    expect(applyResetButton()).toHaveTextContent('Resetting...')

    finishReset({
      data: {
        success: true,
        message: '',
        upstream_status: 200,
        data: { outcome: 'reset', cleared: ['five_hour'] },
      },
    })
    expect(await screen.findByText('Reset completed')).toBeVisible()
    await waitFor(() =>
      expect(get.mock.calls.filter(([url]) => url === usageUrl)).toHaveLength(2)
    )
    expect(get.mock.calls.filter(([url]) => url === creditsUrl)).toHaveLength(2)
  })

  test('a failed Claude reset asks for a refresh before another reset', async () => {
    mockGet('claude', true)
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: false,
        message:
          'The reset request was interrupted and its result is unknown. Query the reset credits again before redeeming another credit',
      },
    })
    const user = userEvent.setup()

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={subscription}
      />
    )
    await openCreditsAndConfirmReset(user)

    expect(await screen.findByText(staleText)).toBeVisible()
    expect(applyResetButton()).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Refresh details' }))
    await waitFor(() => expect(applyResetButton()).toBeEnabled())
    expect(screen.queryByText(staleText)).not.toBeInTheDocument()
  })

  test('a failed Codex reset also asks for a refresh before another reset', async () => {
    mockGet('codex', true)
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: false,
        message: 'Failed to reset usage. Please try again later',
      },
    })
    const user = userEvent.setup()

    render(
      <SubscriptionUsageDialog
        open
        onOpenChange={() => {}}
        subscription={{ ...subscription, bound_channel_type: 57 }}
      />
    )
    await openCreditsAndConfirmReset(user)

    expect(await screen.findByText(staleText)).toBeVisible()
    expect(applyResetButton()).toBeDisabled()
  })
})
