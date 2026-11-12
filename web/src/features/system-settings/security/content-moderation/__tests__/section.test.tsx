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

import type { ContentModerationConfigResponse } from '../api'
import { ContentModerationSection } from '../content-moderation-section'

const savedConfig: ContentModerationConfigResponse = {
  config: {
    mode: 'observe',
    base_url: 'https://api.openai.com',
    model: 'omni-moderation-latest',
    timeout_ms: 3000,
    retry_count: 2,
    include_images: true,
    sample_rate: 100,
    all_groups: true,
    groups: [],
    model_filter: { type: 'all', models: [] },
    thresholds: { hate: 0.5 },
    worker_count: 4,
    queue_size: 1024,
    record_non_hits: false,
    hit_retention_days: 180,
    non_hit_retention_days: 3,
    hash_blocklist_enabled: false,
    email_on_hit: false,
    auto_disable_enabled: false,
    disable_threshold: 10,
    violation_window_hours: 720,
    keyword_mode: 'api_only',
    keyword_lists: [],
    allowlist_user_ids: [],
    config_version: 4,
    updated_at: 0,
    updated_by: 1,
  },
  keys: [],
  categories: ['hate'],
  default_thresholds: { hate: 0.65 },
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('shows the group input right below the all groups switch when it is turned off', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url.endsWith('/config')) {
      return { data: { success: true, data: savedConfig } }
    }
    if (url.endsWith('/keyword_lists')) {
      return { data: { success: true, data: [] } }
    }
    // The runtime card is not under test; let its status query fail.
    return { data: { success: false, message: 'unavailable' } }
  })
  const user = userEvent.setup()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ContentModerationSection />
    </QueryClientProvider>
  )
  const allGroups = await screen.findByRole('switch', {
    name: 'Moderate all groups',
  })
  expect(
    screen.queryByPlaceholderText('Group name, or a prefix ending in *')
  ).not.toBeInTheDocument()

  await user.click(allGroups)

  const groupsInput = screen.getByPlaceholderText(
    'Group name, or a prefix ending in *'
  )
  const nextSwitch = screen.getByRole('switch', {
    name: 'Remember flagged inputs',
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
