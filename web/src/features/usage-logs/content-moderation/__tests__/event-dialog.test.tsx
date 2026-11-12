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
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { parseScores, type ContentModerationEvent } from '../api'
import { ContentModerationEventDialog } from '../components/content-moderation-event-dialog'

const event: ContentModerationEvent = {
  id: 7,
  created_at: 1788600600,
  request_id: 'req-7',
  user_id: 3,
  username: 'alice',
  token_id: 9,
  token_name: 'main key',
  using_group: 'default',
  channel_id: 4,
  relay_format: 'openai',
  endpoint: '/v1/chat/completions',
  model_name: 'gpt-x',
  mode: 'pre_block',
  action: 'block',
  flagged: true,
  allowlisted: false,
  highest_category: 'violence',
  highest_score: 0.99,
  category_scores: '{"violence":0.99,"hate":0.1}',
  thresholds: '{"violence":0.95,"hate":0.65}',
  input_hash: 'f'.repeat(64),
  keyword_list: 'spam',
  input_excerpt: 'do [redacted] now',
  input_length: 20,
  image_count: 0,
  engine_model: 'omni-moderation-test',
  latency_ms: 80,
  queue_delay_ms: 0,
  error: '',
  violation_count: 2,
  auto_disabled: false,
  email_sent: false,
  config_version: 2,
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('orders scores from highest to lowest and ignores malformed values', () => {
  expect(parseScores(event.category_scores)).toEqual([
    ['violence', 0.99],
    ['hate', 0.1],
  ])
  expect(parseScores('not json')).toEqual([])
  expect(parseScores('{"hate":"high"}')).toEqual([])
})

it('adds the user to the allowlist from the event details', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: {} } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ContentModerationEventDialog event={event} />
    </QueryClientProvider>
  )
  await userEvent.click(screen.getByRole('button', { name: 'Details' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Content moderation event',
  })
  expect(within(dialog).getByText('do [redacted] now')).toBeVisible()
  const scores = within(dialog).getByRole('list', { name: 'Category scores' })
  expect(within(scores).getAllByRole('listitem')[0]).toHaveTextContent(
    'Violence: 0.990 / 0.95'
  )

  const add = within(dialog).getByRole('button', {
    name: 'Add user to allowlist',
  })
  await userEvent.click(add)
  expect(post).toHaveBeenCalledWith('/api/content_moderation/allowlist', {
    user_id: 3,
    action: 'add',
  })
  await vi.waitFor(() => expect(add).toBeDisabled())
})
