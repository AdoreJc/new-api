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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTablePage, useDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { handleServerError } from '@/lib/handle-server-error'

import { CompactDateTimeRangePicker } from '../components/compact-date-time-range-picker'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from '../components/logs-filter-toolbar'
import {
  deleteContentModerationEvent,
  deleteContentModerationEventsByFilter,
  getContentModerationEvents,
  previewContentModerationDeletion,
  type ContentModerationEvent,
  type ContentModerationEventFilter,
} from './api'
import { useContentModerationColumns } from './components/content-moderation-columns'

const EMPTY_EVENTS: ContentModerationEvent[] = []

type PendingDeletion =
  | { kind: 'one'; event: ContentModerationEvent }
  | { kind: 'filter'; total: number; maxId: number }

export function ContentModerationEvents() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<ContentModerationEventFilter>({})
  const [filter, setFilter] = useState<ContentModerationEventFilter>({})
  const [page, setPage] = useState({ pageIndex: 0, pageSize: 20 })
  const [pending, setPending] = useState<PendingDeletion | null>(null)

  const query = useQuery({
    queryKey: ['content-moderation', 'events', filter, page],
    queryFn: () =>
      getContentModerationEvents(filter, page.pageIndex + 1, page.pageSize),
    retry: false,
  })
  const onDelete = useCallback(
    (event: ContentModerationEvent) => setPending({ kind: 'one', event }),
    []
  )
  const columns = useContentModerationColumns(onDelete)
  const { table } = useDataTable({
    columns,
    data: query.data?.items ?? EMPTY_EVENTS,
    getRowId: (event) => String(event.id),
    totalCount: query.data?.total ?? 0,
    pagination: page,
    onPaginationChange: (updater) =>
      setPage((previous) =>
        typeof updater === 'function' ? updater(previous) : updater
      ),
    enableRowSelection: false,
    enableSorting: false,
    manualFiltering: true,
    manualPagination: true,
  })

  const preview = useMutation({
    mutationFn: () => previewContentModerationDeletion(filter),
    onSuccess: (data) => {
      if (data.total === 0) {
        toast.info(t('No events match the current filters'))
        return
      }
      setPending({ kind: 'filter', total: data.total, maxId: data.max_id })
    },
    onError: (error) => handleServerError(error),
  })
  const remove = useMutation({
    mutationFn: (deletion: PendingDeletion) =>
      deletion.kind === 'one'
        ? deleteContentModerationEvent(deletion.event.id)
        : deleteContentModerationEventsByFilter(filter, deletion.maxId),
    onSuccess: (data) => {
      toast.success(t('Deleted {{count}} events', { count: data.deleted }))
      setPending(null)
      void queryClient.invalidateQueries({
        queryKey: ['content-moderation', 'events'],
      })
    },
    onError: (error) => handleServerError(error),
  })

  const apply = (next: ContentModerationEventFilter) => {
    setFilter(next)
    setPage((previous) => ({ ...previous, pageIndex: 0 }))
  }
  const textFilter = (
    key: 'keyword' | 'group' | 'request_id',
    label: string
  ) => (
    <LogsFilterField>
      <LogsFilterInput
        aria-label={label}
        placeholder={label}
        value={draft[key] ?? ''}
        onChange={(event) =>
          setDraft((previous) => ({
            ...previous,
            [key]: event.target.value || undefined,
          }))
        }
      />
    </LogsFilterField>
  )
  const primaryFilters = (
    <>
      <LogsFilterField wide>
        <CompactDateTimeRangePicker
          start={
            draft.start_time === undefined
              ? undefined
              : new Date(draft.start_time * 1000)
          }
          end={
            draft.end_time === undefined
              ? undefined
              : new Date(draft.end_time * 1000)
          }
          onChange={(range) =>
            setDraft((previous) => ({
              ...previous,
              start_time: range.start
                ? Math.floor(range.start.getTime() / 1000)
                : undefined,
              end_time: range.end
                ? Math.floor(range.end.getTime() / 1000)
                : undefined,
            }))
          }
        />
      </LogsFilterField>
      <LogsFilterField>
        <Combobox
          options={[
            { value: 'all', label: t('All results') },
            { value: 'true', label: t('Flagged') },
            { value: 'false', label: t('Not flagged') },
          ]}
          value={draft.flagged ?? 'all'}
          onValueChange={(value) => {
            if (value === null) return
            setDraft((previous) => ({
              ...previous,
              flagged: value === 'true' || value === 'false' ? value : undefined,
            }))
          }}
          aria-label={t('Result')}
          className='w-full'
        />
      </LogsFilterField>
      {textFilter('keyword', t('Search user, key, model or excerpt'))}
    </>
  )
  const advancedFilters = (
    <>
      {textFilter('group', t('Group'))}
      {textFilter('request_id', t('Request ID'))}
    </>
  )
  const advancedCount = [draft.group, draft.request_id].filter(Boolean).length
  const activeCount =
    advancedCount +
    [draft.flagged, draft.keyword, draft.start_time, draft.end_time].filter(
      (value) => value !== undefined
    ).length

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Content Moderation')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col'>
          <DataTablePage
            table={table}
            columns={columns}
            isLoading={query.isPending}
            isFetching={query.isFetching}
            emptyTitle={
              query.isError
                ? t('Failed to load content moderation events')
                : t('No records')
            }
            paginationInFooter
            className='h-auto min-h-0 flex-1'
            applyHeaderSize
            getColumnClassName={() => 'py-2'}
            toolbar={
              <LogsFilterToolbar
                table={table}
                primaryFilters={primaryFilters}
                advancedFilters={advancedFilters}
                mobileFilters={
                  <>
                    {primaryFilters}
                    {advancedFilters}
                  </>
                }
                mobileFilterCount={activeCount}
                advancedFilterCount={advancedCount}
                hasActiveFilters={activeCount > 0}
                hasAdvancedActiveFilters={advancedCount > 0}
                searchLoading={query.isFetching}
                onSearch={() => apply(draft)}
                onReset={() => {
                  setDraft({})
                  apply({})
                }}
                actionEnd={
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='text-destructive'
                    disabled={preview.isPending}
                    onClick={() => preview.mutate()}
                  >
                    {t('Delete matching events')}
                  </Button>
                }
              />
            }
          />
        </div>
      </SectionPageLayout.Content>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open) setPending(null)
        }}
        title={t('Delete content moderation events')}
        desc={
          pending?.kind === 'filter'
            ? t(
                'Delete {{count}} events that match the applied filters? Events recorded after this preview are kept. This cannot be undone.',
                { count: pending.total }
              )
            : t('Delete this event? This cannot be undone.')
        }
        destructive
        confirmText={t('Delete')}
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (pending) remove.mutate(pending)
        }}
      />
    </SectionPageLayout>
  )
}
