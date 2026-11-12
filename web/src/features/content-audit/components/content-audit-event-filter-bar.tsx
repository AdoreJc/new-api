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
import type { Table } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Combobox } from '@/components/ui/combobox'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from '@/features/usage-logs/components/logs-filter-toolbar'

import type { ContentAuditEvent, ContentAuditEventFilters } from '../types'

type TextFilterKey = 'user_id' | 'group' | 'request_id' | 'prompt_hash'

export function ContentAuditEventFilterBar(props: {
  table: Table<ContentAuditEvent>
  filters: ContentAuditEventFilters
  onChange: (patch: Partial<ContentAuditEventFilters>) => void
  isFetching: boolean
  onSearch: () => void
  onReset: () => void
}) {
  const { t } = useTranslation()
  const textFilters: { key: TextFilterKey; label: string }[] = [
    { key: 'user_id', label: t('User ID') },
    { key: 'group', label: t('Group') },
    { key: 'request_id', label: t('Request ID') },
    { key: 'prompt_hash', label: t('Prompt hash') },
  ]
  const dateFilter = (
    <LogsFilterField wide>
      <CompactDateTimeRangePicker
        start={
          props.filters.start_timestamp === undefined
            ? undefined
            : new Date(props.filters.start_timestamp * 1000)
        }
        end={
          props.filters.end_timestamp === undefined
            ? undefined
            : new Date(props.filters.end_timestamp * 1000)
        }
        onChange={({ start, end }) =>
          props.onChange({
            start_timestamp: start
              ? Math.floor(start.getTime() / 1000)
              : undefined,
            end_timestamp: end ? Math.floor(end.getTime() / 1000) : undefined,
          })
        }
      />
    </LogsFilterField>
  )
  const decisionFilter = (
    <LogsFilterField>
      <Combobox
        options={[
          { value: 'all', label: t('All decisions') },
          { value: 'warn', label: t('Warn') },
          { value: 'block', label: t('Block') },
        ]}
        value={props.filters.decision ?? 'all'}
        onValueChange={(value) => {
          if (value === null) return
          props.onChange({
            decision: value === 'warn' || value === 'block' ? value : undefined,
          })
        }}
        aria-label={t('Decision')}
        className='w-full'
      />
    </LogsFilterField>
  )
  const advancedFilters = textFilters.map((filter) => (
    <LogsFilterField key={filter.key}>
      <LogsFilterInput
        aria-label={filter.label}
        placeholder={filter.label}
        value={props.filters[filter.key] ?? ''}
        onChange={(event) =>
          props.onChange({
            [filter.key]: event.target.value.trim() || undefined,
          })
        }
      />
    </LogsFilterField>
  ))
  const advancedCount = textFilters.filter(
    (filter) => props.filters[filter.key]
  ).length
  const filterCount = advancedCount + (props.filters.decision ? 1 : 0)
  const hasFilters =
    filterCount > 0 ||
    props.filters.start_timestamp !== undefined ||
    props.filters.end_timestamp !== undefined

  return (
    <LogsFilterToolbar
      table={props.table}
      primaryFilters={
        <>
          {dateFilter}
          {decisionFilter}
        </>
      }
      advancedFilters={advancedFilters}
      mobilePinnedFilters={dateFilter}
      mobileFilters={
        <>
          {decisionFilter}
          {advancedFilters}
        </>
      }
      mobileFilterCount={filterCount}
      advancedFilterCount={advancedCount}
      hasActiveFilters={hasFilters}
      hasAdvancedActiveFilters={advancedCount > 0}
      searchLoading={props.isFetching}
      onSearch={props.onSearch}
      onReset={props.onReset}
    />
  )
}
