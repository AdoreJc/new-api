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
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { CONTENT_MODERATION_CATEGORY_LABELS } from '@/features/system-settings/security/content-moderation/lib/form'
import dayjs from '@/lib/dayjs'
import { handleServerError } from '@/lib/handle-server-error'

import {
  DetailRow,
  DetailSection,
} from '../../components/dialogs/log-detail-layout'
import {
  CONTENT_MODERATION_ACTION_LABELS,
  deleteContentModerationHash,
  parseScores,
  updateContentModerationAllowlist,
  type ContentModerationEvent,
} from '../api'

function categoryLabel(category: string): string {
  return CONTENT_MODERATION_CATEGORY_LABELS[category] ?? category
}

export function ContentModerationEventDialog(props: {
  event: ContentModerationEvent
}) {
  const { t } = useTranslation()
  const event = props.event
  const thresholds = new Map(parseScores(event.thresholds))
  const scores = parseScores(event.category_scores)
  const allowlist = useMutation({
    mutationFn: () =>
      updateContentModerationAllowlist(
        event.user_id,
        event.allowlisted ? 'remove' : 'add'
      ),
    onSuccess: () =>
      toast.success(
        event.allowlisted
          ? t('User removed from the allowlist')
          : t('User added to the allowlist')
      ),
    onError: (error) => handleServerError(error),
  })
  const forgetHash = useMutation({
    mutationFn: () => deleteContentModerationHash(event.input_hash),
    onSuccess: (data) =>
      toast.success(
        data.deleted > 0
          ? t('Input removed from the hash blocklist')
          : t('This input was not in the hash blocklist')
      ),
    onError: (error) => handleServerError(error),
  })
  return (
    <Dialog
      title={t('Content moderation event')}
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
            CONTENT_MODERATION_ACTION_LABELS[event.action] ?? event.action
          )}
          variant={event.flagged ? 'danger' : 'neutral'}
          copyable={false}
        />
        {event.allowlisted && (
          <StatusBadge
            label={t('Allowlisted')}
            variant='info'
            copyable={false}
          />
        )}
        <span className='text-muted-foreground tabular-nums'>
          {dayjs.unix(event.created_at).format('YYYY-MM-DD HH:mm:ss')}
        </span>
      </div>
      <DetailSection label={t('Result')}>
        <DetailRow
          label={t('Highest category')}
          value={
            event.highest_category
              ? `${t(categoryLabel(event.highest_category))} · ${event.highest_score.toFixed(3)}`
              : '-'
          }
        />
        {event.rule_id && (
          <DetailRow
            label={t('Audit policy rule')}
            value={event.rule_id}
            mono
          />
        )}
        {event.error && <DetailRow label={t('Error')} value={event.error} />}
        <DetailRow
          label={t('Call')}
          value={t('{{ms}} ms, waited {{delay}} ms in queue', {
            ms: event.latency_ms,
            delay: event.queue_delay_ms,
          })}
        />
        {event.engine_model && (
          <DetailRow
            label={t('Engine model')}
            value={event.engine_model}
            mono
          />
        )}
        {scores.length > 0 && (
          <ul aria-label={t('Category scores')} className='space-y-1 text-xs'>
            {scores.map(([category, score]) => {
              const threshold = thresholds.get(category)
              const over = threshold !== undefined && score >= threshold
              return (
                <li
                  key={category}
                  className={over ? 'text-destructive font-medium' : ''}
                >
                  {t(categoryLabel(category))}: {score.toFixed(3)}
                  {threshold !== undefined && ` / ${threshold.toFixed(2)}`}
                </li>
              )
            })}
          </ul>
        )}
      </DetailSection>
      <DetailSection label={t('Disposal')}>
        <DetailRow
          label={t('Flags in the counting window')}
          value={String(event.violation_count)}
        />
        <DetailRow
          label={t('User disabled')}
          value={event.auto_disabled ? t('Yes') : t('No')}
        />
        <DetailRow
          label={t('Notice emailed')}
          value={event.email_sent ? t('Yes') : t('No')}
        />
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
        <DetailRow label={t('Input hash')} value={event.input_hash} mono />
        {event.keyword_list && (
          <DetailRow
            label={t('Matched keyword list')}
            value={event.keyword_list}
            mono
          />
        )}
        <DetailRow
          label={t('Input size')}
          value={t('{{chars}} characters, {{images}} images', {
            chars: event.input_length,
            images: event.image_count,
          })}
        />
        {event.input_excerpt && (
          <DetailRow
            label={t('Redacted excerpt')}
            value={event.input_excerpt}
            mono
          />
        )}
      </DetailSection>
      <div className='flex flex-wrap gap-2'>
        {event.user_id > 0 && (
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={allowlist.isPending || allowlist.isSuccess}
            onClick={() => allowlist.mutate()}
          >
            {event.allowlisted
              ? t('Remove user from allowlist')
              : t('Add user to allowlist')}
          </Button>
        )}
        {event.flagged && event.input_hash && (
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={forgetHash.isPending || forgetHash.isSuccess}
            onClick={() => forgetHash.mutate()}
          >
            {t('Remove input from hash blocklist')}
          </Button>
        )}
      </div>
    </Dialog>
  )
}
