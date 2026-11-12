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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { TruncatedCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { CONTENT_MODERATION_CATEGORY_LABELS } from '@/features/system-settings/security/content-moderation/lib/form'
import dayjs from '@/lib/dayjs'

import {
  CONTENT_MODERATION_ACTION_LABELS,
  type ContentModerationEvent,
} from '../api'
import { ContentModerationEventDialog } from './content-moderation-event-dialog'

export function useContentModerationColumns(
  onDelete: (event: ContentModerationEvent) => void
): ColumnDef<ContentModerationEvent>[] {
  const { t } = useTranslation()
  return useMemo(
    () => [
      {
        accessorKey: 'created_at',
        header: t('Time'),
        size: 170,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {dayjs.unix(row.original.created_at).format('YYYY-MM-DD HH:mm:ss')}
          </span>
        ),
        meta: { label: t('Time'), mobileTitle: true },
      },
      {
        accessorKey: 'action',
        header: t('Result'),
        size: 120,
        cell: ({ row }) => (
          <StatusBadge
            label={t(
              CONTENT_MODERATION_ACTION_LABELS[row.original.action] ??
                row.original.action
            )}
            variant={row.original.flagged ? 'danger' : 'neutral'}
            copyable={false}
          />
        ),
        meta: { label: t('Result') },
      },
      {
        id: 'category',
        header: t('Highest category'),
        size: 180,
        accessorFn: (event) =>
          event.highest_category
            ? `${t(CONTENT_MODERATION_CATEGORY_LABELS[event.highest_category] ?? event.highest_category)} · ${event.highest_score.toFixed(2)}`
            : '',
        cell: ({ getValue }) => (
          <TruncatedCell className='max-w-48'>
            {getValue<string>() || '-'}
          </TruncatedCell>
        ),
        meta: { label: t('Highest category') },
      },
      {
        accessorKey: 'username',
        header: t('User'),
        size: 110,
        meta: { label: t('User') },
      },
      {
        accessorKey: 'using_group',
        header: t('Group'),
        size: 110,
        meta: { label: t('Group') },
      },
      {
        accessorKey: 'model_name',
        header: t('Model'),
        size: 160,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-40'>
            {row.original.model_name}
          </TruncatedCell>
        ),
        meta: { label: t('Model') },
      },
      {
        accessorKey: 'input_excerpt',
        header: t('Redacted excerpt'),
        size: 200,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-56 font-mono'>
            {row.original.input_excerpt}
          </TruncatedCell>
        ),
        meta: { label: t('Redacted excerpt') },
      },
      {
        id: 'actions',
        header: t('Actions'),
        size: 140,
        cell: ({ row }) => (
          <div className='flex items-center gap-1'>
            <ContentModerationEventDialog event={row.original} />
            <Button
              variant='ghost'
              size='sm'
              className='text-destructive h-7 px-2'
              onClick={() => onDelete(row.original)}
            >
              {t('Delete')}
            </Button>
          </div>
        ),
        meta: { label: t('Actions') },
      },
    ],
    [t, onDelete]
  )
}
