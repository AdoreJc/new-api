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

/**
 * Pure helpers for the Claude Subscription (OAuth) usage document returned by
 * GET /api/channel/:id/claude/usage, which passes through Anthropic's
 * /api/oauth/usage response.
 */

export type ClaudeUsageWindowKey =
  | 'five_hour'
  | 'seven_day'
  | 'seven_day_sonnet'
  | 'seven_day_overage_included'

export type ClaudeUsageWindow = {
  key: ClaudeUsageWindowKey
  /** i18n key of the window title */
  labelKey: string
  /** 0-100, clamped */
  utilization: number
  /** ISO timestamp or null when upstream omitted it */
  resetsAt: string | null
  /**
   * 'passive' when the server filled the window from the rate-limit headers
   * of the last relayed request because the usage API omitted it
   */
  source: 'api' | 'passive'
  /** ISO timestamp of the sampled response, only with source 'passive' */
  sampledAt: string | null
}

const WINDOW_DEFINITIONS: {
  key: ClaudeUsageWindowKey
  labelKey: string
  optional: boolean
}[] = [
  { key: 'five_hour', labelKey: '5-Hour Window', optional: false },
  { key: 'seven_day', labelKey: '7-Day Window', optional: false },
  { key: 'seven_day_sonnet', labelKey: '7-Day Sonnet Window', optional: true },
  // Anthropic's seven_day_overage_included claim (rate-limit header window
  // 7d_oi) is the 7-day window that counts Fable models only.
  {
    key: 'seven_day_overage_included',
    labelKey: '7-Day Fable Window',
    optional: true,
  },
]

export function clampUtilization(value: unknown): number {
  const v = Number(value)
  if (!Number.isFinite(v)) return 0
  return Math.max(0, Math.min(100, v))
}

function readWindow(
  raw: unknown
): Pick<
  ClaudeUsageWindow,
  'utilization' | 'resetsAt' | 'source' | 'sampledAt'
> | null {
  if (!raw || typeof raw !== 'object') return null
  const block = raw as Record<string, unknown>
  const resetsAt =
    typeof block.resets_at === 'string' && block.resets_at.trim() !== ''
      ? block.resets_at
      : null
  const passive = block.source === 'passive'
  const sampledAt =
    passive &&
    typeof block.sampled_at === 'string' &&
    block.sampled_at.trim() !== ''
      ? block.sampled_at
      : null
  return {
    utilization: clampUtilization(block.utilization),
    resetsAt,
    source: passive ? 'passive' : 'api',
    sampledAt,
  }
}

/**
 * Returns the windows to display, in a fixed order. The two base windows are
 * always present (empty when upstream omitted them); the optional Sonnet and
 * Fable windows appear only when upstream returned them or the server filled
 * them from sampled headers.
 */
export function parseClaudeUsageWindows(
  payload: unknown
): (ClaudeUsageWindow | { key: ClaudeUsageWindowKey; labelKey: string })[] {
  const doc =
    payload && typeof payload === 'object'
      ? (payload as Record<string, unknown>)
      : {}
  const windows: (
    | ClaudeUsageWindow
    | { key: ClaudeUsageWindowKey; labelKey: string }
  )[] = []
  for (const definition of WINDOW_DEFINITIONS) {
    const parsed = readWindow(doc[definition.key])
    if (parsed) {
      windows.push({
        key: definition.key,
        labelKey: definition.labelKey,
        ...parsed,
      })
    } else if (!definition.optional) {
      windows.push({ key: definition.key, labelKey: definition.labelKey })
    }
  }
  return windows
}

export function isClaudeUsageWindow(
  value: ClaudeUsageWindow | { key: ClaudeUsageWindowKey; labelKey: string }
): value is ClaudeUsageWindow {
  return 'utilization' in value
}

export type ClaudeResetCreditView = {
  label: string
  resets_left: number
  starts_at?: string | null
  expires_at?: string | null
  clears: string[]
  percent_used: Record<string, number>
  blocking: string[]
  use_requires_limit: boolean
  redeemable: boolean
}

export type ClaudeResetCreditsView = {
  eligible: boolean
  at_limit: boolean
  available_count: number
  credits: ClaudeResetCreditView[]
  cooldown_until?: string | null
  weekly_resets_at?: string | null
  fetched_at?: string
}

/** Reads the projected reset-credit document, tolerating missing fields. */
export function parseClaudeResetCredits(
  payload: unknown
): ClaudeResetCreditsView | null {
  if (!payload || typeof payload !== 'object') return null
  const doc = payload as Record<string, unknown>
  const rawCredits = Array.isArray(doc.credits) ? doc.credits : []
  const credits: ClaudeResetCreditView[] = []
  for (const item of rawCredits) {
    if (!item || typeof item !== 'object') continue
    const credit = item as Record<string, unknown>
    const percentUsed: Record<string, number> = {}
    if (credit.percent_used && typeof credit.percent_used === 'object') {
      for (const [window, value] of Object.entries(
        credit.percent_used as Record<string, unknown>
      )) {
        const n = Number(value)
        if (Number.isFinite(n)) percentUsed[window] = n
      }
    }
    credits.push({
      label: typeof credit.label === 'string' ? credit.label : '',
      resets_left: Number.isFinite(Number(credit.resets_left))
        ? Number(credit.resets_left)
        : 0,
      starts_at: typeof credit.starts_at === 'string' ? credit.starts_at : null,
      expires_at:
        typeof credit.expires_at === 'string' ? credit.expires_at : null,
      clears: Array.isArray(credit.clears)
        ? credit.clears.filter((c): c is string => typeof c === 'string')
        : [],
      percent_used: percentUsed,
      blocking: Array.isArray(credit.blocking)
        ? credit.blocking.filter((b): b is string => typeof b === 'string')
        : [],
      use_requires_limit: credit.use_requires_limit === true,
      redeemable: credit.redeemable === true,
    })
  }
  return {
    eligible: doc.eligible === true,
    at_limit: doc.at_limit === true,
    available_count: Number.isFinite(Number(doc.available_count))
      ? Number(doc.available_count)
      : 0,
    credits,
    cooldown_until:
      typeof doc.cooldown_until === 'string' ? doc.cooldown_until : null,
    weekly_resets_at:
      typeof doc.weekly_resets_at === 'string' ? doc.weekly_resets_at : null,
    fetched_at: typeof doc.fetched_at === 'string' ? doc.fetched_at : undefined,
  }
}

const RESET_OUTCOME_KEYS: Record<string, string> = {
  reset: 'Reset completed. Latest usage has been refreshed.',
  already_used: 'This reset credit was already used.',
  not_limited: 'The account is not rate limited, so nothing was reset.',
  cooldown: 'Reset is in cooldown. Try again later.',
  ineligible: 'This account is not eligible for a reset.',
  unavailable: 'The reset service is unavailable right now.',
  unknown:
    'The reset result could not be confirmed. Check the usage before trying again.',
}

/** Translated title of a Claude usage window, or the raw key when unknown. */
export function claudeWindowName(
  key: string,
  t: (key: string) => string
): string {
  const definition = WINDOW_DEFINITIONS.find((d) => d.key === key)
  return definition ? t(definition.labelKey) : key
}

/**
 * Message for the answer of a Claude reset request: the outcome sentence and
 * the windows upstream reports as cleared. Only the 'reset' outcome succeeded.
 */
export function describeClaudeResetOutcome(
  payload: unknown,
  t: (key: string) => string
): { succeeded: boolean; message: string } {
  const doc =
    payload && typeof payload === 'object'
      ? (payload as Record<string, unknown>)
      : {}
  const outcome = typeof doc.outcome === 'string' ? doc.outcome : ''
  const cleared = Array.isArray(doc.cleared)
    ? doc.cleared.filter((w): w is string => typeof w === 'string')
    : []
  const clearedText =
    cleared.length > 0
      ? ` ${t('Cleared windows:')} ${cleared.map((w) => claudeWindowName(w, t)).join(', ')}`
      : ''
  const key = RESET_OUTCOME_KEYS[outcome] ?? RESET_OUTCOME_KEYS.unknown
  return { succeeded: outcome === 'reset', message: `${t(key)}${clearedText}` }
}
