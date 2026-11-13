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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { ClaudeUsageDialog } from '../dialogs/claude-usage-dialog'

const usageResponse = {
  success: true,
  message: '',
  upstream_status: 200,
  data: {
    five_hour: { utilization: 12, resets_at: '2026-11-06T03:00:00Z' },
    seven_day: { utilization: 40, resets_at: '2026-11-10T00:00:00Z' },
  },
}

const creditsResponse = {
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
          starts_at: '2026-11-01T00:00:00Z',
          expires_at: '2026-11-30T00:00:00Z',
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

function applyResetButton() {
  // The panel button; the confirm dialog's button only exists while open.
  return screen.getAllByRole('button', { name: 'Apply reset' })[0]
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ClaudeUsageDialog usage windows', () => {
  test('a window filled from sampled headers shows the Fable label, the source badge and the sample time', () => {
    render(
      <ClaudeUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        response={{
          ...usageResponse,
          data: {
            ...usageResponse.data,
            seven_day_overage_included: {
              utilization: 37.5,
              resets_at: '2026-11-09T00:00:00Z',
              source: 'passive',
              sampled_at: '2026-11-05T12:00:00Z',
            },
          },
        }}
      />
    )

    expect(screen.getByText('7-Day Fable Window')).toBeVisible()
    expect(screen.queryByText('7-Day Overage Window')).not.toBeInTheDocument()
    expect(screen.getByText('From response headers')).toBeVisible()
    expect(screen.getByText('Sampled at:')).toBeVisible()
    expect(
      screen.getByRole('progressbar', {
        name: '7-Day Fable Window usage: 37.5%',
      })
    ).toBeInTheDocument()
  })

  test('windows reported by the usage API carry no source badge', () => {
    render(
      <ClaudeUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        response={{
          ...usageResponse,
          data: {
            ...usageResponse.data,
            seven_day_overage_included: { utilization: 37.5 },
          },
        }}
      />
    )

    expect(screen.getByText('7-Day Fable Window')).toBeVisible()
    expect(screen.queryByText('From response headers')).not.toBeInTheDocument()
    expect(screen.queryByText('Sampled at:')).not.toBeInTheDocument()
  })
})

describe('ClaudeUsageDialog reset credits', () => {
  test('a failed reset locks "Apply reset" until the credits are queried again', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(creditsResponse)
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: false,
        message:
          'The reset request was interrupted and its result is unknown. Query the reset credits again before redeeming another credit',
      },
    })
    const user = userEvent.setup()
    render(
      <ClaudeUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        channelName='claude-sub'
        response={usageResponse}
      />
    )

    await user.click(screen.getByRole('button', { name: /Reset Credits/ }))
    await waitFor(() => expect(applyResetButton()).toBeEnabled())
    expect(get).toHaveBeenCalledExactlyOnceWith(
      '/api/channel/7/claude/usage/reset-credits',
      expect.anything()
    )

    await user.click(applyResetButton())
    const confirmButtons = await screen.findAllByRole('button', {
      name: 'Apply reset',
    })
    await user.click(confirmButtons.at(-1) as HTMLElement)

    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/api/channel/7/claude/usage/reset',
        {},
        expect.anything()
      )
    )
    await waitFor(() => expect(applyResetButton()).toBeDisabled())
    expect(
      screen.getByText(
        'The last reset request did not complete. Refresh the reset credits before applying another reset.'
      )
    ).toBeVisible()
    expect(
      screen.getByText(
        'The reset request was interrupted and its result is unknown. Query the reset credits again before redeeming another credit'
      )
    ).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Refresh details' }))
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(applyResetButton()).toBeEnabled())
    expect(
      screen.queryByText(
        'The last reset request did not complete. Refresh the reset credits before applying another reset.'
      )
    ).not.toBeInTheDocument()
  })

  test('a failed credit re-query keeps "Apply reset" locked', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce(creditsResponse)
      .mockResolvedValueOnce({
        data: { success: false, message: 'Upstream returned status 503' },
      })
    vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: false, message: 'Failed to reset usage' },
    })
    const user = userEvent.setup()
    render(
      <ClaudeUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        response={usageResponse}
      />
    )

    await user.click(screen.getByRole('button', { name: /Reset Credits/ }))
    await waitFor(() => expect(applyResetButton()).toBeEnabled())
    await user.click(applyResetButton())
    const confirmButtons = await screen.findAllByRole('button', {
      name: 'Apply reset',
    })
    await user.click(confirmButtons.at(-1) as HTMLElement)
    await waitFor(() => expect(applyResetButton()).toBeDisabled())

    await user.click(screen.getByRole('button', { name: 'Refresh details' }))
    await waitFor(() =>
      expect(screen.getByText('Upstream returned status 503')).toBeVisible()
    )
    expect(applyResetButton()).toBeDisabled()
  })
})

describe('ClaudeUsageDialog automatic reset', () => {
  const view = {
    config: {
      enabled: false,
      rule: { type: 'five_hour_used_pct', threshold: 90 },
      min_interval_minutes: 60,
      max_per_day: 2,
      max_per_week: 5,
      skip_if_reset_within_minutes: 15,
      alarm_ignores_reset_soon: true,
      expiring_ignores_reset_soon: true,
    },
    state: {},
    alarm_grace_minutes: 10,
    weekly_windows: [
      'seven_day',
      'seven_day_sonnet',
      'seven_day_overage_included',
    ],
  }

  test('a trial run shows the decision and neither saves nor redeems', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: view },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          channel_id: 7,
          evaluated_at: 1_800_000_000,
          dry_run: true,
          matched: true,
          hits: [{ type: 'five_hour_used_pct', threshold: 90, value: 95 }],
          reason: 'would_redeem',
          credits: { available: 1 },
        },
      },
    })
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ClaudeUsageDialog
          open
          onOpenChange={() => {}}
          channelId={7}
          response={usageResponse}
        />
      </QueryClientProvider>
    )

    await user.click(screen.getByRole('button', { name: 'Automatic reset' }))
    expect(
      await screen.findByRole('switch', {
        name: /Use reset credits automatically/,
      })
    ).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Trial run' }))

    expect(
      await screen.findByText('A credit would be redeemed now')
    ).toBeVisible()
    expect(screen.getByText('5-hour usage 95% (threshold 90%)')).toBeVisible()
    expect(post).toHaveBeenCalledTimes(1)
    expect(post.mock.calls[0][0]).toBe(
      '/api/channel/7/auto-reset-credit/dry-run'
    )
    expect(put).not.toHaveBeenCalled()
  })
})
