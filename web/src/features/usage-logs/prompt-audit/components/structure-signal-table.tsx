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
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Combobox } from '@/components/ui/combobox'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import { CompactDateTimeRangePicker } from '../../components/compact-date-time-range-picker'
import { LogsFilterField } from '../../components/logs-filter-toolbar'
import {
  getPromptAuditStructureSignals,
  type PromptAuditStructureSignalDay,
} from '../api'
import {
  STRUCTURE_CLIENT_CLASS_LABELS,
  STRUCTURE_REQUEST_FORMAT_LABELS,
  STRUCTURE_SIGNAL_COLUMNS,
  formatStructureSignalRate,
  formatStructureStatDay,
  toStructureStatDay,
} from '../lib/structure-stats'

const ALL = 'all'
const DEFAULT_RANGE_DAYS = 30

type SignalFilters = {
  start: Date
  end: Date
  requestFormat: string
  clientClass: string
}

function defaultFilters(): SignalFilters {
  const now = dayjs()
  return {
    start: now
      .subtract(DEFAULT_RANGE_DAYS - 1, 'day')
      .startOf('day')
      .toDate(),
    end: now.endOf('day').toDate(),
    requestFormat: ALL,
    clientClass: ALL,
  }
}

/** Structure signals per day: each request counter with its share of the day. */
export function StructureSignalTable() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [filters, setFilters] = useState(defaultFilters)
  const request = {
    start_day: toStructureStatDay(filters.start),
    end_day: toStructureStatDay(filters.end),
    request_format:
      filters.requestFormat === ALL ? undefined : filters.requestFormat,
    client_class: filters.clientClass === ALL ? undefined : filters.clientClass,
  }
  const query = useQuery({
    queryKey: ['prompt-audit', 'structure', 'signals', request],
    queryFn: () => getPromptAuditStructureSignals(request),
    placeholderData: keepPreviousData,
    retry: false,
  })

  const options = (
    values: string[] | undefined,
    labels: Record<string, string>,
    allLabel: string
  ) => [
    { value: ALL, label: allLabel },
    ...(values ?? []).map((value) => ({
      value,
      label: t(labels[value] ?? value),
    })),
  ]
  const columns: StaticDataTableColumn<PromptAuditStructureSignalDay>[] = [
    {
      id: 'stat_day',
      header: t('Date'),
      cellClassName: 'font-mono tabular-nums',
      cell: (day) => formatStructureStatDay(day.stat_day),
    },
    {
      id: 'requests',
      header: t('Requests'),
      cellClassName: 'tabular-nums',
      cell: (day) => formatNumber(day.requests, locale),
    },
    ...STRUCTURE_SIGNAL_COLUMNS.map((column) => ({
      id: column.key,
      header: t(column.label),
      cellClassName: 'tabular-nums',
      cell: (day: PromptAuditStructureSignalDay) =>
        column.rate
          ? formatStructureSignalRate(day[column.key], day.requests, locale)
          : formatNumber(day[column.key], locale),
    })),
  ]

  return (
    <div className='space-y-2.5 sm:space-y-3'>
      <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-4'>
        <LogsFilterField wide>
          <CompactDateTimeRangePicker
            start={filters.start}
            end={filters.end}
            onChange={(range) => {
              // The API needs both days; a cleared side returns to the default.
              const fallback = defaultFilters()
              setFilters((previous) => ({
                ...previous,
                start: range.start ?? fallback.start,
                end: range.end ?? fallback.end,
              }))
            }}
          />
        </LogsFilterField>
        <LogsFilterField>
          <Combobox
            options={options(
              query.data?.request_formats,
              STRUCTURE_REQUEST_FORMAT_LABELS,
              t('All request formats')
            )}
            value={filters.requestFormat}
            onValueChange={(value) => {
              if (value === null) return
              setFilters((previous) => ({ ...previous, requestFormat: value }))
            }}
            aria-label={t('Request format')}
            className='w-full'
          />
        </LogsFilterField>
        <LogsFilterField>
          <Combobox
            options={options(
              query.data?.client_classes,
              STRUCTURE_CLIENT_CLASS_LABELS,
              t('All clients')
            )}
            value={filters.clientClass}
            onValueChange={(value) => {
              if (value === null) return
              setFilters((previous) => ({ ...previous, clientClass: value }))
            }}
            aria-label={t('Client')}
            className='w-full'
          />
        </LogsFilterField>
      </div>
      <SignalRows
        columns={columns}
        items={query.data?.items}
        isError={query.isError}
        onRetry={() => void query.refetch()}
      />
    </div>
  )
}

function SignalRows(props: {
  columns: StaticDataTableColumn<PromptAuditStructureSignalDay>[]
  items: PromptAuditStructureSignalDay[] | undefined
  isError: boolean
  onRetry: () => void
}) {
  const { t } = useTranslation()
  if (props.isError) {
    return (
      <ErrorState
        title={t('Failed to load structure statistics')}
        onRetry={props.onRetry}
        className='min-h-[200px]'
      />
    )
  }
  if (props.items === undefined) return <LoadingState />
  if (props.items.length === 0) {
    return (
      <EmptyState
        title={t('No structure statistics in this range')}
        className='min-h-[200px]'
        bordered
      />
    )
  }
  return (
    <StaticDataTable
      columns={props.columns}
      data={props.items}
      getRowKey={(day) => day.stat_day}
      tableClassName='whitespace-nowrap'
    />
  )
}
