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
import { formatNumber } from '@/lib/format'

import type {
  PromptAuditStructureSignalCounts,
  PromptAuditStructureSignalSums,
} from '../api'

/** The fingerprint and prefix hash of requests without a system prompt. */
export const STRUCTURE_NO_SYSTEM = 'none'

/** Request formats with their English i18n keys; unknown ones show as sent. */
export const STRUCTURE_REQUEST_FORMAT_LABELS: Record<string, string> = {
  openai_chat: 'OpenAI Chat',
  anthropic_messages: 'Anthropic Messages',
  gemini: 'Gemini',
  openai_responses: 'OpenAI Responses',
  openai_images: 'OpenAI Images',
  generic: 'Generic',
}

/** Client classes with their English i18n keys; unknown ones show as sent. */
export const STRUCTURE_CLIENT_CLASS_LABELS: Record<string, string> = {
  claude_code: 'Claude Code',
  codex: 'Codex',
  gemini_cli: 'Gemini CLI',
  sdk: 'SDK',
  browser: 'Browser',
  other: 'Other',
}

type StructureSignalColumn =
  | { key: keyof PromptAuditStructureSignalCounts; label: string; rate: true }
  | { key: keyof PromptAuditStructureSignalSums; label: string; rate: false }

/**
 * Signal columns in display order, with English i18n keys. A rate column is a
 * request count shown with its share of the day; the others are plain sums.
 */
export const STRUCTURE_SIGNAL_COLUMNS: readonly StructureSignalColumn[] = [
  { key: 'with_system', label: 'System prompt', rate: true },
  { key: 'with_developer', label: 'Developer message', rate: true },
  { key: 'with_history_user', label: 'Earlier user turns', rate: true },
  {
    key: 'with_history_assistant',
    label: 'Earlier assistant turns',
    rate: true,
  },
  { key: 'trailing_assistant', label: 'Assistant prefill', rate: true },
  { key: 'with_agent_context', label: 'Agent context', rate: true },
  { key: 'with_tool', label: 'Tool use', rate: true },
  { key: 'user_turns', label: 'User turns (total)', rate: false },
  { key: 'assistant_turns', label: 'Assistant turns (total)', rate: false },
  { key: 'assistant_turns_10_plus', label: '10+ assistant turns', rate: true },
  { key: 'mid_system', label: 'System message mid-conversation', rate: true },
  { key: 'control_tokens', label: 'Template control tokens', rate: true },
  { key: 'role_markers', label: 'Role label lines', rate: true },
  { key: 'invisible_requests', label: 'Invisible characters', rate: true },
  {
    key: 'invisible_chars',
    label: 'Invisible characters (total)',
    rate: false,
  },
  { key: 'encoded_requests', label: 'Long encoded runs', rate: true },
  { key: 'encoded_runs', label: 'Long encoded runs (total)', rate: false },
  { key: 'system_under_1k', label: 'System prompt < 1K chars', rate: true },
  { key: 'system_under_4k', label: 'System prompt < 4K chars', rate: true },
  { key: 'system_under_16k', label: 'System prompt < 16K chars', rate: true },
  { key: 'system_under_64k', label: 'System prompt < 64K chars', rate: true },
  { key: 'system_64k_plus', label: 'System prompt ≥ 64K chars', rate: true },
]

/**
 * Formats a request counter with its occurrence rate, for example
 * "25 (12.5%)". A day without requests has no rate.
 */
export function formatStructureSignalRate(
  count: number,
  requests: number,
  locales?: Intl.LocalesArgument
): string {
  const formatted = formatNumber(count, locales)
  if (!(requests > 0)) return `${formatted} (-)`
  const percent = Math.round((count / requests) * 1000) / 10
  return `${formatted} (${formatNumber(percent, locales)}%)`
}

/** Distinct users; a capped count is a lower bound. */
export function formatStructureUserCount(
  count: number,
  capped: boolean,
  locales?: Intl.LocalesArgument
): string {
  const formatted = formatNumber(count, locales)
  return capped ? `≥${formatted}` : formatted
}

/** Converts a local date to the yyyymmdd number the API takes. */
export function toStructureStatDay(date: Date): number {
  return (
    date.getFullYear() * 10000 + (date.getMonth() + 1) * 100 + date.getDate()
  )
}

/** Shows a yyyymmdd stat day as YYYY-MM-DD; other values show as sent. */
export function formatStructureStatDay(day: number): string {
  const text = String(day)
  if (!/^\d{8}$/.test(text)) return text
  return `${text.slice(0, 4)}-${text.slice(4, 6)}-${text.slice(6)}`
}
