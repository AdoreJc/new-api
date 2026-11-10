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
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'

/**
 * BCP-47 tag (`en`, `zh-CN`, `zh-TW`, ...) of the current interface
 * language, sent as `?lang=` to the public site-content APIs (notice, about,
 * home page, legal documents, footer). The server picks the matching
 * administrator translation or falls back to the default content.
 *
 * Use it in query keys so switching language refetches without a reload.
 */
export function useContentLanguage(): string | undefined {
  const { i18n } = useTranslation()
  return toIntlLocale(i18n.resolvedLanguage || i18n.language)
}
