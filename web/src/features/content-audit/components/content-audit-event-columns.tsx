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

import { CopyButton } from '@/components/copy-button'
import { BadgeListCell, TruncatedCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Checkbox } from '@/components/ui/checkbox'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import type { ContentAuditEvent } from '../types'
import { ContentAuditDecisionBadge } from './content-audit-decision-badge'
import { ContentAuditEventRowActions } from './content-audit-event-row-actions'

export function useContentAuditEventColumns(): ColumnDef<ContentAuditEvent>[] {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return useMemo<ColumnDef<ContentAuditEvent>[]>(
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
        header: t('Decision'),
        size: 80,
        cell: ({ row }) => (
          <ContentAuditDecisionBadge decision={row.original.decision} />
        ),
        meta: { label: t('Decision'), mobileBadge: true },
      },
      {
        id: 'user',
        header: t('User'),
        size: 140,
        accessorFn: (event) => event.username,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-36'>
            {`${row.original.username || '—'} (${row.original.user_id})`}
          </TruncatedCell>
        ),
        meta: { label: t('User') },
      },
      {
        accessorKey: 'token_name',
        header: t('Token'),
        size: 120,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-32'>
            {row.original.token_name || '—'}
          </TruncatedCell>
        ),
        meta: { label: t('Token'), mobileHidden: true },
      },
      {
        accessorKey: 'using_group',
        header: t('Group'),
        size: 110,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-28'>
            {row.original.using_group || '—'}
          </TruncatedCell>
        ),
        meta: { label: t('Group') },
      },
      {
        accessorKey: 'model_name',
        header: t('Model'),
        size: 160,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-40 font-mono'>
            {row.original.model_name || '—'}
          </TruncatedCell>
        ),
        meta: { label: t('Model') },
      },
      {
        accessorKey: 'endpoint',
        header: t('Endpoint'),
        size: 180,
        cell: ({ row }) => (
          <div className='min-w-0'>
            <TruncatedCell className='max-w-44 font-mono'>
              {row.original.endpoint || '—'}
            </TruncatedCell>
            {row.original.relay_format && (
              <span className='text-muted-foreground block text-xs'>
                {row.original.relay_format}
              </span>
            )}
          </div>
        ),
        meta: { label: t('Endpoint'), mobileHidden: true },
      },
      {
        id: 'categories',
        header: t('Categories'),
        size: 180,
        accessorFn: (event) => (event.categories ?? []).join(', '),
        cell: ({ row }) => (
          <BadgeListCell
            items={(row.original.categories ?? []).map((category) => (
              <StatusBadge
                key={category}
                label={category}
                autoColor={category}
                copyable={false}
              />
            ))}
          />
        ),
        meta: { label: t('Categories') },
      },
      {
        accessorKey: 'prompt_hash',
        header: t('Prompt hash'),
        size: 140,
        cell: ({ row }) =>
          row.original.prompt_hash ? (
            <div className='flex items-center gap-1'>
              <span className='font-mono' title={row.original.prompt_hash}>
                {row.original.prompt_hash.slice(0, 12)}
              </span>
              <CopyButton
                value={row.original.prompt_hash}
                className='size-5'
                iconClassName='size-3'
                aria-label={t('Copy {{field}}', { field: t('Prompt hash') })}
              />
            </div>
          ) : (
            '—'
          ),
        meta: { label: t('Prompt hash'), mobileHidden: true },
      },
      {
        accessorKey: 'redacted_preview',
        header: t('Redacted preview'),
        size: 200,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-48 font-mono'>
            {row.original.redacted_preview || '—'}
          </TruncatedCell>
        ),
        meta: { label: t('Redacted preview') },
      },
      {
        accessorKey: 'latency_ms',
        header: t('Latency'),
        size: 90,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {t('{{value}} ms', {
              value: formatNumber(row.original.latency_ms, locale),
            })}
          </span>
        ),
        meta: { label: t('Latency'), mobileHidden: true },
      },
      {
        id: 'actions',
        header: t('Actions'),
        size: 110,
        enableHiding: false,
        cell: ({ row }) => <ContentAuditEventRowActions event={row.original} />,
        meta: { label: t('Actions') },
      },
    ],
    [locale, t]
  )
}
