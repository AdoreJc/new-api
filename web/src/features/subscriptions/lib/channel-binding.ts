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
import type {
  SubscriptionBindableChannel,
  SubscriptionUsageData,
  SubscriptionUsageWindow,
  UserSubscription,
} from '../types'

/**
 * Pure helpers for subscriptions bound to an OAuth channel account
 * (Claude Subscription (OAuth) type 64, ChatGPT Subscription (Codex) type 57).
 */

export const BOUND_CHANNEL_TYPE_CODEX = 57
export const BOUND_CHANNEL_TYPE_CLAUDE_OAUTH = 64

const BOUND_ACCOUNT_LABEL_KEYS: Record<number, string> = {
  [BOUND_CHANNEL_TYPE_CLAUDE_OAUTH]: 'Claude Subscription (OAuth)',
  [BOUND_CHANNEL_TYPE_CODEX]: 'ChatGPT Subscription (Codex)',
}

/** i18n key naming the kind of account bound to the subscription. */
export function boundAccountLabelKey(channelType: number | undefined): string {
  if (channelType == null) return 'OAuth account'
  return BOUND_ACCOUNT_LABEL_KEYS[channelType] ?? 'OAuth account'
}

/**
 * Builds the picker label for one OAuth channel. Channels bound to another
 * active subscription stay listed so the administrator sees why they cannot
 * be chosen.
 */
export function bindableChannelLabel(
  channel: SubscriptionBindableChannel,
  t: (key: string, opts?: Record<string, unknown>) => string
): string {
  const kind = t(boundAccountLabelKey(channel.type))
  const base = `#${channel.id} ${channel.name || '-'} · ${kind}`
  if (channel.bound_subscription_id > 0) {
    return `${base} · ${t('Bound to {{user}}', {
      user: channel.bound_username || `#${channel.bound_user_id}`,
    })}`
  }
  if (channel.group) {
    return `${base} · ${t('Group')}: ${channel.group}`
  }
  return base
}

const BOUND_GROUP_PREFIXES: Record<number, string> = {
  [BOUND_CHANNEL_TYPE_CLAUDE_OAUTH]: 'claude-ch',
  [BOUND_CHANNEL_TYPE_CODEX]: 'codex-ch',
}

/**
 * Dedicated group a new binding of the channel moves into: claude-ch<id> for
 * Claude Subscription (OAuth), codex-ch<id> for ChatGPT Subscription (Codex),
 * '' for channel types that cannot be bound. Mirrors
 * model.SubscriptionBoundGroupName.
 */
export function subscriptionBoundGroupName(
  channel: Pick<SubscriptionBindableChannel, 'id' | 'type'>
): string {
  const prefix = BOUND_GROUP_PREFIXES[channel.type]
  return prefix ? `${prefix}${channel.id}` : ''
}

export function isSubscriptionBound(
  sub: Pick<UserSubscription, 'bound_channel_id'> | null | undefined
): boolean {
  return Number(sub?.bound_channel_id ?? 0) > 0
}

export type SubscriptionUsageWindowView = {
  key: string
  /** i18n key of the window title */
  labelKey: string
  /** 0-100 */
  utilization: number
  resetsAt: string | null
  windowSeconds: number | null
  source: 'api' | 'passive'
  sampledAt: string | null
}

const CLAUDE_WINDOW_LABELS: { key: string; labelKey: string }[] = [
  { key: 'five_hour', labelKey: '5-Hour Window' },
  { key: 'seven_day', labelKey: '7-Day Window' },
  { key: 'seven_day_sonnet', labelKey: '7-Day Sonnet Window' },
  { key: 'seven_day_overage_included', labelKey: '7-Day Fable Window' },
]

const CODEX_WINDOW_LABELS: { key: string; labelKey: string }[] = [
  { key: 'primary', labelKey: 'Primary Window' },
  { key: 'secondary', labelKey: 'Secondary Window' },
]

function clampUtilization(value: unknown): number {
  const v = Number(value)
  if (!Number.isFinite(v)) return 0
  return Math.max(0, Math.min(100, v))
}

function readWindow(
  key: string,
  labelKey: string,
  raw: SubscriptionUsageWindow | undefined
): SubscriptionUsageWindowView | null {
  if (!raw || typeof raw !== 'object') return null
  const resetsAt =
    typeof raw.resets_at === 'string' && raw.resets_at.trim() !== ''
      ? raw.resets_at
      : null
  const windowSeconds = Number(raw.window_seconds)
  const passive = raw.source === 'passive'
  return {
    key,
    labelKey,
    utilization: clampUtilization(raw.utilization),
    resetsAt,
    windowSeconds:
      Number.isFinite(windowSeconds) && windowSeconds > 0
        ? windowSeconds
        : null,
    source: passive ? 'passive' : 'api',
    sampledAt:
      passive && typeof raw.sampled_at === 'string' && raw.sampled_at !== ''
        ? raw.sampled_at
        : null,
  }
}

/**
 * Orders the windows the server returned for display. Claude always shows
 * the 5-hour and 7-day windows (empty when upstream omitted them); Codex
 * shows the primary and secondary windows the same way.
 */
export function parseSubscriptionUsageWindows(
  data: SubscriptionUsageData | null | undefined
): (SubscriptionUsageWindowView | { key: string; labelKey: string })[] {
  if (!data) return []
  const windows = data.windows ?? {}
  const definitions =
    data.provider === 'codex' ? CODEX_WINDOW_LABELS : CLAUDE_WINDOW_LABELS
  const required = new Set(
    data.provider === 'codex'
      ? ['primary', 'secondary']
      : ['five_hour', 'seven_day']
  )
  const result: (
    | SubscriptionUsageWindowView
    | { key: string; labelKey: string }
  )[] = []
  for (const definition of definitions) {
    const parsed = readWindow(
      definition.key,
      definition.labelKey,
      windows[definition.key]
    )
    if (parsed) {
      result.push(parsed)
    } else if (required.has(definition.key)) {
      result.push({ key: definition.key, labelKey: definition.labelKey })
    }
  }
  return result
}

export function isSubscriptionUsageWindow(
  value: SubscriptionUsageWindowView | { key: string; labelKey: string }
): value is SubscriptionUsageWindowView {
  return 'utilization' in value
}
