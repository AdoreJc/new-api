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

import { PrivacyPolicy } from '../privacy-policy'
import { UserAgreement } from '../user-agreement'

vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => (
    <main>{props.children}</main>
  ),
}))

async function setInterfaceLanguage(language: string) {
  if (!i18next.hasResourceBundle(language, 'translation')) {
    i18next.addResourceBundle(language, 'translation', {})
  }
  await act(() => i18next.changeLanguage(language))
}

function renderWithQuery(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{node}</QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'get').mockImplementation((async (
    url: string,
    config?: { params?: { lang?: string } }
  ) => ({
    data: { success: true, data: `${url} in ${config?.params?.lang}` },
  })) as unknown as typeof api.get)
})

afterEach(async () => {
  vi.restoreAllMocks()
  await act(() => i18next.changeLanguage('en'))
})

test.each([
  ['user agreement', UserAgreement, '/api/user-agreement'],
  ['privacy policy', PrivacyPolicy, '/api/privacy-policy'],
])(
  'the %s is requested in the interface language and refetched after switching',
  async (_name, Page, url) => {
    renderWithQuery(<Page />)
    expect(await screen.findByText(`${url} in en`)).toBeInTheDocument()

    await setInterfaceLanguage('zhTW')

    expect(await screen.findByText(`${url} in zh-TW`)).toBeInTheDocument()
    expect(api.get).toHaveBeenCalledWith(
      url,
      expect.objectContaining({ params: { lang: 'zh-TW' } })
    )
  }
)
