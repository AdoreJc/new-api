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

import { PromptAuditEvents } from '..'
import type { PromptAuditEvent } from '../api'

function auditEvent(id: number, username: string): PromptAuditEvent {
  return {
    id,
    created_at: 1788600600 + id,
    request_id: `req-${id}`,
    user_id: id,
    username,
    token_id: 1,
    token_name: 'key',
    using_group: 'default',
    channel_id: 1,
    relay_format: 'openai',
    endpoint: '/v1/chat/completions',
    model_name: 'gpt-4o',
    mode: 'async_audit',
    decision: 'flag',
    risk_level: 'flag',
    action: 'Warn',
    safety: 'controversial',
    categories: '[]',
    matched_scanners: '[]',
    unknown_categories: '[]',
    guard_endpoint_id: 'g1',
    scanner_version: 'guard',
    config_version: 1,
    chunk_total: 1,
    chunks_scanned: 1,
    scan_incomplete: false,
    latency_ms: 10,
    prompt_hash: 'a'.repeat(64),
    redacted_preview: 'preview',
    prompt_length: 7,
    message_count: 1,
  }
}

const preview = {
  matched_count: 3,
  snapshot_max_id: 9,
  filter_hash: 'hash-9',
  confirmation_token: 'token-9',
  expires_at: 1788601000,
}

function renderPage(items: PromptAuditEvent[]) {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items, total: items.length } },
  })
  const post = vi.spyOn(api, 'post').mockImplementation((url: string) =>
    Promise.resolve({
      data: {
        success: true,
        data: url.endsWith('/delete-preview') ? preview : { deleted: 1 },
      },
    })
  )
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <PromptAuditEvents />
    </QueryClientProvider>
  )
  return { get, post }
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('filtered delete confirms the previewed count and filters, then deletes with the preview token', async () => {
  const { get, post } = renderPage([auditEvent(9, 'alice')])
  await screen.findByText('alice')
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search user, key, model or preview' }),
    'alice'
  )

  await userEvent.click(
    screen.getByRole('button', { name: 'Delete matching events' })
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('Delete 3 events that match these filters?')
  expect(within(dialog).getByLabelText('Deletion filters')).toHaveTextContent(
    'alice'
  )
  expect(post).toHaveBeenCalledWith('/api/prompt_audit/events/delete-preview', {
    keyword: 'alice',
  })
  const listsBefore = get.mock.calls.length

  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(post).toHaveBeenCalledWith(
    '/api/prompt_audit/events/delete-by-filter',
    {
      filter: { keyword: 'alice' },
      snapshot_max_id: 9,
      filter_hash: 'hash-9',
      confirmation_token: 'token-9',
    }
  )
  await vi.waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(get.mock.calls.length).toBeGreaterThan(listsBefore)
})

it('deleting one row asks for confirmation before calling the API', async () => {
  const deleteRequest = vi.spyOn(api, 'delete').mockResolvedValue({
    data: { success: true, data: { deleted: 1 } },
  })
  renderPage([auditEvent(9, 'alice')])
  await screen.findByText('alice')

  await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
  const dialog = await screen.findByRole('alertdialog')
  expect(deleteRequest).not.toHaveBeenCalled()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(deleteRequest).toHaveBeenCalledWith('/api/prompt_audit/events/9')
})

it('selected rows are deleted through the batch endpoint', async () => {
  const { post } = renderPage([auditEvent(9, 'alice'), auditEvent(8, 'bob')])
  await screen.findByText('alice')

  const rows = screen.getAllByRole('checkbox', { name: 'Select row' })
  await userEvent.click(rows[1])
  await userEvent.click(
    screen.getByRole('button', { name: 'Delete selected (1)' })
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('Delete 1 selected events?')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(post).toHaveBeenCalledWith('/api/prompt_audit/events/batch-delete', {
    ids: [8],
  })
})

it('a regex the browser cannot parse is still searched so the server decides', async () => {
  const { get } = renderPage([auditEvent(9, 'alice')])
  await screen.findByText('alice')
  await userEvent.click(screen.getByRole('switch', { name: 'Regex' }))
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search user, key, model or preview' }),
    '(?P<word>secret)'
  )

  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await vi.waitFor(() =>
    expect(get).toHaveBeenCalledWith('/api/prompt_audit/events', {
      params: expect.objectContaining({
        keyword: '(?P<word>secret)',
        keyword_mode: 'regex',
      }),
    })
  )
})

it('retrying a scan that keeps timing out before any row requests it again', async () => {
  const timedOut = {
    items: [],
    truncated: true,
    next_cursor: 0,
    scanned: 0,
    stop_reason: 'time_budget',
  }
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: timedOut },
  })
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <PromptAuditEvents />
    </QueryClientProvider>
  )
  await userEvent.click(screen.getByRole('switch', { name: 'Regex' }))
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search user, key, model or preview' }),
    'secret'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  const scanRequests = () =>
    get.mock.calls.filter(
      (call) =>
        (call[1] as { params?: { keyword?: string } } | undefined)?.params
          ?.keyword === 'secret'
    ).length
  await vi.waitFor(() => expect(scanRequests()).toBe(1))

  for (const expected of [2, 3]) {
    await userEvent.click(
      await screen.findByRole('button', { name: 'Continue search' })
    )
    await vi.waitFor(() => expect(scanRequests()).toBe(expected))
  }
})

it('select all on the page selects every row for batch deletion', async () => {
  renderPage([auditEvent(9, 'alice'), auditEvent(8, 'bob')])
  await screen.findByText('alice')

  await userEvent.click(screen.getByRole('checkbox', { name: 'Select all' }))
  expect(
    screen.getByRole('button', { name: 'Delete selected (2)' })
  ).toBeInTheDocument()
})
