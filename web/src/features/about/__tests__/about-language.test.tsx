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
import { act, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { About } from '..'

vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => (
    <main>{props.children}</main>
  ),
}))

beforeEach(() => {
  vi.spyOn(api, 'get').mockImplementation((async (
    _url: string,
    config?: { params?: { lang?: string } }
  ) => ({
    data: { success: true, data: `About in ${config?.params?.lang}` },
  })) as unknown as typeof api.get)
})

afterEach(async () => {
  vi.restoreAllMocks()
  await act(() => i18next.changeLanguage('en'))
})

test('the about page shows the translation for the interface language after switching', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <About />
    </QueryClientProvider>
  )
  expect(await screen.findByText('About in en')).toBeInTheDocument()

  if (!i18next.hasResourceBundle('fr', 'translation')) {
    i18next.addResourceBundle('fr', 'translation', {})
  }
  await act(() => i18next.changeLanguage('fr'))

  expect(await screen.findByText('About in fr')).toBeInTheDocument()
  expect(api.get).toHaveBeenLastCalledWith(
    '/api/about',
    expect.objectContaining({ params: { lang: 'fr' } })
  )
})
