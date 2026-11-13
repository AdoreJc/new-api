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
import type { PromptAuditEventFilter } from '../api'

/** Matches the server limit on a scanned search pattern. */
export const PROMPT_AUDIT_KEYWORD_MAX_LENGTH = 512

export const PROMPT_AUDIT_FULL_PROMPT_FIELD = 'full_prompt'

/** Searchable fields; the server searches all but full_prompt by default. */
export const PROMPT_AUDIT_KEYWORD_FIELDS = [
  { value: 'username', labelKey: 'User' },
  { value: 'token_name', labelKey: 'Token name' },
  { value: 'model_name', labelKey: 'Model' },
  { value: 'redacted_preview', labelKey: 'Preview' },
  { value: 'request_id', labelKey: 'Request ID' },
  { value: PROMPT_AUDIT_FULL_PROMPT_FIELD, labelKey: 'Full prompt' },
] as const

export function promptAuditKeywordFields(
  filter: PromptAuditEventFilter
): string[] {
  return (filter.keyword_fields ?? '')
    .split(',')
    .map((field) => field.trim())
    .filter(Boolean)
}

/**
 * A regex or full prompt search is matched by scanning rows on the server:
 * it pages with a cursor and cannot delete by filter.
 */
export function isScannedPromptAuditSearch(
  filter: PromptAuditEventFilter
): boolean {
  if (!filter.keyword?.trim()) return false
  return (
    filter.keyword_mode === 'regex' ||
    promptAuditKeywordFields(filter).includes(PROMPT_AUDIT_FULL_PROMPT_FIELD)
  )
}

/**
 * A rough browser-side hint about a regex search pattern. It never blocks a
 * search: the server's RE2 compile result is authoritative. RE2 syntax that
 * JavaScript lacks is rewritten first: inline flags such as (?i), (?s) or
 * (?i:...) anywhere in the pattern, and named groups written (?P<name>...).
 * Returns the likely problem, or null when the pattern looks valid.
 */
export function promptAuditRegexProblem(
  pattern: string
): 'too_long' | 'invalid' | null {
  const trimmed = pattern.trim()
  if (!trimmed) return null
  if ([...trimmed].length > PROMPT_AUDIT_KEYWORD_MAX_LENGTH) {
    return 'too_long'
  }
  try {
    new RegExp(
      trimmed
        .replaceAll(/\(\?[imsU-]+\)/g, '')
        .replaceAll(/\(\?[imsU-]+:/g, '(?:')
        .replaceAll('(?P<', '(?<')
    )
  } catch {
    return 'invalid'
  }
  return null
}
