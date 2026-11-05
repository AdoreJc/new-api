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

import { OAuthAuthorizationDialog } from '../dialogs/oauth-authorization-dialog'

const startResponse = {
  data: {
    success: true,
    data: {
      session_id: 'sess-1',
      auth_url: 'https://claude.com/cai/oauth/authorize?state=abc',
      expires_in: 1800,
    },
  },
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('OAuthAuthorizationDialog', () => {
  test('generating a link shows it and enables pasting the code', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(startResponse)
    const user = userEvent.setup()
    render(
      <OAuthAuthorizationDialog
        open
        onOpenChange={() => {}}
        provider='claude'
        onComplete={() => {}}
      />
    )

    const codeInput = screen.getByRole('textbox', {
      name: /Step 2: Paste the result/,
    })
    expect(codeInput).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Complete authorization' })
    ).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Generate' }))

    await waitFor(() =>
      expect(
        screen.getByRole('textbox', {
          name: 'Step 1: Generate the authorization link',
        })
      ).toHaveValue('https://claude.com/cai/oauth/authorize?state=abc')
    )
    expect(post).toHaveBeenCalledExactlyOnceWith(
      '/api/channel/claude/oauth/start',
      {},
      expect.anything()
    )
    expect(
      screen.getByRole('button', { name: 'Open authorization link' })
    ).toHaveAttribute(
      'href',
      'https://claude.com/cai/oauth/authorize?state=abc'
    )
    expect(codeInput).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Regenerate' })).toBeVisible()
  })

  test('a successful exchange reports the key with the channel proxy and closes', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce(startResponse)
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { key: '{"access_token":"at"}', email: 'dev@example.com' },
        },
      })
    const onComplete = vi.fn()
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    render(
      <OAuthAuthorizationDialog
        open
        onOpenChange={onOpenChange}
        provider='codex'
        proxy='socks5://proxy.example:1080'
        onComplete={onComplete}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Generate' }))
    const codeInput = await screen.findByRole('textbox', {
      name: /Step 2: Paste the result/,
    })
    await waitFor(() => expect(codeInput).toBeEnabled())
    await user.type(
      codeInput,
      'http://localhost:1455/auth/callback?code=abc&state=xyz'
    )
    await user.click(
      screen.getByRole('button', { name: 'Complete authorization' })
    )

    await waitFor(() =>
      expect(onComplete).toHaveBeenCalledExactlyOnceWith({
        key: '{"access_token":"at"}',
        email: 'dev@example.com',
      })
    )
    expect(post).toHaveBeenLastCalledWith(
      '/api/channel/codex/oauth/exchange',
      {
        session_id: 'sess-1',
        code: 'http://localhost:1455/auth/callback?code=abc&state=xyz',
        proxy: 'socks5://proxy.example:1080',
      },
      expect.anything()
    )
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  test('a rejected exchange shows the server message and requires a new link', async () => {
    vi.spyOn(api, 'post')
      .mockResolvedValueOnce(startResponse)
      .mockResolvedValueOnce({
        data: {
          success: false,
          message:
            'The pasted state does not match this authorization session. Generate a new authorization link',
        },
      })
    const onComplete = vi.fn()
    const user = userEvent.setup()
    render(
      <OAuthAuthorizationDialog
        open
        onOpenChange={() => {}}
        provider='claude'
        onComplete={onComplete}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Generate' }))
    const codeInput = await screen.findByRole('textbox', {
      name: /Step 2: Paste the result/,
    })
    await waitFor(() => expect(codeInput).toBeEnabled())
    await user.type(codeInput, 'abc#wrong')
    await user.click(
      screen.getByRole('button', { name: 'Complete authorization' })
    )

    expect(
      await screen.findByText(
        'The pasted state does not match this authorization session. Generate a new authorization link'
      )
    ).toBeVisible()
    expect(onComplete).not.toHaveBeenCalled()
    expect(
      screen.getByRole('textbox', {
        name: 'Step 1: Generate the authorization link',
      })
    ).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Generate' })).toBeVisible()
  })
})
