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
import { ChevronDown, ChevronUp } from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StatusBadge } from '@/components/status-badge'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ClaudeResetCreditsPanel } from '@/features/channels/components/dialogs/claude-usage-dialog'
import {
  CodexResetCreditsPanel,
  type CodexResetCreditsPayload,
} from '@/features/channels/components/dialogs/codex-usage-dialog'
import {
  describeClaudeResetOutcome,
  parseClaudeResetCredits,
} from '@/features/channels/lib/claude-usage'
import { createServerError } from '@/lib/server-error-message'

import {
  getSelfSubscriptionResetCredits,
  resetSelfSubscriptionUsage,
} from '../../api'
import type { SubscriptionResetCreditsResponse } from '../../types'

type SubscriptionResetCreditsSectionProps = {
  subscriptionId: number
  provider: string
  /** Re-reads the usage windows after a redemption */
  onReset: () => void | Promise<void>
}

/**
 * Reset credits of the account bound to the subscriber's own subscription,
 * shown only when the administrator enabled them for this user. It reuses the
 * channel usage dialogs' panels; a failed or interrupted redemption locks the
 * reset button until the credits are queried again.
 */
export function SubscriptionResetCreditsSection(
  props: SubscriptionResetCreditsSectionProps
) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [response, setResponse] =
    useState<SubscriptionResetCreditsResponse | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [errorMessage, setErrorMessage] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [isResetting, setIsResetting] = useState(false)
  const [resetError, setResetError] = useState('')
  const [resetMessage, setResetMessage] = useState('')
  const [isStale, setIsStale] = useState(false)

  const isClaude = props.provider === 'claude'
  const claudeView = useMemo(
    () => (isClaude ? parseClaudeResetCredits(response?.data) : null),
    [isClaude, response?.data]
  )
  const codexPayload = useMemo(
    () =>
      !isClaude && response?.data
        ? (response.data as CodexResetCreditsPayload)
        : null,
    [isClaude, response?.data]
  )
  const availableCount = isClaude
    ? claudeView?.available_count
    : codexPayload?.available_count
  const availableCountText = Number.isFinite(Number(availableCount))
    ? String(availableCount)
    : '-'
  const canReset = Number(availableCount) > 0 && !isStale

  const loadCredits = useCallback(async () => {
    setIsLoading(true)
    setErrorMessage('')
    try {
      const res = await getSelfSubscriptionResetCredits(props.subscriptionId)
      if (!res.success) {
        throw createServerError(res, t('Failed to fetch reset credit details'))
      }
      setResponse(res)
      setIsStale(false)
    } catch (error) {
      setErrorMessage(
        error instanceof Error
          ? error.message
          : t('Failed to fetch reset credit details')
      )
    } finally {
      setIsLoading(false)
    }
  }, [props.subscriptionId, t])

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen)
    if (nextOpen && !response && !isLoading) {
      void loadCredits()
    }
  }

  const requestReset = () => {
    setResetError('')
    setResetMessage('')
    setConfirmOpen(true)
  }

  const handleConfirmReset = async () => {
    if (isResetting || !canReset) {
      return
    }
    setIsResetting(true)
    setResetError('')
    setResetMessage('')
    try {
      const res = await resetSelfSubscriptionUsage(props.subscriptionId)
      if (!res.success) {
        throw createServerError(res, t('Failed to reset usage'))
      }
      if (isClaude) {
        const outcome = describeClaudeResetOutcome(res.data, t)
        if (outcome.succeeded) {
          setResetMessage(outcome.message)
        } else {
          setResetError(outcome.message)
        }
      } else {
        const windowsReset = Number(
          (res.data as { windows_reset?: number } | undefined)?.windows_reset
        )
        setResetMessage(
          Number.isFinite(windowsReset)
            ? `${t('Reset completed. Latest usage has been refreshed.')} ${t(
                'Affected windows:'
              )} ${windowsReset}`
            : t('Reset completed. Latest usage has been refreshed.')
        )
      }
      setConfirmOpen(false)
      await Promise.resolve(props.onReset())
      await loadCredits()
    } catch (error) {
      setIsStale(true)
      setConfirmOpen(false)
      setResetError(
        error instanceof Error ? error.message : t('Failed to reset usage')
      )
    } finally {
      setIsResetting(false)
    }
  }

  return (
    <>
      <Collapsible
        open={open}
        onOpenChange={handleOpenChange}
        className='rounded-lg border'
      >
        <CollapsibleTrigger
          render={
            <button
              type='button'
              className='hover:bg-muted/40 flex w-full items-start justify-between gap-3 p-3 text-left transition-colors'
              aria-expanded={open}
            />
          }
        >
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <div className='text-sm font-semibold'>{t('Reset Credits')}</div>
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
          {open ? (
            <ChevronUp className='text-muted-foreground mt-0.5 shrink-0' />
          ) : (
            <ChevronDown className='text-muted-foreground mt-0.5 shrink-0' />
          )}
        </CollapsibleTrigger>
        <CollapsibleContent className='border-t'>
          {isClaude ? (
            <ClaudeResetCreditsPanel
              view={claudeView}
              response={response}
              isLoading={isLoading}
              isResetting={isResetting}
              isStale={isStale}
              errorMessage={errorMessage}
              resetErrorMessage={resetError}
              resetSuccessMessage={resetMessage}
              onRefresh={() => void loadCredits()}
              onRequestReset={requestReset}
            />
          ) : (
            <CodexResetCreditsPanel
              payload={codexPayload}
              response={response}
              usageAvailableCount='-'
              isLoading={isLoading}
              isResetting={isResetting}
              isStale={isStale}
              errorMessage={errorMessage}
              resetErrorMessage={resetError}
              resetSuccessMessage={resetMessage}
              onRefresh={() => void loadCredits()}
              onRequestReset={requestReset}
            />
          )}
        </CollapsibleContent>
      </Collapsible>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Confirm usage reset')}
        desc={
          <div className='flex flex-col gap-2'>
            <p>
              {t(
                'Use one available reset credit for the account bound to this subscription. The reset request is sent only after confirmation.'
              )}
            </p>
            <div className='bg-muted/50 text-muted-foreground rounded-lg border px-3 py-2 text-xs'>
              {t('Available reset credits')}: {availableCountText}
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
    </>
  )
}
