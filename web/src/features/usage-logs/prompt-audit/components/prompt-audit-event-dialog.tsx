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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import dayjs from '@/lib/dayjs'

import {
  DetailRow,
  DetailSection,
} from '../../components/dialogs/log-detail-layout'
import { getPromptAuditEvent, type PromptAuditEvent } from '../api'
import {
  PROMPT_AUDIT_DECISION_LABELS,
  PROMPT_AUDIT_ROLE_LABELS,
  promptAuditCategories,
  promptAuditHitChunks,
} from '../lib/event-format'

export function PromptAuditEventDialog(props: { event: PromptAuditEvent }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [showFull, setShowFull] = useState(false)
  // Each full view is recorded in the audit log, so it loads only on request.
  const detail = useQuery({
    queryKey: ['prompt-audit', 'event', props.event.id],
    queryFn: () => getPromptAuditEvent(props.event.id),
    enabled: open && showFull,
    staleTime: Infinity,
    retry: false,
  })
  const event = props.event
  const categories = promptAuditCategories(event)
  const hitChunks = promptAuditHitChunks(event)
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) setShowFull(false)
      }}
      title={t('Prompt audit event')}
      description={t('View the complete details for this log entry')}
      descriptionClassName='sr-only'
      trigger={
        <Button variant='ghost' size='sm' className='h-7 px-2'>
          {t('Details')}
        </Button>
      }
      contentClassName='min-w-0 sm:max-w-2xl max-sm:max-h-(--dialog-available-height) max-sm:w-[calc(100vw-1.5rem)] max-sm:max-w-[calc(100vw-1.5rem)]'
      titleClassName='text-base'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      <div className='flex flex-wrap items-center gap-2 text-xs'>
        <StatusBadge
          label={t(
            PROMPT_AUDIT_DECISION_LABELS[event.decision] ?? event.decision
          )}
          variant={event.decision === 'critical' ? 'danger' : 'warning'}
          copyable={false}
        />
        <span className='text-muted-foreground tabular-nums'>
          {dayjs.unix(event.created_at).format('YYYY-MM-DD HH:mm:ss')}
        </span>
      </div>
      <DetailSection label={t('Findings')}>
        <DetailRow label={t('Guard verdict')} value={event.safety} mono />
        <DetailRow
          label={t('Risk categories')}
          value={
            categories.length === 0
              ? t('None')
              : categories
                  .map(
                    (category) =>
                      `${t(category.labelKey)}${category.labelKey === 'Unknown category' ? ` (${category.id})` : ''}${category.matched ? '' : ` · ${t('not selected')}`}`
                  )
                  .join(', ')
          }
        />
        <DetailRow
          label={t('Guard endpoint')}
          value={`${event.guard_endpoint_id} · ${event.scanner_version}`}
          mono
        />
        <DetailRow
          label={t('Scan')}
          value={t('{{scanned}} of {{chunks}} chunks scanned in {{ms}} ms', {
            scanned: event.chunks_scanned,
            chunks: event.chunk_total,
            ms: event.latency_ms,
          })}
        />
        {hitChunks.length > 0 && (
          <DetailRow
            label={t('Triggered by')}
            value={hitChunks
              .map((hit) =>
                t('Chunk {{index}} ({{roles}}): {{decision}}', {
                  index: hit.index,
                  roles: hit.roles
                    .map((role) => t(PROMPT_AUDIT_ROLE_LABELS[role] ?? role))
                    .join(', '),
                  decision: t(
                    PROMPT_AUDIT_DECISION_LABELS[hit.decision] ?? hit.decision
                  ),
                })
              )
              .join('; ')}
          />
        )}
        {event.scan_incomplete && (
          <p className='text-destructive text-xs'>
            {t(
              'Scan incomplete: a later chunk could not be scanned; these are the findings so far'
            )}
          </p>
        )}
      </DetailSection>
      <DetailSection label={t('Request')}>
        <DetailRow
          label={t('User')}
          value={`${event.username} (#${event.user_id})`}
        />
        <DetailRow
          label={t('API Key')}
          value={`${event.token_name} (#${event.token_id})`}
        />
        <DetailRow label={t('Group')} value={event.using_group} mono />
        <DetailRow label={t('Model')} value={event.model_name} mono />
        <DetailRow label={t('Endpoint')} value={event.endpoint} mono />
        <DetailRow label={t('Request ID')} value={event.request_id} mono />
        <DetailRow label={t('Prompt hash')} value={event.prompt_hash} mono />
        <DetailRow
          label={t('Prompt size')}
          value={t('{{chars}} characters in {{count}} messages', {
            chars: event.prompt_length,
            count: event.message_count,
          })}
        />
        <DetailRow label={t('Preview')} value={event.redacted_preview} mono />
      </DetailSection>
      <DetailSection label={t('Full prompt')}>
        {!showFull && (
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => setShowFull(true)}
          >
            {t('Show full prompt')}
          </Button>
        )}
        {showFull && detail.isPending && (
          <p className='text-muted-foreground text-xs'>{t('Loading...')}</p>
        )}
        {showFull && detail.isError && (
          <p role='alert' className='text-destructive text-xs'>
            {t('Failed to load the prompt audit event')}
          </p>
        )}
        {showFull && detail.data && !detail.data.full_prompt && (
          <p className='text-muted-foreground text-xs'>
            {t('The full prompt was not stored for this event')}
          </p>
        )}
        {showFull && detail.data?.full_prompt && (
          <div className='space-y-1'>
            <div className='flex justify-end'>
              <CopyButton value={detail.data.full_prompt} />
            </div>
            <pre className='bg-muted max-h-80 overflow-auto rounded-md p-3 text-xs break-words whitespace-pre-wrap'>
              {detail.data.full_prompt}
            </pre>
          </div>
        )}
      </DetailSection>
    </Dialog>
  )
}
