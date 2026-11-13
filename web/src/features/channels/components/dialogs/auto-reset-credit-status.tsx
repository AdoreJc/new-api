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
import type { TFunction } from 'i18next'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import dayjs from '@/lib/dayjs'

import type {
  AutoResetDecision,
  AutoResetHit,
  AutoResetView,
} from '../../lib/auto-reset-rule'

// Decision reasons sent by the server, mapped to English locale keys.
const REASON_LABELS: Record<string, string> = {
  disabled: 'Automatic reset is off',
  unsupported_channel: 'This channel is not supported',
  channel_disabled: 'The channel is disabled',
  no_rule: 'No conditions are set',
  usage_unavailable: 'Usage data is unavailable',
  not_matched: 'Conditions are not met',
  backoff: 'Waiting after a failed attempt',
  min_interval: 'Minimum interval not reached',
  daily_cap: 'Daily limit reached',
  weekly_cap: 'Weekly limit reached',
  window_resets_soon: 'The window resets on its own soon',
  no_credit: 'No redeemable reset credit',
  busy: 'Another redemption is in progress',
  would_redeem: 'A credit would be redeemed now',
  redeemed: 'Redeemed',
  failed: 'Redemption failed',
  in_flight: 'A redemption is in progress or was interrupted',
  invalid_setting: 'The stored setting is invalid',
  credential_problem: 'Credentials were rejected or unavailable',
}

const SOURCE_LABELS: Record<string, string> = {
  auto: 'Automatic',
  manual: 'Manual',
  subscription: 'Subscriber',
}

const WEEKLY_LABELS: Record<string, string> = {
  seven_day: '7-day window',
  seven_day_sonnet: '7-day Sonnet window',
  seven_day_overage_included: '7-day Fable window',
}

function autoResetReasonLabel(reason: string | undefined, t: TFunction) {
  if (!reason) return '-'
  return t(REASON_LABELS[reason] ?? reason)
}

function reasonVariant(reason: string): StatusBadgeProps['variant'] {
  if (reason === 'redeemed' || reason === 'would_redeem') return 'success'
  if (reason === 'failed' || reason === 'usage_unavailable') return 'danger'
  if (reason === 'not_matched' || reason === 'disabled') return 'neutral'
  return 'warning'
}

function formatUnix(seconds: number | undefined): string {
  return seconds ? dayjs.unix(seconds).format('YYYY-MM-DD HH:mm') : '-'
}

function describeAutoResetHit(hit: AutoResetHit, t: TFunction): string {
  switch (hit.type) {
    case 'five_hour_used_pct':
      return t('5-hour usage {{value}}% (threshold {{threshold}}%)', {
        value: hit.value ?? 0,
        threshold: hit.threshold ?? 0,
      })
    case 'weekly_used_pct':
      return t('{{window}} usage {{value}}% (threshold {{threshold}}%)', {
        window: t(WEEKLY_LABELS[hit.window ?? 'seven_day'] ?? hit.window ?? ''),
        value: hit.value ?? 0,
        threshold: hit.threshold ?? 0,
      })
    case 'credit_expires_within':
      return t('Earliest credit expires in {{value}} h (within {{hours}} h)', {
        value: hit.value ?? 0,
        hours: hit.hours ?? 0,
      })
    case 'at_time':
      return t('Alarm at {{time}}', {
        time: hit.alarm ? dayjs(hit.alarm).format('YYYY-MM-DD HH:mm') : '-',
      })
  }
}

function Field(props: { label: string; children: ReactNode }) {
  return (
    <div className='flex flex-col gap-1'>
      <span className='text-muted-foreground text-xs'>{props.label}</span>
      <div className='text-sm'>{props.children}</div>
    </div>
  )
}

/** Outcome of one evaluation, from the trial button or the scheduler. */
export function AutoResetDecisionPanel(props: {
  decision: AutoResetDecision
  title: string
}) {
  const { t } = useTranslation()
  const decision = props.decision
  const usage = Object.entries(decision.usage ?? {})
  return (
    <section
      className='flex flex-col gap-3 rounded-lg border p-3'
      aria-label={props.title}
    >
      <div className='flex flex-wrap items-center gap-2'>
        <span className='text-sm font-medium'>{props.title}</span>
        <StatusBadge
          label={autoResetReasonLabel(decision.reason, t)}
          variant={reasonVariant(decision.reason)}
          copyable={false}
        />
        <span className='text-muted-foreground text-xs'>
          {formatUnix(decision.evaluated_at)}
        </span>
      </div>
      <Field label={t('Matched conditions')}>
        {decision.hits.length === 0 ? (
          '-'
        ) : (
          <ul className='list-inside list-disc'>
            {decision.hits.map((hit) => (
              <li key={`${hit.type}:${hit.window ?? ''}:${hit.alarm ?? ''}`}>
                {describeAutoResetHit(hit, t)}
              </li>
            ))}
          </ul>
        )}
      </Field>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-3'>
        <Field label={t('Usage')}>
          {usage.length === 0
            ? '-'
            : usage
                .map(([key, w]) => `${key}: ${Math.round(w.utilization)}%`)
                .join(', ')}
        </Field>
        <Field label={t('Redeemable credits')}>
          {decision.credits ? decision.credits.available : '-'}
        </Field>
        <Field label={t('Next allowed')}>
          {formatUnix(decision.next_allowed_at)}
        </Field>
      </div>
      {decision.result ? (
        <Field label={t('Result')}>{decision.result}</Field>
      ) : null}
    </section>
  )
}

/** Stored state: last attempt, last skip and when the next attempt may run. */
export function AutoResetStatePanel(props: { view: AutoResetView }) {
  const { t } = useTranslation()
  const state = props.view.state
  let lastResult = '-'
  if (state.last_attempt_at) {
    lastResult = `${formatUnix(state.last_attempt_at)} · ${
      state.last_success ? t('Succeeded') : t('Failed')
    } (${state.last_result ?? '-'})`
  }
  return (
    <section
      className='grid grid-cols-1 gap-3 rounded-lg border p-3 sm:grid-cols-2'
      aria-label={t('Automatic reset status')}
    >
      <Field label={t('Last automatic redemption')}>{lastResult}</Field>
      <Field label={t('Last skip')}>
        {state.last_skip_reason
          ? `${formatUnix(state.last_skip_at)} · ${autoResetReasonLabel(state.last_skip_reason, t)}`
          : '-'}
      </Field>
      <Field label={t('Next allowed')}>
        {props.view.next_allowed_at
          ? `${formatUnix(props.view.next_allowed_at)} · ${autoResetReasonLabel(props.view.next_allowed_reason, t)}`
          : t('Now')}
      </Field>
      <Field label={t('Next alarm')}>
        {props.view.next_alarm_at
          ? dayjs(props.view.next_alarm_at).format('YYYY-MM-DD HH:mm')
          : '-'}
      </Field>
      <Field label={t('Last redemption (any source)')}>
        {state.last_redeem_at
          ? `${formatUnix(state.last_redeem_at)} · ${t(SOURCE_LABELS[state.last_redeem_source ?? ''] ?? state.last_redeem_source ?? '-')}`
          : '-'}
      </Field>
      {state.in_flight_since ? (
        <Field label={t('Redemption in progress')}>
          {`${formatUnix(state.in_flight_since)} · ${t(SOURCE_LABELS[state.in_flight_source ?? ''] ?? state.in_flight_source ?? '-')}`}
        </Field>
      ) : null}
      <Field label={t('Automatic redemptions in the last 7 days')}>
        {state.redeemed_at?.length ?? 0}
      </Field>
      <Field label={t('Consecutive failures')}>{state.fail_count ?? 0}</Field>
    </section>
  )
}
