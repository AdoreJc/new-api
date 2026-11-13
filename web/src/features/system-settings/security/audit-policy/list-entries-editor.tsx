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
import { Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'

import type { AuditPolicyListEntry } from './api'

type ListEntriesEditorProps = {
  title: string
  description: string
  entries: AuditPolicyListEntry[]
  onChange: (entries: AuditPolicyListEntry[]) => void
  /** Only allowlist entries can be verified for rerouting. */
  showVerified: boolean
}

type EntryRow = { entry: AuditPolicyListEntry; index: number }

function toId(value: string): number {
  const parsed = Number.parseInt(value, 10)
  return Number.isFinite(parsed) ? parsed : 0
}

export function ListEntriesEditor(props: ListEntriesEditorProps) {
  const { t } = useTranslation()
  const update = (index: number, patch: Partial<AuditPolicyListEntry>) =>
    props.onChange(
      props.entries.map((entry, position) =>
        position === index ? { ...entry, ...patch } : entry
      )
    )
  const columns: StaticDataTableColumn<EntryRow>[] = [
    {
      id: 'user',
      header: t('User ID'),
      className: 'w-28',
      cell: (row) => (
        <Input
          type='number'
          min={1}
          className='h-8'
          aria-label={t('User ID')}
          aria-invalid={row.entry.user_id <= 0}
          value={row.entry.user_id > 0 ? String(row.entry.user_id) : ''}
          onChange={(event) =>
            update(row.index, { user_id: toId(event.target.value) })
          }
        />
      ),
    },
    {
      id: 'token',
      header: t('Token ID (0 = all tokens)'),
      className: 'w-36',
      cell: (row) => (
        <Input
          type='number'
          min={0}
          className='h-8'
          aria-label={t('Token ID (0 = all tokens)')}
          value={String(row.entry.token_id)}
          onChange={(event) =>
            update(row.index, {
              token_id: Math.max(0, toId(event.target.value)),
            })
          }
        />
      ),
    },
    ...(props.showVerified
      ? [
          {
            id: 'verified',
            header: t('Verified'),
            className: 'w-20',
            cell: (row: EntryRow) => (
              <Checkbox
                checked={row.entry.verified}
                aria-label={t('Verified')}
                onCheckedChange={(next) =>
                  update(row.index, { verified: next === true })
                }
              />
            ),
          },
        ]
      : []),
    {
      id: 'note',
      header: t('Note'),
      cell: (row) => (
        <Input
          className='h-8'
          maxLength={255}
          aria-label={t('Note')}
          value={row.entry.note}
          onChange={(event) => update(row.index, { note: event.target.value })}
        />
      ),
    },
    {
      id: 'remove',
      header: <span className='sr-only'>{t('Actions')}</span>,
      className: 'w-12',
      cell: (row) => (
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          aria-label={t('Remove entry')}
          onClick={() =>
            props.onChange(
              props.entries.filter((_, position) => position !== row.index)
            )
          }
        >
          <Trash2 />
        </Button>
      ),
    },
  ]
  return (
    <div className='flex flex-col gap-2'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0'>
          <h4 className='text-sm font-medium'>{props.title}</h4>
          <p className='text-muted-foreground text-xs'>{props.description}</p>
        </div>
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() =>
            props.onChange([
              ...props.entries,
              { user_id: 0, token_id: 0, verified: false, note: '' },
            ])
          }
        >
          <Plus data-icon='inline-start' />
          {t('Add entry')}
        </Button>
      </div>
      <StaticDataTable
        columns={columns}
        data={props.entries.map((entry, index) => ({ entry, index }))}
        emptyContent={t('No entries')}
      />
    </div>
  )
}
