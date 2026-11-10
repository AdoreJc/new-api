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
import { act, renderHook, waitFor } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { useHomePageContent } from '../hooks'

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(api, 'get').mockImplementation((async (
    _url: string,
    config?: { params?: { lang?: string } }
  ) => ({
    data: { success: true, data: `Home in ${config?.params?.lang}` },
  })) as unknown as typeof api.get)
})

afterEach(async () => {
  vi.restoreAllMocks()
  localStorage.clear()
  await act(() => i18next.changeLanguage('en'))
})

test('home page content follows the interface language and is cached per language', async () => {
  const { result } = renderHook(() => useHomePageContent())
  await waitFor(() => expect(result.current.content).toBe('Home in en'))

  if (!i18next.hasResourceBundle('zhCN', 'translation')) {
    i18next.addResourceBundle('zhCN', 'translation', {})
  }
  await act(() => i18next.changeLanguage('zhCN'))

  await waitFor(() => expect(result.current.content).toBe('Home in zh-CN'))
  expect(api.get).toHaveBeenLastCalledWith(
    '/api/home_page_content',
    expect.objectContaining({ params: { lang: 'zh-CN' } })
  )
  expect(localStorage.getItem('home_page_content:en')).toBe('Home in en')
  expect(localStorage.getItem('home_page_content:zh-CN')).toBe('Home in zh-CN')
})
