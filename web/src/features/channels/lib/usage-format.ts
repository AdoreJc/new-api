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
import type { StatusBadgeProps } from '@/components/status-badge'
import dayjs from '@/lib/dayjs'
import { formatDateTimeStr } from '@/lib/format'

/**
 * Formatting helpers shared by the subscription usage dialogs (Codex and
 * Claude).
 */

export function formatUsageIsoTimestamp(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '-'
  }
  const d = dayjs(value)
  if (!d.isValid()) {
    return value
  }
  return formatDateTimeStr(d.toDate())
}

export function formatUsageDurationSeconds(
  seconds: unknown,
  t: (key: string) => string
): string {
  const s = Number(seconds)
  if (!Number.isFinite(s) || s <= 0) {
    return '-'
  }

  const total = Math.floor(s)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60

  if (hours > 0) {
    return `${hours}${t('h')} ${minutes}${t('m')}`
  }
  if (minutes > 0) {
    return `${minutes}${t('m')} ${secs}${t('s')}`
  }
  return `${secs}${t('s')}`
}

export function formatUsageTimeLeftUntil(
  value: unknown,
  t: (key: string) => string
): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '-'
  }
  const expiresAt = dayjs(value)
  if (!expiresAt.isValid()) {
    return '-'
  }

  const secondsLeft = expiresAt.diff(dayjs(), 'second')
  if (secondsLeft <= 0) {
    return t('Expired')
  }

  const days = Math.floor(secondsLeft / (24 * 60 * 60))
  const remainingSeconds = secondsLeft % (24 * 60 * 60)
  if (days > 0) {
    const hours = Math.floor(remainingSeconds / 3600)
    return `${days} ${t('days')} ${hours}${t('h')}`
  }

  return formatUsageDurationSeconds(secondsLeft, t)
}

export function usagePercentVariant(
  percent: number
): NonNullable<StatusBadgeProps['variant']> {
  if (percent >= 95) {
    return 'danger'
  }
  if (percent >= 80) {
    return 'warning'
  }
  return 'info'
}

export const usagePercentTextClassName: Record<
  NonNullable<StatusBadgeProps['variant']>,
  string
> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-destructive',
  info: 'text-info',
  neutral: 'text-muted-foreground',
  purple: 'text-chart-4',
  amber: 'text-warning',
  blue: 'text-chart-1',
  cyan: 'text-chart-2',
  green: 'text-success',
  grey: 'text-muted-foreground',
  indigo: 'text-chart-1',
  'light-blue': 'text-info',
  'light-green': 'text-emerald-500 dark:text-emerald-300',
  lime: 'text-chart-3',
  orange: 'text-warning',
  pink: 'text-chart-5',
  red: 'text-destructive',
  teal: 'text-chart-2',
  violet: 'text-chart-4',
  yellow: 'text-warning',
}
