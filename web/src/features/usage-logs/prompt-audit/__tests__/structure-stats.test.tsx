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
import type { ReactElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { PromptAuditEvents } from '..'
import type {
  PromptAuditStructureFingerprint,
  PromptAuditStructureSignalDay,
} from '../api'
import { PromptAuditStructureStats } from '../components/prompt-audit-structure-stats'
import {
  formatStructureSignalRate,
  formatStructureStatDay,
  formatStructureUserCount,
  toStructureStatDay,
} from '../lib/structure-stats'

const FINGERPRINTS_URL = '/api/prompt_audit/structure/fingerprints'
const SIGNALS_URL = '/api/prompt_audit/structure/signals'

function fingerprint(
  patch: Partial<PromptAuditStructureFingerprint>
): PromptAuditStructureFingerprint {
  return {
    fingerprint: `abcdef012345${'0'.repeat(52)}`,
    prefix_hash: `12345678${'f'.repeat(56)}`,
    first_seen_at: 1788600000,
    last_seen_at: 1788686400,
    request_count: 1200,
    user_count: 7,
    user_count_capped: false,
    system_length: 2048,
    last_request_format: 'openai_chat',
    last_client_class: 'sdk',
    ...patch,
  }
}

function signalDay(
  patch: Partial<PromptAuditStructureSignalDay>
): PromptAuditStructureSignalDay {
  return {
    stat_day: 20261113,
    requests: 200,
    with_system: 0,
    with_developer: 0,
    with_history_user: 0,
    with_history_assistant: 0,
    trailing_assistant: 0,
    with_agent_context: 0,
    with_tool: 0,
    assistant_turns: 0,
    user_turns: 0,
    assistant_turns_10_plus: 0,
    mid_system: 0,
    control_tokens: 0,
    role_markers: 0,
    invisible_requests: 0,
    invisible_chars: 0,
    encoded_requests: 0,
    encoded_runs: 0,
    system_under_1k: 0,
    system_under_4k: 0,
    system_under_16k: 0,
    system_under_64k: 0,
    system_64k_plus: 0,
    ...patch,
  }
}

type RequestParams = Record<string, unknown>

function mockStructureApi(data: {
  fingerprints?: PromptAuditStructureFingerprint[]
  days?: PromptAuditStructureSignalDay[]
}) {
  const fingerprints = data.fingerprints ?? []
  return vi.spyOn(api, 'get').mockImplementation(async (url: string) => ({
    data: {
      success: true,
      data:
        url === FINGERPRINTS_URL
          ? {
              page: 1,
              page_size: 20,
              total: fingerprints.length,
              items: fingerprints,
            }
          : {
              items: data.days ?? [],
              request_formats: ['openai_chat', 'anthropic_messages'],
              client_classes: ['claude_code', 'sdk'],
            },
    },
  }))
}

function requestParams(
  get: ReturnType<typeof mockStructureApi>,
  url: string
): RequestParams[] {
  return get.mock.calls
    .filter((call) => call[0] === url)
    .map((call) => (call[1] as { params: RequestParams }).params)
}

function renderWithQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('structure statistics formatting', () => {
  it('shows a counter with its share of the requests of the day', () => {
    expect(formatStructureSignalRate(25, 200, 'en')).toBe('25 (12.5%)')
    expect(formatStructureSignalRate(1, 3, 'en')).toBe('1 (33.3%)')
    expect(formatStructureSignalRate(200, 200, 'en')).toBe('200 (100%)')
  })

  it('shows no rate for a day without requests', () => {
    expect(formatStructureSignalRate(0, 0, 'en')).toBe('0 (-)')
  })

  it('marks a capped distinct user count as a lower bound', () => {
    expect(formatStructureUserCount(50, true, 'en')).toBe('≥50')
    expect(formatStructureUserCount(50, false, 'en')).toBe('50')
  })

  it('converts a local date to a stat day and shows a stat day as a date', () => {
    expect(toStructureStatDay(new Date(2026, 10, 3))).toBe(20261103)
    expect(toStructureStatDay(new Date(2026, 0, 31))).toBe(20260131)
    expect(formatStructureStatDay(20261103)).toBe('2026-11-03')
    expect(formatStructureStatDay(0)).toBe('0')
  })
})

describe('fingerprint ranking', () => {
  it('shows a shortened fingerprint and a capped user count as a lower bound', async () => {
    mockStructureApi({
      fingerprints: [fingerprint({ user_count: 50, user_count_capped: true })],
    })
    renderWithQueries(<PromptAuditStructureStats />)

    const row = (await screen.findByText('abcdef012345')).closest('tr')
    expect(row).not.toBeNull()
    const cells = within(row as HTMLElement)
    expect(cells.getByText('12345678')).toBeInTheDocument()
    expect(cells.getByText('≥50')).toBeInTheDocument()
    expect(cells.getByText('1,200')).toBeInTheDocument()
    expect(cells.getByText('OpenAI Chat')).toBeInTheDocument()
    expect(
      cells.getByRole('button', { name: 'Copy Fingerprint' })
    ).toBeInTheDocument()
  })

  it('labels requests without a system prompt instead of showing a hash', async () => {
    mockStructureApi({
      fingerprints: [fingerprint({ fingerprint: 'none', prefix_hash: 'none' })],
    })
    renderWithQueries(<PromptAuditStructureStats />)

    const row = (await screen.findByText('No system prompt')).closest('tr')
    expect(
      within(row as HTMLElement).queryByRole('button', {
        name: 'Copy Fingerprint',
      })
    ).not.toBeInTheDocument()
  })

  it('requests the ranking by distinct users first and by requests after the toggle', async () => {
    const get = mockStructureApi({ fingerprints: [fingerprint({})] })
    renderWithQueries(<PromptAuditStructureStats />)
    await screen.findByText('abcdef012345')
    expect(requestParams(get, FINGERPRINTS_URL)).toEqual([
      { p: 1, page_size: 20, sort: 'users' },
    ])

    await userEvent.click(screen.getByRole('button', { name: 'By requests' }))

    await vi.waitFor(() =>
      expect(requestParams(get, FINGERPRINTS_URL).at(-1)).toEqual({
        p: 1,
        page_size: 20,
        sort: 'requests',
      })
    )
    expect(screen.getByRole('button', { name: 'By requests' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })

  it('shows the empty state when nothing was measured', async () => {
    mockStructureApi({})
    renderWithQueries(<PromptAuditStructureStats />)

    expect(
      await screen.findByText('No structure statistics yet')
    ).toBeInTheDocument()
    expect(
      await screen.findByText('No structure statistics in this range')
    ).toBeInTheDocument()
  })

  it('offers a retry for each table when loading fails', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'unavailable' },
    })
    renderWithQueries(<PromptAuditStructureStats />)

    await vi.waitFor(() =>
      expect(
        screen.getAllByText('Failed to load structure statistics')
      ).toHaveLength(2)
    )
    expect(screen.getAllByRole('button', { name: 'Retry' })).toHaveLength(2)
  })
})

describe('signals by day', () => {
  it('shows each signal as a count with its rate', async () => {
    mockStructureApi({
      days: [signalDay({ with_system: 150, trailing_assistant: 25 })],
    })
    renderWithQueries(<PromptAuditStructureStats />)

    const row = (await screen.findByText('2026-11-13')).closest('tr')
    const cells = within(row as HTMLElement)
    expect(cells.getByText('150 (75%)')).toBeInTheDocument()
    expect(cells.getByText('25 (12.5%)')).toBeInTheDocument()
  })

  it('requests the last 30 days without format or client filters by default', async () => {
    const get = mockStructureApi({ days: [signalDay({})] })
    renderWithQueries(<PromptAuditStructureStats />)
    await screen.findByText('2026-11-13')

    const today = new Date()
    const start = new Date(today)
    start.setDate(today.getDate() - 29)
    expect(requestParams(get, SIGNALS_URL)).toEqual([
      {
        start_day: toStructureStatDay(start),
        end_day: toStructureStatDay(today),
        request_format: undefined,
        client_class: undefined,
      },
    ])
  })

  it('passes the chosen request format and client class to the request', async () => {
    const get = mockStructureApi({ days: [signalDay({})] })
    const user = userEvent.setup()
    renderWithQueries(<PromptAuditStructureStats />)
    await screen.findByText('2026-11-13')

    await user.click(screen.getByRole('combobox', { name: 'Request format' }))
    await user.click(screen.getByRole('option', { name: 'Anthropic Messages' }))
    await vi.waitFor(() =>
      expect(requestParams(get, SIGNALS_URL).at(-1)).toMatchObject({
        request_format: 'anthropic_messages',
        client_class: undefined,
      })
    )

    await user.click(screen.getByRole('combobox', { name: 'Client' }))
    await user.click(screen.getByRole('option', { name: 'Claude Code' }))
    await vi.waitFor(() =>
      expect(requestParams(get, SIGNALS_URL).at(-1)).toMatchObject({
        request_format: 'anthropic_messages',
        client_class: 'claude_code',
      })
    )
  })
})

describe('prompt audit page', () => {
  it('opens the structure statistics from the view tabs', async () => {
    mockStructureApi({ fingerprints: [fingerprint({})] })
    renderWithQueries(<PromptAuditEvents />)
    expect(screen.getByRole('tab', { name: 'Events' })).toHaveAttribute(
      'aria-selected',
      'true'
    )

    await userEvent.click(
      screen.getByRole('tab', { name: 'Structure statistics' })
    )

    expect(
      screen.getByRole('heading', { name: 'System prompt fingerprints' })
    ).toBeInTheDocument()
    expect(await screen.findByText('abcdef012345')).toBeInTheDocument()
  })
})
