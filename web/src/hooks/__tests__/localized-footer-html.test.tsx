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
import { act, renderHook, waitFor } from '@testing-library/react'
import i18next from 'i18next'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { useLocalizedFooterHtml } from '../use-localized-footer-html'

function mockApi(footerI18nEnabled: boolean) {
  vi.spyOn(api, 'get').mockImplementation((async (
    url: string,
    config?: { params?: { lang?: string } }
  ) => {
    if (url === '/api/status') {
      return {
        data: {
          success: true,
          data: {
            footer_html: 'Default footer',
            footer_i18n_enabled: footerI18nEnabled,
          },
        },
      }
    }
    return {
      data: { success: true, data: `Footer in ${config?.params?.lang}` },
    }
  }) as unknown as typeof api.get)
}

function renderFooterHook() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return renderHook(() => useLocalizedFooterHtml('Default footer'), {
    wrapper: (props: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        {props.children}
      </QueryClientProvider>
    ),
  })
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(async () => {
  vi.restoreAllMocks()
  localStorage.clear()
  await act(() => i18next.changeLanguage('en'))
})

test('without footer translations the status footer is used and /api/footer is not requested', async () => {
  mockApi(false)
  const { result } = renderFooterHook()
  await waitFor(() => expect(api.get).toHaveBeenCalledWith('/api/status'))
  expect(result.current).toBe('Default footer')
  expect(api.get).not.toHaveBeenCalledWith('/api/footer', expect.anything())
})

test('with footer translations the footer follows the interface language after switching', async () => {
  mockApi(true)
  const { result } = renderFooterHook()
  await waitFor(() => expect(result.current).toBe('Footer in en'))

  if (!i18next.hasResourceBundle('ja', 'translation')) {
    i18next.addResourceBundle('ja', 'translation', {})
  }
  await act(() => i18next.changeLanguage('ja'))

  await waitFor(() => expect(result.current).toBe('Footer in ja'))
  expect(api.get).toHaveBeenCalledWith(
    '/api/footer',
    expect.objectContaining({ params: { lang: 'ja' } })
  )
})
