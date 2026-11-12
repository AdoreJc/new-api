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
import { useState, type ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { EmailSettingsSection } from '../email-settings-section'

type EmailValues = ComponentProps<typeof EmailSettingsSection>['defaultValues']
const defaults: EmailValues = {
  QuotaRemindEnabled: false,
  QuotaRemindThreshold: '0',
  EmailProvider: 'smtp',
  EmailOAuthProvider: 'google',
  EmailOAuthClientId: '',
  EmailOAuthClientSecret: '',
  EmailOAuthSecretConfigured: false,
  EmailOAuthTenant: 'common',
  EmailOAuthEmail: '',
  ResendApiKey: '',
  ResendKeyConfigured: false,
  ResendFrom: '',
  SMTPServer: 'smtp.example.com',
  SMTPPort: '587',
  SMTPAccount: 'sender@example.com',
  SMTPFrom: '',
  SMTPToken: '',
  SMTPSSLEnabled: false,
  SMTPStartTLSEnabled: true,
  SMTPInsecureSkipVerify: false,
  SMTPForceAuthLogin: false,
}
const savedApp: Partial<EmailValues> = {
  EmailOAuthClientId: 'client-id',
  EmailOAuthSecretConfigured: true,
}
const redirectURI = 'https://console.example/system-settings/operations/email'

function EmailFixture(props: { values: EmailValues }) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  return (
    <SettingsPageProvider actionsContainer={actions}>
      <div ref={setActions} />
      <EmailSettingsSection defaultValues={props.values} />
    </SettingsPageProvider>
  )
}

function renderEmail(values: Partial<EmailValues> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, {
    server_address: 'https://console.example/',
  })
  render(
    <QueryClientProvider client={client}>
      <EmailFixture values={{ ...defaults, ...values }} />
    </QueryClientProvider>
  )
  return userEvent.setup()
}

afterEach(() => {
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

describe('OAuth mailbox settings', () => {
  it('saves the OAuth app without selecting OAuth before a mailbox is authorized', async () => {
    const request = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = renderEmail()
    await user.click(screen.getByRole('radio', { name: 'OAuth mailbox' }))
    expect(screen.getByRole('textbox', { name: 'Redirect URI' })).toHaveValue(
      redirectURI
    )
    expect(
      screen.getByRole('button', { name: 'Authorize mailbox' })
    ).toBeDisabled()
    await user.click(
      screen.getByRole('radio', { name: 'Outlook / Microsoft 365' })
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Client ID' }),
      'client-id'
    )
    await user.type(screen.getByLabelText('Client secret'), 'client-secret')
    await user.clear(screen.getByRole('textbox', { name: 'Tenant' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Tenant' }),
      'contoso.onmicrosoft.com'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(4))
    expect(request.mock.calls.map((call) => call[1])).toEqual([
      { key: 'EmailOAuthProvider', value: 'microsoft' },
      { key: 'EmailOAuthClientId', value: 'client-id' },
      { key: 'EmailOAuthTenant', value: 'contoso.onmicrosoft.com' },
      { key: 'EmailOAuthClientSecret', value: 'client-secret' },
    ])
    await waitFor(() =>
      expect(screen.getByLabelText('Client secret')).toHaveValue('')
    )
  })

  it('requires a Client ID and secret before saving the OAuth method', async () => {
    const request = vi.spyOn(api, 'put')
    const user = renderEmail()
    await user.click(screen.getByRole('radio', { name: 'OAuth mailbox' }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(await screen.findByText('Enter the Client ID')).toBeVisible()
    expect(screen.getByText('Enter the client secret')).toBeVisible()
    expect(request).not.toHaveBeenCalled()
  })

  it('starts authorization with the saved app and opens the provider page', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: { authorization_url: 'https://accounts.example/consent' },
      },
    })
    const assign = vi.fn()
    vi.spyOn(window, 'location', 'get').mockReturnValue({
      ...window.location,
      assign,
    })
    const user = renderEmail({ ...savedApp, EmailProvider: 'oauth' })
    expect(screen.getByText('Not authorized')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Authorize mailbox' }))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://accounts.example/consent')
    )
    expect(post).toHaveBeenCalledWith('/api/option/email_oauth/authorize')
  })

  it('shows the authorized mailbox, locks the app and selects OAuth on save', async () => {
    const request = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = renderEmail({
      ...savedApp,
      EmailOAuthEmail: 'sender@example.com',
    })
    await user.click(screen.getByRole('radio', { name: 'OAuth mailbox' }))
    expect(screen.getByText('sender@example.com')).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'Client ID' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith('/api/option/', {
      key: 'EmailProvider',
      value: 'oauth',
    })
  })

  it('completes a provider redirect once and removes the code from the address', async () => {
    window.history.replaceState(
      null,
      '',
      '/system-settings/operations/email?state=one-time-state&code=auth-code&scope=x'
    )
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: true, data: { email: 'sender@example.com' } },
    })
    renderEmail(savedApp)
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/option/email_oauth/callback', {
      state: 'one-time-state',
      code: 'auth-code',
      error: '',
    })
    expect(window.location.search).toBe('')
    expect(screen.getByRole('radio', { name: 'OAuth mailbox' })).toBeChecked()
  })

  it('removes the authorization after confirmation', async () => {
    const remove = vi
      .spyOn(api, 'delete')
      .mockResolvedValue({ data: { success: true } })
    const user = renderEmail({
      ...savedApp,
      EmailProvider: 'smtp',
      EmailOAuthEmail: 'sender@example.com',
    })
    await user.click(screen.getByRole('radio', { name: 'OAuth mailbox' }))
    await user.click(
      screen.getByRole('button', { name: 'Remove authorization' })
    )
    expect(remove).not.toHaveBeenCalled()
    await user.click(await screen.findByRole('button', { name: 'Continue' }))
    await waitFor(() =>
      expect(remove).toHaveBeenCalledWith(
        '/api/option/email_oauth/authorization'
      )
    )
  })
})
