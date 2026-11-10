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
import { useQuery } from '@tanstack/react-query'

import { getFooter } from '@/lib/api'

import { useContentLanguage } from './use-content-language'
import { useStatus } from './use-status'

/**
 * Footer HTML for the current interface language.
 *
 * `/api/status` ships the default footer as `footer_html`. Only when the
 * administrator has saved a per-language footer (`footer_i18n_enabled`) is
 * `/api/footer?lang=` requested; the default footer is shown until it
 * answers and whenever the request fails.
 */
export function useLocalizedFooterHtml(
  defaultHtml: string | undefined
): string | undefined {
  const { status } = useStatus()
  const lang = useContentLanguage()
  const enabled = Boolean(status?.footer_i18n_enabled)
  const { data } = useQuery({
    queryKey: ['footer', lang],
    queryFn: () => getFooter(lang),
    enabled,
    staleTime: 5 * 60 * 1000,
    meta: { errorToast: false },
  })

  if (!enabled || !data?.success) return defaultHtml
  return data.data ?? ''
}
