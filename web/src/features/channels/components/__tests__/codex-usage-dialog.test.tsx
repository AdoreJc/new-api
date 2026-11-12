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
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { CodexUsageDialog } from '../dialogs/codex-usage-dialog'

const usageResponse = {
  success: true,
  message: '',
  upstream_status: 200,
  data: {
    plan_type: 'plus',
    rate_limit: {
      allowed: true,
      limit_reached: false,
      primary_window: { used_percent: 20, limit_window_seconds: 18000 },
    },
  },
}

describe('CodexUsageDialog usage-limit cooldown', () => {
  test('a channel skipped by routing shows the cooldown badge', () => {
    render(
      <CodexUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        response={{ ...usageResponse, cooldown_until: 1_800_000_000 }}
      />
    )

    expect(screen.getByText(/Usage-limit cooldown until/)).toBeVisible()
  })

  test('a channel without a cooldown shows no cooldown badge', () => {
    render(
      <CodexUsageDialog
        open
        onOpenChange={() => {}}
        channelId={7}
        response={usageResponse}
      />
    )

    expect(
      screen.queryByText(/Usage-limit cooldown until/)
    ).not.toBeInTheDocument()
  })
})
