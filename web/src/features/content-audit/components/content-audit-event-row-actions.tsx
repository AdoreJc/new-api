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
import { Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { deleteContentAuditEvent } from '../api'
import { CONTENT_AUDIT_QUERY_KEYS } from '../constants'
import type { ContentAuditEvent } from '../types'
import { ContentAuditEventDetailsDialog } from './content-audit-event-details-dialog'

export function ContentAuditEventRowActions(props: {
  event: ContentAuditEvent
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [detailsOpen, setDetailsOpen] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const deletion = useMutation({
    mutationFn: () => deleteContentAuditEvent(props.event.id),
    onSuccess: () => {
      toast.success(t('Content audit event deleted'))
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: CONTENT_AUDIT_QUERY_KEYS.events,
      })
    },
    onError: (error) => {
      handleServerError(error, t('Failed to delete content audit event'))
    },
  })

  return (
    <div className='flex items-center gap-1'>
      <Button
        variant='ghost'
        size='sm'
        className='h-7 px-2'
        onClick={() => setDetailsOpen(true)}
      >
        {t('Details')}
      </Button>
      <Button
        variant='ghost'
        size='icon'
        className='text-destructive size-7'
        aria-label={t('Delete content audit event')}
        onClick={() => setConfirmOpen(true)}
      >
        <Trash2 aria-hidden='true' />
      </Button>
      <ContentAuditEventDetailsDialog
        event={props.event}
        open={detailsOpen}
        onOpenChange={setDetailsOpen}
      />
      <ConfirmDialog
        destructive
        open={confirmOpen}
        onOpenChange={(open) => {
          if (!deletion.isPending) setConfirmOpen(open)
        }}
        title={t('Delete this content audit event?')}
        desc={t('This action cannot be undone.')}
        confirmText={deletion.isPending ? t('Deleting...') : t('Delete')}
        isLoading={deletion.isPending}
        handleConfirm={() => {
          if (!deletion.isPending) deletion.mutate()
        }}
      />
    </div>
  )
}
