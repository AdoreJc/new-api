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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemInfoSection } from '../system-info-section'

function Fixture(props: { userAgreementTranslations: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <SystemInfoSection
          defaultValues={{
            SystemName: 'New API',
            ServerAddress: '',
            TaskPublicAddress: '',
            Logo: '',
            Footer: '',
            About: 'Default about',
            HomePageContent: '',
            general_setting: { docs_link: '' },
            legal: { user_agreement: 'Default terms', privacy_policy: '' },
            site_content_i18n: {
              footer: '{}',
              about: '{}',
              home_page_content: '{}',
              user_agreement: props.userAgreementTranslations,
              privacy_policy: '',
            },
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

async function renderSystemInfo(userAgreementTranslations = '{}') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <Fixture userAgreementTranslations={userAgreementTranslations} />
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('textbox', { name: 'About' })
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('a translation typed in a language tab is saved as that language only, leaving the default untouched', async () => {
  const user = userEvent.setup()
  const defaultAbout = await renderSystemInfo()
  expect(defaultAbout).toHaveValue('Default about')

  const aboutTabs = screen.getAllByRole('tablist', {
    name: 'Content language',
  })[1]
  await user.click(within(aboutTabs).getByRole('tab', { name: 'English' }))
  await user.type(
    screen.getByRole('textbox', { name: 'About (English)' }),
    'Hello'
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))

  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'site_content_i18n.about',
      value: '{"en":"Hello"}',
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('languages cleared or left blank are dropped from the saved translations', async () => {
  const user = userEvent.setup()
  await renderSystemInfo('{"ja":"利用規約","en":"Terms"}')
  const agreementTabs = screen.getAllByRole('tablist', {
    name: 'Content language',
  })[3]
  const tab = (name: string) =>
    within(agreementTabs).getByRole('tab', { name: new RegExp(`^${name}`) })
  expect(tab('English')).toHaveTextContent('Translated')
  expect(tab('Français')).not.toHaveTextContent('Translated')

  await user.click(tab('日本語'))
  await user.clear(
    screen.getByRole('textbox', { name: 'User Agreement (日本語)' })
  )
  await user.click(tab('Français'))
  await user.type(
    screen.getByRole('textbox', { name: 'User Agreement (Français)' }),
    '   '
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))

  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'site_content_i18n.user_agreement',
      value: '{"en":"Terms"}',
    })
  )
})
