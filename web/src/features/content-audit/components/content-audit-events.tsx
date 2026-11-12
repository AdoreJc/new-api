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

import { DataTablePage, useDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import { getContentAuditEvents } from '../api'
import { CONTENT_AUDIT_QUERY_KEYS } from '../constants'
import type { ContentAuditEvent, ContentAuditEventFilters } from '../types'
import { ContentAuditEventBulkActions } from './content-audit-event-bulk-actions'
import { useContentAuditEventColumns } from './content-audit-event-columns'
import { ContentAuditEventFilterBar } from './content-audit-event-filter-bar'

const EMPTY_EVENTS: ContentAuditEvent[] = []

export function ContentAuditEvents() {
  const { t } = useTranslation()
  const [filters, setFilters] = useState<ContentAuditEventFilters>({
    p: 1,
    page_size: 20,
  })
  const invalidRange =
    filters.start_timestamp !== undefined &&
    filters.end_timestamp !== undefined &&
    filters.start_timestamp > filters.end_timestamp
  const query = useQuery({
    queryKey: [...CONTENT_AUDIT_QUERY_KEYS.events, filters],
    queryFn: () => getContentAuditEvents(filters),
    enabled: !invalidRange,
    retry: false,
  })
  const columns = useContentAuditEventColumns()
  const { table } = useDataTable({
    columns,
    data: query.isError ? EMPTY_EVENTS : (query.data?.items ?? EMPTY_EVENTS),
    getRowId: (event) => String(event.id),
    totalCount: query.isError ? 0 : (query.data?.total ?? 0),
    pagination: { pageIndex: filters.p - 1, pageSize: filters.page_size },
    onPaginationChange: (updater) => {
      if (query.isFetching || query.isError || invalidRange) return
      setFilters((previous) => {
        const current = {
          pageIndex: previous.p - 1,
          pageSize: previous.page_size,
        }
        const next = typeof updater === 'function' ? updater(current) : updater
        return {
          ...previous,
          p: next.pageSize === previous.page_size ? next.pageIndex + 1 : 1,
          page_size: next.pageSize,
        }
      })
    },
    enableRowSelection: true,
    enableSorting: false,
    manualFiltering: true,
    manualPagination: true,
  })
  const update = (patch: Partial<ContentAuditEventFilters>) =>
    setFilters((previous) => ({ ...previous, ...patch, p: 1 }))

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Content Audit Events')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col'>
          <DataTablePage
            table={table}
            columns={columns}
            isLoading={query.isPending && !invalidRange}
            isFetching={query.isFetching}
            emptyTitle={
              query.isError
                ? t('Failed to load content audit events')
                : t('No records')
            }
            emptyDescription={t(
              'Events appear here when content audit records a warn or block verdict.'
            )}
            className='h-auto min-h-0 flex-1'
            applyHeaderSize
            getColumnClassName={() => 'py-2'}
            bulkActions={<ContentAuditEventBulkActions table={table} />}
            toolbar={
              <div className='shrink-0 space-y-2'>
                <ContentAuditEventFilterBar
                  table={table}
                  filters={filters}
                  onChange={update}
                  isFetching={query.isFetching}
                  onSearch={() => {
                    if (!invalidRange) void query.refetch()
                  }}
                  onReset={() =>
                    setFilters({ p: 1, page_size: filters.page_size })
                  }
                />
                {invalidRange && (
                  <Alert variant='destructive'>
                    <AlertDescription>
                      {t('End time must be after start time')}
                    </AlertDescription>
                  </Alert>
                )}
                {query.isError && (
                  <Alert variant='destructive'>
                    <AlertDescription className='flex items-center justify-between gap-2'>
                      <span>{t('Failed to load content audit events')}</span>
                      <Button
                        size='sm'
                        variant='outline'
                        onClick={() => void query.refetch()}
                      >
                        {t('Retry')}
                      </Button>
                    </AlertDescription>
                  </Alert>
                )}
              </div>
            }
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
