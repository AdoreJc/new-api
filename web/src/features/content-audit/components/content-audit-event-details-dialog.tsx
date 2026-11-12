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

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { AuditDetailValue } from '@/features/usage-logs/audit/components/audit-detail-value'
import {
  DetailRow,
  DetailSection,
} from '@/features/usage-logs/components/dialogs/log-detail-layout'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import { getContentAuditEvent } from '../api'
import {
  CONTENT_AUDIT_QUERY_KEYS,
  contentAuditScannerLabelKey,
} from '../constants'
import type { ContentAuditEvent } from '../types'
import { ContentAuditDecisionBadge } from './content-audit-decision-badge'

export function ContentAuditEventDetailsDialog(props: {
  event: ContentAuditEvent
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const detail = useQuery({
    queryKey: CONTENT_AUDIT_QUERY_KEYS.event(props.event.id),
    queryFn: () => getContentAuditEvent(props.event.id),
    enabled: props.open,
  })
  const event = detail.data ?? props.event
  const num = (value: number) => formatNumber(value, locale)
  const list = (items: string[] | null | undefined) =>
    items?.length ? items.join(', ') : '—'
  const copyable = (label: string, value: string) =>
    value ? <AuditDetailValue label={label} value={value} mono copyable /> : '—'

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Content audit event')}
      description={t(
        'Prompt text is never stored. Only a redacted preview of up to 24 characters and a SHA-256 hash are kept.'
      )}
      contentClassName='min-w-0 sm:max-w-lg max-sm:max-h-(--dialog-available-height) max-sm:w-[calc(100vw-1.5rem)] max-sm:max-w-[calc(100vw-1.5rem)]'
      titleClassName='text-base'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      <div className='flex flex-wrap items-center gap-2 text-xs'>
        <ContentAuditDecisionBadge decision={event.decision} />
        <span className='text-muted-foreground tabular-nums'>
          {dayjs.unix(event.created_at).format('YYYY-MM-DD HH:mm:ss')}
        </span>
        <span className='text-muted-foreground font-mono'>#{event.id}</span>
      </div>

      <DetailSection label={t('Verdict')}>
        <DetailRow label={t('Safety')} value={event.safety || '—'} />
        <DetailRow label={t('Risk level')} value={event.risk_level || '—'} />
        <DetailRow label={t('Score')} value={num(event.score)} mono />
        <DetailRow label={t('Categories')} value={list(event.categories)} />
        <DetailRow
          label={t('Matched scanners')}
          value={list(
            event.matched_scanners?.map((id) =>
              t(contentAuditScannerLabelKey(id))
            )
          )}
        />
        <DetailRow
          label={t('Unknown categories')}
          value={list(event.unknown_categories)}
        />
        <DetailRow
          label={t('Guard endpoint')}
          value={event.guard_endpoint_id || '—'}
          mono
        />
        <DetailRow
          label={t('Scanner version')}
          value={event.scanner_version || '—'}
          mono
        />
        <DetailRow
          label={t('Config version')}
          value={num(event.config_version)}
          mono
        />
        <DetailRow label={t('Chunks')} value={num(event.chunk_total)} mono />
        <DetailRow
          label={t('Latency')}
          value={t('{{value}} ms', { value: num(event.latency_ms) })}
          mono
        />
      </DetailSection>

      <DetailSection label={t('Request')}>
        <DetailRow
          label={t('Request ID')}
          value={copyable(t('Request ID'), event.request_id)}
        />
        <DetailRow
          label={t('User')}
          value={`${event.username || '—'} (ID: ${event.user_id})`}
        />
        <DetailRow
          label={t('Token')}
          value={`${event.token_name || '—'} (ID: ${event.token_id})`}
        />
        <DetailRow label={t('Group')} value={event.using_group || '—'} />
        <DetailRow
          label={t('Channel ID')}
          value={event.channel_id || '—'}
          mono
        />
        <DetailRow label={t('Model')} value={event.model_name || '—'} mono />
        <DetailRow label={t('Endpoint')} value={event.endpoint || '—'} mono />
        <DetailRow
          label={t('Relay format')}
          value={event.relay_format || '—'}
          mono
        />
      </DetailSection>

      <DetailSection label={t('Prompt fingerprint')}>
        <DetailRow
          label={t('Prompt hash')}
          value={copyable(t('Prompt hash'), event.prompt_hash)}
        />
        <DetailRow
          label={t('Redacted preview')}
          value={event.redacted_preview || '—'}
          mono
        />
        <DetailRow
          label={t('Prompt length')}
          value={num(event.prompt_length)}
          mono
        />
        <DetailRow
          label={t('Messages')}
          value={num(event.message_count)}
          mono
        />
      </DetailSection>
      {detail.isError && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Showing the list values; the latest details could not be loaded.'
          )}{' '}
          <Button
            variant='link'
            size='sm'
            className='h-auto p-0 text-xs'
            onClick={() => void detail.refetch()}
          >
            {t('Retry')}
          </Button>
        </p>
      )}
    </Dialog>
  )
}
