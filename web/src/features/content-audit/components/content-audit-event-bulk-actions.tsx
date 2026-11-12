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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { Table } from '@tanstack/react-table'
import { Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTableBulkActions } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { handleServerError } from '@/lib/handle-server-error'

import { batchDeleteContentAuditEvents } from '../api'
import {
  CONTENT_AUDIT_MAX_BATCH_DELETE,
  CONTENT_AUDIT_QUERY_KEYS,
} from '../constants'
import type { ContentAuditEvent } from '../types'

export function ContentAuditEventBulkActions(props: {
  table: Table<ContentAuditEvent>
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [deleteIds, setDeleteIds] = useState<number[] | null>(null)
  const selectedRows = props.table.getFilteredSelectedRowModel().rows
  const overLimit = selectedRows.length > CONTENT_AUDIT_MAX_BATCH_DELETE
  const deletion = useMutation({
    mutationFn: batchDeleteContentAuditEvents,
    onSuccess: (count) => {
      toast.success(t('Deleted {{count}} content audit events', { count }))
      props.table.resetRowSelection()
      setDeleteIds(null)
      void queryClient.invalidateQueries({
        queryKey: CONTENT_AUDIT_QUERY_KEYS.events,
      })
    },
    onError: (error) => {
      handleServerError(error, t('Failed to delete content audit events'))
    },
  })
  const label = overLimit
    ? t('Select at most {{count}} events to delete at once', {
        count: CONTENT_AUDIT_MAX_BATCH_DELETE,
      })
    : t('Delete selected events')

  return (
    <>
      <DataTableBulkActions
        table={props.table}
        entityName={t('content audit event')}
      >
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='destructive'
                size='icon'
                className='size-8'
                aria-label={label}
                disabled={deletion.isPending || overLimit}
                onClick={() =>
                  setDeleteIds(selectedRows.map((row) => row.original.id))
                }
              />
            }
          >
            <Trash2 aria-hidden='true' />
          </TooltipTrigger>
          <TooltipContent>{label}</TooltipContent>
        </Tooltip>
      </DataTableBulkActions>
      <ConfirmDialog
        destructive
        open={deleteIds !== null}
        onOpenChange={(open) => {
          if (!open && !deletion.isPending) setDeleteIds(null)
        }}
        title={t('Delete {{count}} content audit events?', {
          count: deleteIds?.length ?? 0,
        })}
        desc={t('This action cannot be undone.')}
        confirmText={deletion.isPending ? t('Deleting...') : t('Delete')}
        isLoading={deletion.isPending}
        disabled={!deleteIds?.length}
        handleConfirm={() => {
          if (deleteIds?.length && !deletion.isPending) {
            deletion.mutate(deleteIds)
          }
        }}
      />
    </>
  )
}
