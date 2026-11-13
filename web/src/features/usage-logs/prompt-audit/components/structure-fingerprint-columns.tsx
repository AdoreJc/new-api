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
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import type { PromptAuditStructureFingerprint } from '../api'
import {
  STRUCTURE_CLIENT_CLASS_LABELS,
  STRUCTURE_NO_SYSTEM,
  STRUCTURE_REQUEST_FORMAT_LABELS,
  formatStructureUserCount,
} from '../lib/structure-stats'

const FINGERPRINT_LENGTH = 12
const PREFIX_HASH_LENGTH = 8

function formatSeenAt(timestamp: number): string {
  if (!timestamp) return '-'
  return dayjs.unix(timestamp).format('YYYY-MM-DD HH:mm:ss')
}

export function useStructureFingerprintColumns(): ColumnDef<PromptAuditStructureFingerprint>[] {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return useMemo(
    () => [
      {
        accessorKey: 'fingerprint',
        header: t('Fingerprint'),
        size: 170,
        cell: ({ row }) =>
          row.original.fingerprint === STRUCTURE_NO_SYSTEM ? (
            <span className='text-muted-foreground'>
              {t('No system prompt')}
            </span>
          ) : (
            <span className='inline-flex items-center gap-1'>
              <span className='font-mono'>
                {row.original.fingerprint.slice(0, FINGERPRINT_LENGTH)}
              </span>
              <CopyButton
                value={row.original.fingerprint}
                className='size-5'
                iconClassName='size-3'
                aria-label={t('Copy {{field}}', { field: t('Fingerprint') })}
              />
            </span>
          ),
        meta: { label: t('Fingerprint'), mobileTitle: true },
      },
      {
        accessorKey: 'prefix_hash',
        header: t('Prefix hash'),
        size: 110,
        cell: ({ row }) =>
          row.original.prefix_hash === STRUCTURE_NO_SYSTEM ? (
            <span className='text-muted-foreground'>-</span>
          ) : (
            <span className='font-mono'>
              {row.original.prefix_hash.slice(0, PREFIX_HASH_LENGTH)}
            </span>
          ),
        meta: { label: t('Prefix hash') },
      },
      {
        accessorKey: 'user_count',
        header: t('Distinct users'),
        size: 120,
        cell: ({ row }) => (
          <span className='tabular-nums'>
            {formatStructureUserCount(
              row.original.user_count,
              row.original.user_count_capped,
              locale
            )}
          </span>
        ),
        meta: { label: t('Distinct users') },
      },
      {
        accessorKey: 'request_count',
        header: t('Requests'),
        size: 110,
        cell: ({ row }) => (
          <span className='tabular-nums'>
            {formatNumber(row.original.request_count, locale)}
          </span>
        ),
        meta: { label: t('Requests') },
      },
      {
        accessorKey: 'system_length',
        header: t('System prompt length'),
        size: 150,
        cell: ({ row }) => (
          <span className='tabular-nums'>
            {formatNumber(row.original.system_length, locale)}
          </span>
        ),
        meta: { label: t('System prompt length') },
      },
      {
        id: 'last_request_format',
        header: t('Last request format'),
        size: 160,
        accessorFn: (item) =>
          t(
            STRUCTURE_REQUEST_FORMAT_LABELS[item.last_request_format] ??
              item.last_request_format
          ),
        meta: { label: t('Last request format') },
      },
      {
        id: 'last_client_class',
        header: t('Last client'),
        size: 130,
        accessorFn: (item) =>
          t(
            STRUCTURE_CLIENT_CLASS_LABELS[item.last_client_class] ??
              item.last_client_class
          ),
        meta: { label: t('Last client') },
      },
      {
        accessorKey: 'first_seen_at',
        header: t('First seen'),
        size: 170,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {formatSeenAt(row.original.first_seen_at)}
          </span>
        ),
        meta: { label: t('First seen') },
      },
      {
        accessorKey: 'last_seen_at',
        header: t('Last seen'),
        size: 170,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {formatSeenAt(row.original.last_seen_at)}
          </span>
        ),
        meta: { label: t('Last seen') },
      },
    ],
    [t, locale]
  )
}
