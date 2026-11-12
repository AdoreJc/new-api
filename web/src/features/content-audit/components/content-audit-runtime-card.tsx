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
import { TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { SettingsCard } from '@/features/system-settings/components/settings-card'
import {
  DetailRow,
  DetailSection,
} from '@/features/usage-logs/components/dialogs/log-detail-layout'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import { getContentAuditRuntime } from '../api'
import {
  CONTENT_AUDIT_QUERY_KEYS,
  CONTENT_AUDIT_RUNTIME_POLL_MS,
} from '../constants'
import type { ContentAuditCounters, ContentAuditLatency } from '../types'

const COUNTER_LABEL_KEYS: Record<keyof ContentAuditCounters, string> = {
  enqueued: 'Enqueued',
  dropped_queue_full: 'Dropped (queue full)',
  dropped_queue_bytes: 'Dropped (queue memory limit)',
  skipped_body_too_large: 'Skipped (body too large)',
  skipped_unsupported_format: 'Skipped (unsupported format)',
  skipped_invalid_json: 'Skipped (invalid JSON)',
  skipped_no_text: 'Skipped (no text)',
  skipped_out_of_scope: 'Skipped (out of scope)',
  scanned: 'Scanned',
  allowed: 'Allowed',
  warned: 'Warned',
  blocked: 'Would block',
  failed: 'Scan failures',
  retries: 'Retries',
  failovers: 'Failovers',
  credential_pattern_hits: 'Credential pattern hits',
  events_written: 'Events written',
  event_write_failed: 'Event write failures',
}

const LATENCY_KEYS = ['p50', 'p95', 'p99', 'max'] as const

export function ContentAuditRuntimeCard() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const runtime = useQuery({
    queryKey: CONTENT_AUDIT_QUERY_KEYS.runtime,
    queryFn: getContentAuditRuntime,
    // Polling pauses while the tab is hidden (React Query default).
    refetchInterval: CONTENT_AUDIT_RUNTIME_POLL_MS,
  })
  const num = (value: number | null | undefined) => formatNumber(value, locale)
  const latencyRows = (latency: ContentAuditLatency, unit: string) =>
    LATENCY_KEYS.map((key) => (
      <DetailRow
        key={key}
        label={key.toUpperCase()}
        value={`${num(latency?.[key])} ${unit}`}
        mono
      />
    ))

  let body
  if (runtime.isPending) {
    body = <LoadingState size='sm' message={t('Loading runtime status...')} />
  } else if (runtime.isError) {
    body = (
      <ErrorState
        title={t('Failed to load runtime status')}
        onRetry={() => void runtime.refetch()}
      />
    )
  } else {
    const data = runtime.data
    body = (
      <div className='grid min-w-0 gap-4 lg:grid-cols-2'>
        {data.load_error && (
          <Alert variant='destructive' className='lg:col-span-2'>
            <TriangleAlert aria-hidden='true' />
            <AlertDescription className='break-words'>
              {t(
                'The stored content audit configuration failed to load, so auditing is off: {{error}}',
                { error: data.load_error }
              )}
            </AlertDescription>
          </Alert>
        )}
        <DetailSection label={t('Workers and queue')}>
          <DetailRow
            label={t('Status')}
            value={
              <StatusBadge
                label={data.enabled ? t('Enabled') : t('Disabled')}
                variant={data.enabled ? 'success' : 'neutral'}
                copyable={false}
              />
            }
          />
          <DetailRow label={t('Workers')} value={num(data.workers)} mono />
          <DetailRow
            label={t('Queue depth')}
            value={`${num(data.queue_depth)} / ${num(data.queue_capacity)}`}
            mono
          />
          <DetailRow
            label={t('Queue memory')}
            value={`${num(data.queue_bytes)} / ${num(data.queue_max_bytes)} B`}
            mono
          />
          <DetailRow
            label={t('Config version')}
            value={num(data.config_version)}
            mono
          />
          <DetailRow
            label={t('Started at')}
            value={
              data.started_at
                ? dayjs.unix(data.started_at).format('YYYY-MM-DD HH:mm:ss')
                : '—'
            }
            mono
          />
        </DetailSection>
        <DetailSection label={t('Counters')}>
          {(
            Object.keys(COUNTER_LABEL_KEYS) as (keyof ContentAuditCounters)[]
          ).map((key) => (
            <DetailRow
              key={key}
              label={t(COUNTER_LABEL_KEYS[key])}
              value={num(data.counters?.[key])}
              mono
            />
          ))}
        </DetailSection>
        <DetailSection label={t('Guard latency (ms)')}>
          {latencyRows(data.guard_latency_ms, 'ms')}
          <DetailRow
            label={t('Samples')}
            value={num(data.guard_latency_ms?.samples)}
            mono
          />
        </DetailSection>
        <DetailSection label={t('Request path overhead (µs)')}>
          {latencyRows(data.entry_latency_us, 'µs')}
          <DetailRow
            label={t('Samples')}
            value={num(data.entry_latency_us?.samples)}
            mono
          />
        </DetailSection>
      </div>
    )
  }

  return (
    <SettingsCard
      title={t('Runtime status')}
      description={t('Refreshes every 10 seconds while this page is open.')}
    >
      {body}
    </SettingsCard>
  )
}
