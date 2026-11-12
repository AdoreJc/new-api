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
import type { TFunction } from 'i18next'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTableBulkActions,
  DataTablePage,
  useDataTable,
} from '@/components/data-table'
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
  batchDeletePromptAuditEvents,
  deletePromptAuditEvent,
  deletePromptAuditEventsByFilter,
  getPromptAuditEvents,
  previewPromptAuditDeletion,
  type PromptAuditDeletionPreview,
  type PromptAuditEvent,
  type PromptAuditEventFilter,
} from './api'
import { usePromptAuditColumns } from './components/prompt-audit-columns'
import { PromptAuditFilterSummary } from './components/prompt-audit-filter-summary'
import { PromptAuditKeywordOptions } from './components/prompt-audit-keyword-options'
import { PromptAuditScanStatus } from './components/prompt-audit-scan-status'
import { isScannedPromptAuditSearch } from './lib/keyword-search'

const EMPTY_EVENTS: PromptAuditEvent[] = []

type PendingDeletion =
  | { kind: 'one'; event: PromptAuditEvent }
  | { kind: 'selected'; ids: number[] }
  | {
      kind: 'filter'
      filter: PromptAuditEventFilter
      preview: PromptAuditDeletionPreview
    }

function deletePendingEvents(deletion: PendingDeletion) {
  if (deletion.kind === 'one') return deletePromptAuditEvent(deletion.event.id)
  if (deletion.kind === 'selected') {
    return batchDeletePromptAuditEvents(deletion.ids)
  }
  return deletePromptAuditEventsByFilter(deletion.filter, deletion.preview)
}

export function PromptAuditEvents() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<PromptAuditEventFilter>({})
  const [filter, setFilter] = useState<PromptAuditEventFilter>({})
  const [page, setPage] = useState({ pageIndex: 0, pageSize: 20 })
  const [pending, setPending] = useState<PendingDeletion | null>(null)
  // A regex or full prompt search pages with a cursor instead of a page index.
  const [cursor, setCursor] = useState<number | undefined>(undefined)
  const scanned = isScannedPromptAuditSearch(filter)

  const query = useQuery({
    queryKey: ['prompt-audit', 'events', filter, page, cursor],
    queryFn: () =>
      getPromptAuditEvents(
        filter,
        scanned ? 1 : page.pageIndex + 1,
        page.pageSize,
        scanned ? cursor : undefined
      ),
    retry: false,
  })
  const onDelete = useCallback(
    (event: PromptAuditEvent) => setPending({ kind: 'one', event }),
    []
  )
  const columns = usePromptAuditColumns(onDelete)
  const { table } = useDataTable({
    columns,
    data: query.data?.items ?? EMPTY_EVENTS,
    getRowId: (event) => String(event.id),
    totalCount: query.data?.total ?? query.data?.items.length ?? 0,
    pagination: page,
    onPaginationChange: (updater) =>
      setPage((previous) =>
        typeof updater === 'function' ? updater(previous) : updater
      ),
    enableRowSelection: true,
    enableSorting: false,
    manualFiltering: true,
    manualPagination: true,
  })

  // The preview counts the filters on screen, so it also applies them: what
  // the list shows, what the dialog counts and what is deleted stay the same.
  const preview = useMutation({
    mutationFn: (previewed: PromptAuditEventFilter) =>
      previewPromptAuditDeletion(previewed),
    onSuccess: (data, previewed) => {
      if (data.matched_count === 0) {
        toast.info(t('No events match the current filters'))
        return
      }
      setPending({ kind: 'filter', filter: previewed, preview: data })
    },
    onError: (error) => handleServerError(error),
  })
  const remove = useMutation({
    mutationFn: deletePendingEvents,
    onSuccess: (data) => {
      toast.success(t('Deleted {{count}} events', { count: data.deleted }))
      setPending(null)
      table.resetRowSelection()
      void queryClient.invalidateQueries({
        queryKey: ['prompt-audit', 'events'],
      })
    },
    onError: (error, deletion) => {
      handleServerError(error)
      // An expired or outdated preview cannot be confirmed again.
      if (deletion.kind === 'filter') setPending(null)
    },
  })

  // A regex the browser cannot parse is still sent: RE2 accepts syntax that
  // JavaScript does not, and the server answers 400 for a truly invalid one.
  const apply = (next: PromptAuditEventFilter) => {
    setFilter(next)
    setCursor(undefined)
    table.resetRowSelection()
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
            { value: 'critical', label: t('Critical') },
            { value: 'flag', label: t('Flagged') },
          ]}
          value={draft.decision ?? 'all'}
          onValueChange={(value) => {
            if (value === null) return
            setDraft((previous) => ({
              ...previous,
              decision: value === 'all' ? undefined : value,
            }))
          }}
          aria-label={t('Result')}
          className='w-full'
        />
      </LogsFilterField>
      {textFilter('keyword', t('Search user, key, model or preview'))}
      <LogsFilterField wide>
        <PromptAuditKeywordOptions
          draft={draft}
          onChange={(patch) =>
            setDraft((previous) => ({ ...previous, ...patch }))
          }
        />
      </LogsFilterField>
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
    [
      draft.decision,
      draft.keyword,
      draft.keyword_mode,
      draft.keyword_fields,
      draft.start_time,
      draft.end_time,
    ].filter((value) => value !== undefined).length

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Prompt Audit')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col'>
          <DataTablePage
            table={table}
            columns={columns}
            isLoading={query.isPending}
            isFetching={query.isFetching}
            emptyTitle={
              query.isError
                ? t('Failed to load prompt audit events')
                : t('No records')
            }
            paginationInFooter
            showPagination={!scanned}
            afterTable={
              scanned && query.data ? (
                <PromptAuditScanStatus
                  page={query.data}
                  continued={cursor !== undefined}
                  loading={query.isFetching}
                  onContinue={(next) => {
                    table.resetRowSelection()
                    setCursor(next)
                  }}
                  onRestart={() => {
                    table.resetRowSelection()
                    setCursor(undefined)
                  }}
                />
              ) : undefined
            }
            bulkActions={
              <DataTableBulkActions table={table} entityName='event'>
                <Button
                  type='button'
                  size='sm'
                  variant='destructive'
                  onClick={() =>
                    setPending({
                      kind: 'selected',
                      ids: table
                        .getSelectedRowModel()
                        .rows.map((row) => row.original.id),
                    })
                  }
                >
                  {t('Delete selected ({{count}})', {
                    count: table.getSelectedRowModel().rows.length,
                  })}
                </Button>
              </DataTableBulkActions>
            }
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
                  scanned || isScannedPromptAuditSearch(draft) ? undefined : (
                    <Button
                      type='button'
                      size='sm'
                      variant='outline'
                      className='text-destructive'
                      disabled={preview.isPending}
                      onClick={() => {
                        apply(draft)
                        preview.mutate(draft)
                      }}
                    >
                      {t('Delete matching events')}
                    </Button>
                  )
                }
              />
            }
          />
        </div>
        {/* Inside Content: SectionPageLayout renders only its slot children. */}
        <ConfirmDialog
          open={pending !== null}
          onOpenChange={(open) => {
            if (!open) setPending(null)
          }}
          title={t('Delete prompt audit events')}
          desc={
            pending?.kind === 'filter' ? (
              <div className='grid gap-2'>
                <p>
                  {t(
                    'Delete {{count}} events that match these filters? Events recorded after this preview are kept. This cannot be undone.',
                    { count: pending.preview.matched_count }
                  )}
                </p>
                <PromptAuditFilterSummary filter={pending.filter} />
              </div>
            ) : (
              pendingDeletionText(pending, t)
            )
          }
          destructive
          confirmText={t('Delete')}
          isLoading={remove.isPending}
          handleConfirm={() => {
            if (pending) remove.mutate(pending)
          }}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function pendingDeletionText(
  pending: PendingDeletion | null,
  t: TFunction
): string {
  if (pending?.kind === 'selected') {
    return t('Delete {{count}} selected events? This cannot be undone.', {
      count: pending.ids.length,
    })
  }
  return t('Delete this event? This cannot be undone.')
}
