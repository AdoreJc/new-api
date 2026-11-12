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
import { Checkbox } from '@/components/ui/checkbox'
import dayjs from '@/lib/dayjs'

import type { PromptAuditEvent } from '../api'
import {
  PROMPT_AUDIT_DECISION_LABELS,
  promptAuditCategories,
} from '../lib/event-format'
import { PromptAuditEventDialog } from './prompt-audit-event-dialog'

export function usePromptAuditColumns(
  onDelete: (event: PromptAuditEvent) => void
): ColumnDef<PromptAuditEvent>[] {
  const { t } = useTranslation()
  return useMemo(
    () => [
      {
        id: 'select',
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected()}
            indeterminate={table.getIsSomePageRowsSelected()}
            onCheckedChange={(value) =>
              table.toggleAllPageRowsSelected(!!value)
            }
            aria-label={t('Select all')}
            className='translate-y-[2px]'
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            onCheckedChange={(value) => row.toggleSelected(!!value)}
            aria-label={t('Select row')}
            className='translate-y-[2px]'
          />
        ),
        enableSorting: false,
        enableHiding: false,
        size: 40,
      },
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
        accessorKey: 'decision',
        header: t('Result'),
        size: 90,
        cell: ({ row }) => (
          <StatusBadge
            label={t(
              PROMPT_AUDIT_DECISION_LABELS[row.original.decision] ??
                row.original.decision
            )}
            variant={
              row.original.decision === 'critical' ? 'danger' : 'warning'
            }
            copyable={false}
          />
        ),
        meta: { label: t('Result') },
      },
      {
        id: 'categories',
        header: t('Risk categories'),
        size: 200,
        accessorFn: (event) =>
          promptAuditCategories(event)
            .map((category) => t(category.labelKey))
            .join(', '),
        cell: ({ getValue }) => (
          <TruncatedCell className='max-w-56'>
            {getValue<string>() || t('None')}
          </TruncatedCell>
        ),
        meta: { label: t('Risk categories') },
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
        accessorKey: 'redacted_preview',
        header: t('Preview'),
        size: 200,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-56 font-mono'>
            {row.original.redacted_preview}
          </TruncatedCell>
        ),
        meta: { label: t('Preview') },
      },
      {
        id: 'actions',
        header: t('Actions'),
        size: 140,
        cell: ({ row }) => (
          <div className='flex items-center gap-1'>
            <PromptAuditEventDialog event={row.original} />
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
