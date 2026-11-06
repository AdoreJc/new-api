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
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  RefreshCw,
  RotateCcw,
} from 'lucide-react'
import { type ReactNode, useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { createServerError, getServerMessage } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import {
  getClaudeResetCredits,
  resetClaudeUsage,
  type ClaudeResetCreditsResponse,
  type ClaudeUsageResponse,
} from '../../api'
import {
  isClaudeUsageWindow,
  parseClaudeResetCredits,
  parseClaudeUsageWindows,
  type ClaudeResetCreditView,
  type ClaudeResetCreditsView,
} from '../../lib/claude-usage'
import {
  formatUsageIsoTimestamp,
  formatUsageTimeLeftUntil,
} from '../../lib/usage-format'
import {
  UsageInfoField,
  UsageSectionHeading,
  UsageWindowCard,
} from './usage-dialog-shared'

export type ClaudeUsageDialogData = ClaudeUsageResponse

type ClaudeUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  /** Account email stored with the channel, if known */
  email?: string
  response: ClaudeUsageDialogData | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

const WINDOW_NAME_KEYS: Record<string, string> = {
  five_hour: '5-Hour Window',
  seven_day: '7-Day Window',
  seven_day_sonnet: '7-Day Sonnet Window',
  seven_day_overage_included: '7-Day Fable Window',
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

function windowName(key: string, t: (key: string) => string): string {
  return WINDOW_NAME_KEYS[key] ? t(WINDOW_NAME_KEYS[key]) : key
}

function getUsageStatusBadge(
  windows: ReturnType<typeof parseClaudeUsageWindows>,
  t: (key: string) => string
) {
  const measured = windows.filter(isClaudeUsageWindow)
  if (measured.length === 0) {
    return (
      <StatusBadge label={t('Pending')} variant='neutral' copyable={false} />
    )
  }
  if (measured.some((w) => w.utilization >= 100)) {
    return (
      <StatusBadge label={t('Limited')} variant='danger' copyable={false} />
    )
  }
  return (
    <StatusBadge label={t('Available')} variant='success' copyable={false} />
  )
}

function ClaudeUsageWindowGrid(props: {
  windows: ReturnType<typeof parseClaudeUsageWindows>
}) {
  const { t } = useTranslation()
  return (
    <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
      {props.windows.map((window) => {
        const measured = isClaudeUsageWindow(window) ? window : null
        const passive = measured?.source === 'passive'
        const details = [
          {
            label: t('Reset at:'),
            value: measured ? formatUsageIsoTimestamp(measured.resetsAt) : '-',
          },
          {
            label: t('Resets in:'),
            value: measured
              ? formatUsageTimeLeftUntil(measured.resetsAt, t)
              : '-',
          },
        ]
        if (passive) {
          details.push({
            label: t('Sampled at:'),
            value: formatUsageIsoTimestamp(measured?.sampledAt),
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
            percent={
              measured ? Math.round(measured.utilization * 10) / 10 : null
            }
            details={details}
          />
        )
      })}
    </div>
  )
}

function ResetCreditItem(props: {
  credit: ClaudeResetCreditView
  index: number
}) {
  const { t } = useTranslation()
  const title =
    props.credit.label.trim() || `${t('Reset Credit')} ${props.index + 1}`
  const expiresIn = formatUsageTimeLeftUntil(props.credit.expires_at, t)
  const badge: { label: string; variant: StatusBadgeProps['variant'] } = props
    .credit.redeemable
    ? { label: t('Redeemable'), variant: 'success' }
    : { label: t('Not redeemable'), variant: 'neutral' }
  const percentEntries = Object.entries(props.credit.percent_used)

  return (
    <div className='bg-background rounded-lg border p-3'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='flex flex-wrap items-center gap-2'>
            <div className='min-w-0 text-sm font-medium break-words'>
              {title}
            </div>
            <StatusBadge
              label={badge.label}
              variant={badge.variant}
              copyable={false}
            />
            <StatusBadge
              label={`${t('Resets left:')} ${props.credit.resets_left}`}
              variant={props.credit.resets_left > 0 ? 'blue' : 'neutral'}
              copyable={false}
            />
          </div>
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {t('Clears:')}{' '}
            {props.credit.clears.length > 0
              ? props.credit.clears.map((w) => windowName(w, t)).join(', ')
              : '-'}
          </div>
          {props.credit.use_requires_limit ? (
            <div className='text-muted-foreground mt-1 text-xs leading-5'>
              {t('Usable only while the account is at its limit.')}
            </div>
          ) : null}
          {props.credit.blocking.length > 0 ? (
            <div className='text-warning mt-1 text-xs leading-5'>
              {t('Blocked by:')} {props.credit.blocking.join(', ')}
            </div>
          ) : null}
        </div>
        <div className='shrink-0 text-right'>
          <div className='text-muted-foreground text-[11px] font-medium'>
            {t('Expires in')}
          </div>
          <div
            className={cn(
              'mt-1 text-sm font-semibold tabular-nums',
              props.credit.redeemable ? 'text-success' : 'text-muted-foreground'
            )}
          >
            {expiresIn}
          </div>
        </div>
      </div>
      <div className='mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3'>
        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] font-medium'>
            {t('Starts at')}
          </div>
          <div className='mt-1 text-xs leading-5 tabular-nums'>
            {formatUsageIsoTimestamp(props.credit.starts_at)}
          </div>
        </div>
        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] font-medium'>
            {t('Expires at')}
          </div>
          <div className='mt-1 text-xs leading-5 tabular-nums'>
            {formatUsageIsoTimestamp(props.credit.expires_at)}
          </div>
        </div>
        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] font-medium'>
            {t('Used')}
          </div>
          <div className='mt-1 text-xs leading-5 tabular-nums'>
            {percentEntries.length > 0
              ? percentEntries
                  .map(([w, v]) => `${windowName(w, t)} ${v}%`)
                  .join(' · ')
              : '-'}
          </div>
        </div>
      </div>
    </div>
  )
}

function ResetCreditsPanel(props: {
  view: ClaudeResetCreditsView | null
  response: ClaudeResetCreditsResponse | null
  isLoading: boolean
  isResetting: boolean
  /** The last reset did not complete; credits must be re-queried first */
  isStale: boolean
  errorMessage: string
  resetErrorMessage: string
  resetSuccessMessage: string
  onRefresh: () => void
  onRequestReset: () => void
}) {
  const { t } = useTranslation()
  const credits = props.view?.credits ?? []
  const availableCount = props.view ? String(props.view.available_count) : '-'
  const canReset = (props.view?.available_count ?? 0) > 0 && !props.isStale
  let eligibilityText = t('This account is not enrolled in reset credits.')
  if (props.view?.eligible && props.view.at_limit) {
    eligibilityText = t(
      'The account is at its limit; a redeemable credit can be applied now.'
    )
  } else if (props.view?.eligible) {
    eligibilityText = t(
      'The account is below its limit. Credits that require the limit stay disabled.'
    )
  }
  let creditsContent: ReactNode
  if (props.errorMessage) {
    creditsContent = (
      <div className='border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm'>
        {props.errorMessage}
      </div>
    )
  } else if (props.isLoading) {
    creditsContent = (
      <div className='flex flex-col gap-2'>
        <Skeleton className='h-24 w-full' />
        <Skeleton className='h-24 w-full' />
      </div>
    )
  } else if (credits.length > 0) {
    creditsContent = (
      <div className='flex flex-col gap-2'>
        {credits.map((credit, index) => (
          <ResetCreditItem
            key={[
              credit.label,
              credit.starts_at ?? '',
              credit.expires_at ?? '',
              credit.clears.join('+'),
            ].join('|')}
            credit={credit}
            index={index}
          />
        ))}
      </div>
    )
  } else {
    creditsContent = (
      <Empty className='min-h-32 border'>
        <EmptyHeader>
          <EmptyTitle>{t('No reset credits')}</EmptyTitle>
          <EmptyDescription>
            {t('Upstream did not return reset credit details.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  return (
    <div className='flex flex-col gap-3 p-3'>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-3'>
        <UsageInfoField
          label={t('Available reset credits')}
          value={availableCount}
          mono
          copyable={false}
        />
        <UsageInfoField
          label={t('Weekly resets at')}
          value={formatUsageIsoTimestamp(props.view?.weekly_resets_at)}
          mono
          copyable={false}
        />
        <UsageInfoField
          label={t('Cooldown until')}
          value={formatUsageIsoTimestamp(props.view?.cooldown_until)}
          mono
          copyable={false}
        />
      </div>

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='text-muted-foreground text-xs leading-5'>
          {eligibilityText}
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={props.onRefresh}
          disabled={props.isLoading}
        >
          <RefreshCw data-icon='inline-start' />
          {t('Refresh details')}
        </Button>
      </div>

      <div className='bg-muted/30 flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center sm:justify-between'>
        <div className='min-w-0'>
          <div className='text-sm font-semibold'>{t('Reset usage window')}</div>
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {t(
              'Use one available reset credit to clear the current Claude usage windows. The server re-checks eligibility and sends the request once.'
            )}
          </div>
        </div>
        <Button
          type='button'
          variant={canReset ? 'destructive' : 'outline'}
          size='sm'
          onClick={props.onRequestReset}
          disabled={!canReset || props.isLoading || props.isResetting}
          className='shrink-0'
        >
          <RotateCcw data-icon='inline-start' />
          {props.isResetting ? t('Resetting...') : t('Apply reset')}
        </Button>
      </div>

      {props.isStale ? (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Reset credits need a refresh')}</AlertTitle>
          <AlertDescription>
            {t(
              'The last reset request did not complete. Refresh the reset credits before applying another reset.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      {!canReset && !props.isStale ? (
        <Alert>
          <AlertTriangle />
          <AlertTitle>{t('No reset credits available')}</AlertTitle>
          <AlertDescription>
            {t('The reset request stays disabled until a credit is available.')}
          </AlertDescription>
        </Alert>
      ) : null}

      {props.resetSuccessMessage ? (
        <Alert className='border-success/40 bg-success/10 text-success'>
          <Check />
          <AlertTitle>{t('Reset completed')}</AlertTitle>
          <AlertDescription>{props.resetSuccessMessage}</AlertDescription>
        </Alert>
      ) : null}

      {props.resetErrorMessage ? (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Reset failed')}</AlertTitle>
          <AlertDescription>{props.resetErrorMessage}</AlertDescription>
        </Alert>
      ) : null}

      {creditsContent}
    </div>
  )
}

export function ClaudeUsageDialog(props: ClaudeUsageDialogProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const [showRawJson, setShowRawJson] = useState(false)
  const [showResetCredits, setShowResetCredits] = useState(false)
  const [resetCreditsResponse, setResetCreditsResponse] =
    useState<ClaudeResetCreditsResponse | null>(null)
  const [isLoadingResetCredits, setIsLoadingResetCredits] = useState(false)
  const [resetCreditsError, setResetCreditsError] = useState('')
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  const [isResetting, setIsResetting] = useState(false)
  const [resetActionError, setResetActionError] = useState('')
  const [resetActionMessage, setResetActionMessage] = useState('')
  // A failed reset (rejected, or interrupted with an unknown outcome) locks
  // the reset button until the credits are queried again successfully.
  const [resetCreditsStale, setResetCreditsStale] = useState(false)

  const response = props.response
  const channelId = props.channelId
  const windows = useMemo(
    () => parseClaudeUsageWindows(response?.data),
    [response?.data]
  )
  const resetCreditsView = useMemo(
    () => parseClaudeResetCredits(resetCreditsResponse?.data),
    [resetCreditsResponse?.data]
  )
  const availableCountText = resetCreditsView
    ? String(resetCreditsView.available_count)
    : '-'
  const canReset =
    (resetCreditsView?.available_count ?? 0) > 0 && !resetCreditsStale

  const channelLabelName = props.channelDisplayName ?? props.channelName ?? '-'
  let channelLabelId = ''
  if (props.channelDisplayId != null) {
    channelLabelId = ` (#${props.channelDisplayId})`
  } else if (channelId) {
    channelLabelId = ` (#${channelId})`
  }
  const channelLabel = `${channelLabelName}${channelLabelId}`

  const errorMessage =
    response?.success === false
      ? getServerMessage(response)?.trim() || t('Failed to fetch usage')
      : ''

  const loadResetCredits = useCallback(
    async (force = false) => {
      if (!channelId) {
        setResetCreditsError(t('Channel ID is required'))
        return
      }
      if (isLoadingResetCredits || (!force && resetCreditsResponse)) {
        return
      }

      setIsLoadingResetCredits(true)
      setResetCreditsError('')
      try {
        const res = await getClaudeResetCredits(channelId)
        if (!res.success) {
          throw createServerError(
            res,
            t('Failed to fetch reset credit details')
          )
        }
        setResetCreditsResponse(res)
        setResetCreditsStale(false)
      } catch (error) {
        setResetCreditsError(
          error instanceof Error
            ? error.message
            : t('Failed to fetch reset credit details')
        )
      } finally {
        setIsLoadingResetCredits(false)
      }
    },
    [channelId, isLoadingResetCredits, resetCreditsResponse, t]
  )

  const handleResetCreditsOpenChange = (nextOpen: boolean) => {
    setShowResetCredits(nextOpen)
    if (nextOpen) {
      void loadResetCredits(false)
    }
  }

  const handleDialogOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setShowRawJson(false)
      setShowResetCredits(false)
      setResetCreditsResponse(null)
      setResetCreditsError('')
      setIsLoadingResetCredits(false)
      setResetConfirmOpen(false)
      setIsResetting(false)
      setResetActionError('')
      setResetActionMessage('')
      setResetCreditsStale(false)
    }
    props.onOpenChange(nextOpen)
  }

  const handleConfirmReset = async () => {
    if (!channelId || isResetting || !canReset) {
      return
    }

    setIsResetting(true)
    setResetActionError('')
    setResetActionMessage('')
    try {
      const res = await resetClaudeUsage(channelId)
      if (!res.success) {
        throw createServerError(res, t('Failed to reset usage'))
      }
      const outcome = res.data as
        | { outcome?: string; cleared?: string[]; reason?: string }
        | undefined
      const outcomeKey =
        RESET_OUTCOME_KEYS[outcome?.outcome ?? ''] ?? RESET_OUTCOME_KEYS.unknown
      const cleared = Array.isArray(outcome?.cleared) ? outcome.cleared : []
      const clearedText =
        cleared.length > 0
          ? ` ${t('Cleared windows:')} ${cleared.map((w) => windowName(w, t)).join(', ')}`
          : ''
      const message = `${t(outcomeKey)}${clearedText}`
      if (outcome?.outcome === 'reset') {
        setResetActionMessage(message)
      } else {
        setResetActionError(message)
      }
      setResetConfirmOpen(false)
      await Promise.resolve(props.onRefresh?.())
      await loadResetCredits(true)
    } catch (error) {
      setResetCreditsStale(true)
      setResetConfirmOpen(false)
      setResetActionError(
        error instanceof Error ? error.message : t('Failed to reset usage')
      )
    } finally {
      setIsResetting(false)
    }
  }

  const rawJsonText = useMemo(() => {
    if (!response) {
      return ''
    }
    try {
      return JSON.stringify(
        {
          success: response.success,
          message: response.message,
          upstream_status: response.upstream_status,
          data: response.data,
        },
        null,
        2
      )
    } catch {
      return String(response?.data ?? '')
    }
  }, [response])

  const statusBadge = getUsageStatusBadge(windows, t)

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleDialogOpenChange}
      title={t('Claude Account & Usage')}
      contentClassName='sm:max-w-[900px]'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => handleDialogOpenChange(false)}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-col gap-4'>
        {errorMessage && (
          <div className='rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-400'>
            {errorMessage}
          </div>
        )}

        <Card size='sm' className='bg-muted/30 gap-0 py-0'>
          <CardHeader className='p-4 pb-2'>
            <CardTitle className='text-muted-foreground text-xs font-medium'>
              {t('Claude Account Status')}
            </CardTitle>
            {props.onRefresh ? (
              <CardAction>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={props.onRefresh}
                  disabled={Boolean(props.isRefreshing)}
                >
                  <RefreshCw data-icon='inline-start' />
                  {t('Refresh')}
                </Button>
              </CardAction>
            ) : null}
          </CardHeader>
          <CardContent className='p-4 pt-0'>
            <div className='flex flex-wrap items-center gap-2'>
              {statusBadge}
              <StatusBadge
                label={`HTTP ${response?.upstream_status ?? '-'}`}
                variant='neutral'
                copyable={false}
              />
              <StatusBadge
                label={`${t('Reset count:')} ${availableCountText}`}
                variant={canReset ? 'blue' : 'neutral'}
                copyable={false}
              />
            </div>
            <div className='mt-4 grid grid-cols-1 gap-3 md:grid-cols-2'>
              <UsageInfoField label={t('Email')} value={props.email} copyable />
              <UsageInfoField
                label={t('Channel')}
                value={channelLabel}
                copyable={false}
              />
            </div>
          </CardContent>
        </Card>

        <div className='flex flex-col gap-3'>
          <UsageSectionHeading
            title={t('Usage Windows')}
            description={`${t(
              'Utilization of the 5-hour and 7-day windows reported by the Claude subscription.'
            )} ${t(
              'The 7-Day Fable Window counts requests to Fable models only; a window marked "From response headers" was sampled from the latest relayed request because the usage API did not return it.'
            )}`}
          >
            {statusBadge}
          </UsageSectionHeading>
          <ClaudeUsageWindowGrid windows={windows} />
        </div>

        <Collapsible
          open={showResetCredits}
          onOpenChange={handleResetCreditsOpenChange}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-start justify-between gap-3 p-3 text-left transition-colors'
                aria-expanded={showResetCredits}
              />
            }
          >
            <div className='min-w-0'>
              <div className='flex flex-wrap items-center gap-2'>
                <div className='text-sm font-semibold'>
                  {t('Reset Credits')}
                </div>
                <StatusBadge
                  label={`${t('Available')} ${availableCountText}`}
                  variant={canReset ? 'blue' : 'neutral'}
                  copyable={false}
                />
              </div>
              <div className='text-muted-foreground mt-1 text-xs leading-5'>
                {t(
                  'View reset credits granted to this subscription and apply one.'
                )}
              </div>
            </div>
            {showResetCredits ? (
              <ChevronUp className='text-muted-foreground mt-0.5 shrink-0' />
            ) : (
              <ChevronDown className='text-muted-foreground mt-0.5 shrink-0' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent className='border-t'>
            <ResetCreditsPanel
              view={resetCreditsView}
              response={resetCreditsResponse}
              isLoading={isLoadingResetCredits}
              isResetting={isResetting}
              isStale={resetCreditsStale}
              errorMessage={resetCreditsError}
              resetErrorMessage={resetActionError}
              resetSuccessMessage={resetActionMessage}
              onRefresh={() => void loadResetCredits(true)}
              onRequestReset={() => {
                setResetActionError('')
                setResetActionMessage('')
                setResetConfirmOpen(true)
              }}
            />
          </CollapsibleContent>
        </Collapsible>

        <Collapsible
          open={showRawJson}
          onOpenChange={setShowRawJson}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-center justify-between gap-2 p-3 transition-colors'
                aria-expanded={showRawJson}
              />
            }
          >
            <div className='text-sm font-medium'>{t('Raw JSON')}</div>
            {showRawJson ? (
              <ChevronUp className='text-muted-foreground h-4 w-4' />
            ) : (
              <ChevronDown className='text-muted-foreground h-4 w-4' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <>
              <div className='flex justify-end border-t px-3 py-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => copyToClipboard(rawJsonText)}
                  disabled={!rawJsonText}
                >
                  {copiedText === rawJsonText ? (
                    <Check data-icon='inline-start' className='text-success' />
                  ) : (
                    <Copy data-icon='inline-start' />
                  )}
                  {t('Copy')}
                </Button>
              </div>
              <ScrollArea className='max-h-[50vh]'>
                <pre className='bg-muted/30 m-0 p-3 text-xs break-words whitespace-pre-wrap'>
                  {rawJsonText || '-'}
                </pre>
              </ScrollArea>
            </>
          </CollapsibleContent>
        </Collapsible>
      </div>
      <ConfirmDialog
        open={resetConfirmOpen}
        onOpenChange={setResetConfirmOpen}
        title={t('Confirm usage reset')}
        desc={
          <div className='flex flex-col gap-2'>
            <p>
              {t(
                'Use one available reset credit for this channel. The reset request is sent only after confirmation.'
              )}
            </p>
            <div className='bg-muted/50 rounded-lg border px-3 py-2 text-xs'>
              <div className='font-medium'>{channelLabel}</div>
              <div className='text-muted-foreground mt-1'>
                {t('Available reset credits')}: {availableCountText}
              </div>
            </div>
            <p className='text-destructive'>
              {t('Used reset credits cannot be restored.')}
            </p>
          </div>
        }
        destructive
        disabled={!canReset}
        isLoading={isResetting}
        cancelBtnText={t('Cancel')}
        confirmText={isResetting ? t('Resetting...') : t('Apply reset')}
        handleConfirm={handleConfirmReset}
      />
    </Dialog>
  )
}
