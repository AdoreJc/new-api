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
import { INTERFACE_LANGUAGE_OPTIONS, toIntlLocale } from '@/i18n/languages'

import { safeJsonParse } from './json-parser'

/**
 * Per-language overrides of public site content are stored as one JSON option
 * per field (`site_content_i18n.<field>`), mapping BCP-47 tags to text. The
 * legacy single-value option stays the default content.
 */
export type LocalizedContentMap = Record<string, string>

export type ContentLanguage = {
  /** Canonical BCP-47 tag used as the override key, e.g. `zh-TW`. */
  tag: string
  /** Native language name. */
  label: string
}

/** The interface languages, keyed by the BCP-47 tag the server expects. */
export const CONTENT_LANGUAGES: ContentLanguage[] =
  INTERFACE_LANGUAGE_OPTIONS.map((option) => ({
    tag: toIntlLocale(option.code) ?? option.code,
    label: option.label,
  }))

/** Read an option value; anything but a JSON object of strings yields {}. */
export function parseLocalizedContent(raw: unknown): LocalizedContentMap {
  const parsed =
    typeof raw === 'string'
      ? safeJsonParse<unknown>(raw, { fallback: {}, silent: true })
      : raw
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return {}
  }
  const result: LocalizedContentMap = {}
  for (const [tag, text] of Object.entries(parsed)) {
    if (typeof text === 'string') result[tag] = text
  }
  return result
}

/**
 * Serialize with sorted keys so equal maps compare equal as strings. Empty
 * entries are dropped (the language then uses the default content); with
 * `dropBlank`, whitespace-only entries are dropped too, which is what is
 * saved.
 */
export function serializeLocalizedContent(
  map: LocalizedContentMap,
  options: { dropBlank?: boolean } = {}
): string {
  const entries = Object.entries(map)
    .filter(([, text]) =>
      options.dropBlank ? text.trim() !== '' : text !== ''
    )
    .sort(([left], [right]) => left.localeCompare(right))
  return JSON.stringify(Object.fromEntries(entries))
}

/** Normalize a stored option value into the form's comparable string. */
export function normalizeLocalizedContentOption(raw: unknown): string {
  return serializeLocalizedContent(parseLocalizedContent(raw))
}

/** The value submitted for a site_content_i18n.* option. */
export function toLocalizedContentOptionValue(raw: unknown): string {
  return serializeLocalizedContent(parseLocalizedContent(raw), {
    dropBlank: true,
  })
}
