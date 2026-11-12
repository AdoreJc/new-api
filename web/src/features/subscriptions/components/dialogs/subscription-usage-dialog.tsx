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

For commercial licensing, please contact support@quantumnous.com
*/
import { RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  UsageSectionHeading,
  UsageWindowCard,
} from '@/features/channels/components/dialogs/usage-dialog-shared'
import {
  formatUsageDurationSeconds,
  formatUsageIsoTimestamp,
  formatUsageTimeLeftUntil,
} from '@/features/channels/lib/usage-format'
import { createServerError } from '@/lib/server-error-message'

import { getSelfSubscriptionUsage } from '../../api'
import {
  buildCcSwitchUsageScript,
  CC_SWITCH_HOURS_PLACEHOLDER,
  CC_SWITCH_MINUTES_PLACEHOLDER,
} from '../../lib/cc-switch-usage-script'
import {
  BOUND_CHANNEL_TYPE_CODEX,
  boundAccountLabelKey,
  isSubscriptionUsageWindow,
  parseSubscriptionUsageWindows,
} from '../../lib/channel-binding'
import type { SubscriptionUsageData, UserSubscription } from '../../types'
import { SubscriptionResetCreditsSection } from './subscription-reset-credits-section'

// Splits the translated "resets in" text around the time the script fills in.
const TIME_PLACEHOLDER = '__TIME__'

type SubscriptionUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  subscription: UserSubscription | null
}

/**
 * Shows the subscriber the rate-limit windows of the OAuth account bound to
 * one of their subscriptions. The data comes from the trimmed
 * /api/subscription/self/usage/:id endpoint. Reset credits appear only when
 * the administrator enabled them for this user (can_reset); otherwise no
 * reset-credit request is made.
 */
export function SubscriptionUsageDialog(props: SubscriptionUsageDialogProps) {
  const { t } = useTranslation()
  const [data, setData] = useState<SubscriptionUsageData | null>(null)
  const [upstreamStatus, setUpstreamStatus] = useState<number | null>(null)
  const [errorMessage, setErrorMessage] = useState('')
  const [isLoading, setIsLoading] = useState(false)

  const subscriptionId = props.subscription?.id ?? 0

  const load = useCallback(async () => {
    if (!subscriptionId) return
    setIsLoading(true)
    setErrorMessage('')
    try {
      const res = await getSelfSubscriptionUsage(subscriptionId)
      setUpstreamStatus(res.upstream_status ?? null)
      if (!res.success || !res.data) {
        throw createServerError(res, t('Failed to fetch usage'))
      }
      setData(res.data)
    } catch (error) {
      setData(null)
      setErrorMessage(
        error instanceof Error ? error.message : t('Failed to fetch usage')
      )
    } finally {
      setIsLoading(false)
    }
  }, [subscriptionId, t])

  useEffect(() => {
    if (props.open && subscriptionId) {
      void load()
    }
  }, [props.open, subscriptionId, load])

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setData(null)
      setUpstreamStatus(null)
      setErrorMessage('')
    }
    props.onOpenChange(nextOpen)
  }

  const windows = useMemo(() => parseSubscriptionUsageWindows(data), [data])
  const measured = windows.filter(isSubscriptionUsageWindow)
  let statusBadge = (
    <StatusBadge label={t('Pending')} variant='neutral' copyable={false} />
  )
  if (data?.limit_reached || measured.some((w) => w.utilization >= 100)) {
    statusBadge = (
      <StatusBadge label={t('Limited')} variant='danger' copyable={false} />
    )
  } else if (measured.length > 0) {
    statusBadge = (
      <StatusBadge label={t('Available')} variant='success' copyable={false} />
    )
  }

  const accountLabel = t(
    boundAccountLabelKey(
      data?.channel_type ?? props.subscription?.bound_channel_type
    )
  )

  const channelType =
    data?.channel_type ?? props.subscription?.bound_channel_type
  const [resetsInPrefix, resetsInSuffix] = t('Resets in {{time}}', {
    time: TIME_PLACEHOLDER,
  }).split(TIME_PLACEHOLDER)
  const ccSwitchScript = buildCcSwitchUsageScript(
    channelType === BOUND_CHANNEL_TYPE_CODEX ? 'codex' : 'claude',
    {
      failed: t('Failed to fetch usage'),
      noSubscription: t('No bound subscription'),
      resetsInPrefix: resetsInPrefix ?? '',
      resetsInSuffix: resetsInSuffix ?? '',
      hoursMinutes: t('{{hours}}h {{minutes}}m', {
        hours: CC_SWITCH_HOURS_PLACEHOLDER,
        minutes: CC_SWITCH_MINUTES_PLACEHOLDER,
      }),
      minutes: t('{{minutes}}m', { minutes: CC_SWITCH_MINUTES_PLACEHOLDER }),
    }
  )

  let body: React.ReactNode
  if (isLoading && !data) {
    body = (
      <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
        <Skeleton className='h-36 w-full' />
        <Skeleton className='h-36 w-full' />
      </div>
    )
  } else if (errorMessage) {
    body = (
      <div
        role='alert'
        className='border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm'
      >
        {errorMessage}
      </div>
    )
  } else {
    body = (
      <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
        {windows.map((window) => {
          const view = isSubscriptionUsageWindow(window) ? window : null
          const passive = view?.source === 'passive'
          const details = [
            {
              label: t('Reset at:'),
              value: view ? formatUsageIsoTimestamp(view.resetsAt) : '-',
            },
            {
              label: t('Resets in:'),
              value: view ? formatUsageTimeLeftUntil(view.resetsAt, t) : '-',
            },
          ]
          if (passive) {
            details.push({
              label: t('Sampled at:'),
              value: formatUsageIsoTimestamp(view?.sampledAt),
            })
          }
          return (
            <UsageWindowCard
              key={window.key}
              title={t(window.labelKey)}
              badge={
                passive ? (
                  <StatusBadge
                    label={t('From response headers')}
                    variant='neutral'
                    copyable={false}
                  />
                ) : undefined
              }
              subtitle={
                view?.windowSeconds
                  ? formatUsageDurationSeconds(view.windowSeconds, t)
                  : undefined
              }
              percent={view ? Math.round(view.utilization * 10) / 10 : null}
              details={details}
            />
          )
        })}
      </div>
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={t('Bound account usage')}
      description={t(
        'Rate-limit windows of the account bound to this subscription. Requests from your tokens are routed to this account.'
      )}
      contentClassName='sm:max-w-[760px]'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <>
          <CopyButton
            value={ccSwitchScript}
            variant='outline'
            size='default'
            tooltip={t(
              'Paste into CC Switch as the custom usage script of a provider that uses an API key of this account'
            )}
            aria-label={t('Copy CC Switch usage script')}
          >
            {t('Copy CC Switch usage script')}
          </CopyButton>
          <Button
            type='button'
            variant='outline'
            onClick={() => handleOpenChange(false)}
          >
            {t('Close')}
          </Button>
        </>
      }
    >
      <UsageSectionHeading
        title={accountLabel}
        description={
          data?.plan_type
            ? `${t('Plan')}: ${data.plan_type}`
            : `${t('Subscription')} #${subscriptionId || '-'}`
        }
      >
        {statusBadge}
        {upstreamStatus != null ? (
          <StatusBadge
            label={`HTTP ${upstreamStatus}`}
            variant='neutral'
            copyable={false}
          />
        ) : null}
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => void load()}
          disabled={isLoading || !subscriptionId}
        >
          <RefreshCw data-icon='inline-start' />
          {t('Refresh')}
        </Button>
      </UsageSectionHeading>
      {body}
      {data?.can_reset && !errorMessage ? (
        <SubscriptionResetCreditsSection
          key={subscriptionId}
          subscriptionId={subscriptionId}
          provider={data.provider}
          onReset={load}
        />
      ) : null}
    </Dialog>
  )
}
