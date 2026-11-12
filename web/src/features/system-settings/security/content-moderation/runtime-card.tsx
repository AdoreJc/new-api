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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import {
  clearContentModerationHashes,
  getContentModerationRuntime,
  RUNTIME_QUERY_KEY,
  type ContentModerationKeyStatus,
  type ContentModerationRuntime,
} from './api'

const RUNTIME_FIELDS: Array<{
  key: keyof ContentModerationRuntime
  label: string
}> = [
  { key: 'checked', label: 'Inputs checked' },
  { key: 'skipped', label: 'Out of scope' },
  { key: 'skipped_empty', label: 'Skipped (no user text)' },
  { key: 'skipped_too_large', label: 'Skipped (body too large)' },
  { key: 'sampled_out', label: 'Not sampled' },
  { key: 'no_keys', label: 'Skipped (no key)' },
  { key: 'passed', label: 'Passed' },
  { key: 'flagged', label: 'Flagged' },
  { key: 'blocked', label: 'Blocked' },
  { key: 'hash_blocked', label: 'Hash blocklist matches' },
  { key: 'failed', label: 'Engine failures (allowed)' },
  { key: 'timeouts', label: 'Timeouts' },
  { key: 'calls', label: 'API calls' },
  { key: 'enqueued', label: 'Queued for background check' },
  { key: 'dropped', label: 'Dropped (queue full)' },
  { key: 'queue_depth', label: 'Queued' },
  { key: 'auto_disabled', label: 'Users disabled' },
  { key: 'emails_sent', label: 'Notices sent' },
  { key: 'emails_failed', label: 'Notices failed' },
  { key: 'recorded', label: 'Events recorded' },
  { key: 'record_failed', label: 'Events not recorded' },
  {
    key: 'disposal_dropped',
    label: 'Flagged events lost before counting',
  },
  { key: 'hash_blocklist', label: 'Hashes in blocklist' },
  { key: 'sync_avg_ms', label: 'Average added latency (ms)' },
  { key: 'latency_avg_ms', label: 'Average API time (ms)' },
  { key: 'latency_max_ms', label: 'Longest API time (ms)' },
]

const KEY_STATUS: Record<
  ContentModerationKeyStatus['status'],
  { label: string; variant: 'success' | 'warning' | 'danger' | 'neutral' }
> = {
  untested: { label: 'Not used yet', variant: 'neutral' },
  healthy: { label: 'Healthy', variant: 'success' },
  failing: { label: 'Failing', variant: 'warning' },
  frozen: { label: 'Paused', variant: 'danger' },
}

export function ContentModerationRuntimeCard() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const [confirmClear, setConfirmClear] = useState(false)
  const runtime = useQuery({
    queryKey: RUNTIME_QUERY_KEY,
    queryFn: getContentModerationRuntime,
    refetchInterval: 10000,
  })
  const clear = useMutation({
    mutationFn: clearContentModerationHashes,
    onSuccess: (data) => {
      toast.success(
        t('Removed {{count}} hashes from the blocklist', {
          count: data.deleted,
        })
      )
      setConfirmClear(false)
      void queryClient.invalidateQueries({ queryKey: RUNTIME_QUERY_KEY })
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <div className='flex flex-col gap-3 rounded-lg border p-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h4 className='text-sm font-medium'>{t('Runtime status')}</h4>
        <div className='flex flex-wrap gap-2'>
          <Button
            type='button'
            size='sm'
            variant='outline'
            className='text-destructive'
            onClick={() => setConfirmClear(true)}
          >
            {t('Clear hash blocklist')}
          </Button>
          <Button
            type='button'
            size='sm'
            variant='ghost'
            disabled={runtime.isFetching}
            onClick={() => void runtime.refetch()}
          >
            {t('Refresh')}
          </Button>
        </div>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Counters reset when the server restarts. Background checks are queued in memory: pending items are lost on restart and new items are dropped while the queue is full.'
        )}
      </p>
      {runtime.data && (
        <>
          <dl className='grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-3'>
            {RUNTIME_FIELDS.map((field) => (
              <div key={field.key} className='flex min-w-0 flex-col'>
                <dt className='text-muted-foreground truncate text-xs'>
                  {t(field.label)}
                </dt>
                <dd className='font-mono tabular-nums'>
                  {formatNumber(Number(runtime.data[field.key]), locale)}
                </dd>
              </div>
            ))}
          </dl>
          {runtime.data.keys.length > 0 && (
            <ul className='flex flex-col gap-2 text-sm'>
              {runtime.data.keys.map((key) => (
                <li key={key.id} className='flex flex-wrap items-center gap-2'>
                  <span className='font-mono'>{key.id}</span>
                  <StatusBadge
                    label={t(KEY_STATUS[key.status].label)}
                    variant={KEY_STATUS[key.status].variant}
                    copyable={false}
                  />
                  <span className='text-muted-foreground text-xs tabular-nums'>
                    {t('{{ok}} ok · {{failed}} failed', {
                      ok: formatNumber(key.successes, locale),
                      failed: formatNumber(key.failures, locale),
                    })}
                    {key.frozen_until
                      ? ` · ${t('paused until {{time}}', {
                          time: dayjs.unix(key.frozen_until).format('HH:mm:ss'),
                        })}`
                      : ''}
                    {key.last_error ? ` · ${key.last_error}` : ''}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <ConfirmDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title={t('Clear hash blocklist')}
        desc={t(
          'Every remembered input will be checked by the moderation API again. This cannot be undone.'
        )}
        destructive
        confirmText={t('Clear')}
        isLoading={clear.isPending}
        handleConfirm={() => clear.mutate()}
      />
    </div>
  )
}
