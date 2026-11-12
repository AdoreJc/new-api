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
import { PROMPT_AUDIT_SCANNER_LABELS } from '@/features/system-settings/security/prompt-audit/lib/form'

import type { PromptAuditEvent } from '../api'

export type PromptAuditCategory = {
  id: string
  /** i18n key for a known category; unknown ones show their id. */
  labelKey: string
  matched: boolean
}

function parseList(value: string | undefined): string[] {
  if (!value) return []
  try {
    const parsed: unknown = JSON.parse(value)
    return Array.isArray(parsed)
      ? parsed.filter((item): item is string => typeof item === 'string')
      : []
  } catch {
    return []
  }
}

/**
 * Lists the categories of an event: known Guard categories first (marked when
 * they are enabled in the settings), then unknown ones, which always count.
 */
export function promptAuditCategories(
  event: Pick<
    PromptAuditEvent,
    'categories' | 'matched_scanners' | 'unknown_categories'
  >
): PromptAuditCategory[] {
  const matched = new Set(parseList(event.matched_scanners))
  const known = parseList(event.categories).map((id) => ({
    id,
    labelKey: PROMPT_AUDIT_SCANNER_LABELS[id] ?? id,
    matched: matched.has(id),
  }))
  const unknown = parseList(event.unknown_categories).map((id) => ({
    id,
    labelKey: 'Unknown category',
    matched: true,
  }))
  return [...known, ...unknown]
}

export const PROMPT_AUDIT_DECISION_LABELS: Record<string, string> = {
  critical: 'Critical',
  flag: 'Flagged',
  pass: 'Passed',
}
