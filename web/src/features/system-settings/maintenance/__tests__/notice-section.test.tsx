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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { NoticeSection } from '../notice-section'

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <NoticeSection
          defaultValue='Default notice'
          defaultTranslations='{"en":"English notice"}'
        />
      </SettingsPageProvider>
    </>
  )
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('saving a Traditional Chinese notice submits only the translations option', async () => {
  const user = userEvent.setup()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  expect(
    await screen.findByRole('textbox', { name: 'Announcement content' })
  ).toHaveValue('Default notice')

  await user.click(screen.getByRole('tab', { name: '繁體中文' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Announcement content (繁體中文)' }),
    '維護公告'
  )
  await user.click(screen.getByRole('button', { name: 'Save notice' }))

  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'site_content_i18n.notice',
      value: '{"en":"English notice","zh-TW":"維護公告"}',
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})
