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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../../components/settings-page-context'
import type { AuditPolicyResponse } from '../api'
import { AuditPolicySection } from '../audit-policy-section'
import { createRule } from '../lib/rule-form'

afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

function policy(): AuditPolicyResponse {
  const defaultRule = { ...createRule([]), id: 'default' }
  return {
    config: {
      rules: [
        { ...createRule([]), id: 'first', name: 'First' },
        { ...createRule([]), id: 'second', name: 'Second' },
      ],
      default: defaultRule,
      allowlist: [],
      blocklist: [],
      config_version: 5,
      updated_at: 0,
      updated_by: 1,
    },
    saved: true,
    prompt_audit_enabled: true,
    prompt_audit_mode: 'blocking',
    content_moderation_mode: 'observe',
    prompt_audit_categories: ['jailbreak'],
    content_moderation_categories: ['harassment'],
    relay_formats: ['openai'],
    prompt_audit_scan_scopes: ['latest_user'],
    content_moderation_keyword_modes: ['api_only'],
  }
}

function renderSection() {
  const actions = document.createElement('div')
  document.body.appendChild(actions)
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SettingsPageProvider actionsContainer={actions}>
        <AuditPolicySection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

it('asks before the first save of derived rules', async () => {
  const derived = { ...policy(), saved: false }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: derived },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: policy() } })
  const user = userEvent.setup()
  renderSection()

  await user.click(
    await screen.findByRole('button', { name: 'Save audit policy' })
  )
  expect(put).not.toHaveBeenCalled()
  const dialog = await screen.findByRole('alertdialog')
  await user.click(
    within(dialog).getByRole('button', { name: 'Save audit policy' })
  )

  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
})

it('tests the rules being edited, with the requested group', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: policy() },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        rule_id: 'second',
        rule_name: 'Second',
        rule_index: 0,
        saved: true,
        allowlisted: false,
        verified: false,
        blocklisted: false,
        prompt_audit: 'off',
        prompt_audit_action: '',
        prompt_audit_fail_policy: '',
        content_moderation: 'off',
        content_moderation_action: '',
        content_moderation_fail_policy: '',
        notes: [],
      },
    },
  })
  const user = userEvent.setup()
  renderSection()

  await user.click(
    await screen.findByRole('button', { name: 'Move First down' })
  )
  await user.type(screen.getByLabelText('Requested group'), 'auto')
  await user.click(screen.getByRole('button', { name: 'Test' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  const request = post.mock.calls[0][1] as {
    requested_group: string
    config: AuditPolicyResponse['config']
  }
  expect(request.requested_group).toBe('auto')
  expect(request.config.rules.map((rule) => rule.id)).toEqual([
    'second',
    'first',
  ])
})

it('marks unreadable rules and does not save them', async () => {
  const degraded = policy()
  degraded.config.rules[0] = { ...degraded.config.rules[0], degraded: true }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...degraded,
        degraded: true,
        problems: ['rule first is degraded'],
      },
    },
  })
  const put = vi.spyOn(api, 'put')
  const user = userEvent.setup()
  renderSection()

  expect(await screen.findByText('Unreadable')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save audit policy' }))

  expect(put).not.toHaveBeenCalled()
})
