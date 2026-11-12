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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { getPromptAuditRuntime, type PromptAuditRuntime } from './api'

const RUNTIME_FIELDS: Array<{ key: keyof PromptAuditRuntime; label: string }> =
  [
    { key: 'queue_depth', label: 'Queued' },
    { key: 'active_workers', label: 'Scanning' },
    { key: 'enqueued', label: 'Enqueued' },
    { key: 'dropped', label: 'Dropped (queue full)' },
    { key: 'skipped', label: 'Out of scope' },
    { key: 'skipped_too_large', label: 'Skipped (body too large)' },
    { key: 'skipped_no_text', label: 'Skipped (no text)' },
    {
      key: 'skipped_agent_context_only',
      label: 'Skipped (agent context only)',
    },
    { key: 'passed', label: 'Passed' },
    { key: 'flagged', label: 'Flagged' },
    { key: 'critical', label: 'Critical' },
    { key: 'failed', label: 'Guard failures' },
    { key: 'timeouts', label: 'Timeouts' },
    { key: 'failovers', label: 'Failovers' },
    { key: 'recorded', label: 'Events recorded' },
    { key: 'record_failed', label: 'Events not recorded' },
    { key: 'chunks_cached', label: 'Chunks skipped (recently safe)' },
    { key: 'chunks_over_budget', label: 'Chunks over the per-request limit' },
    { key: 'latency_avg_ms', label: 'Average scan time (ms)' },
    { key: 'latency_max_ms', label: 'Longest scan time (ms)' },
  ]

export function PromptAuditRuntimeCard() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const runtime = useQuery({
    queryKey: ['prompt-audit', 'runtime'],
    queryFn: getPromptAuditRuntime,
    refetchInterval: 10000,
  })
  return (
    <div className='flex flex-col gap-3 rounded-lg border p-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h4 className='text-sm font-medium'>{t('Runtime status')}</h4>
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
      <p className='text-muted-foreground text-xs'>
        {t(
          'Counters reset when the server restarts. The queue is kept in memory: pending items are lost on restart and new items are dropped while it is full.'
        )}
      </p>
      {runtime.data && (
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
      )}
    </div>
  )
}
