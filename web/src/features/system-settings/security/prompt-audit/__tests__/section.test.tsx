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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { PromptAuditSection } from '../prompt-audit-section'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function mockSavedConfig(config: { all_groups: boolean; groups: string[] }) {
  vi.spyOn(api, 'get').mockImplementation(async (url: string) => ({
    data: url.endsWith('/config')
      ? {
          success: true,
          data: {
            config: {
              enabled: true,
              mode: 'async_audit',
              all_groups: config.all_groups,
              groups: config.groups,
              scanners: ['jailbreak'],
              store_full_prompt: false,
              retention_days: 30,
              worker_count: 2,
              queue_capacity: 100,
              max_chunks_per_request: 8,
              endpoints: [],
              config_version: 3,
              updated_at: 0,
              updated_by: 1,
            },
            has_token: {},
            scanners: ['jailbreak', 'pii'],
          },
        }
      : { success: true, data: {} },
  }))
}

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <PromptAuditSection />
    </QueryClientProvider>
  )
}

it('shows the saved switch values once the settings load', async () => {
  mockSavedConfig({ all_groups: false, groups: ['claude-ch*'] })
  renderSection()

  const enabled = await screen.findByRole('switch', {
    name: 'Enable prompt audit',
  })
  expect(enabled).toHaveAttribute('aria-checked', 'true')
  expect(
    screen.getByRole('switch', { name: 'Audit all groups' })
  ).toHaveAttribute('aria-checked', 'false')
  expect(
    screen.getByRole('switch', {
      name: 'Store the full prompt of flagged requests',
    })
  ).toHaveAttribute('aria-checked', 'false')
  expect(screen.getByText('claude-ch*')).toBeVisible()
})

it('shows the group input right below the all groups switch when it is turned off', async () => {
  mockSavedConfig({ all_groups: true, groups: [] })
  const user = userEvent.setup()
  renderSection()
  const allGroups = await screen.findByRole('switch', {
    name: 'Audit all groups',
  })
  expect(
    screen.queryByPlaceholderText('Group name, or a prefix ending in *')
  ).not.toBeInTheDocument()

  await user.click(allGroups)

  const groupsInput = screen.getByPlaceholderText(
    'Group name, or a prefix ending in *'
  )
  const nextSwitch = screen.getByRole('switch', {
    name: 'Store the full prompt of flagged requests',
  })
  expect(
    allGroups.compareDocumentPosition(groupsInput) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(
    groupsInput.compareDocumentPosition(nextSwitch) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
})
