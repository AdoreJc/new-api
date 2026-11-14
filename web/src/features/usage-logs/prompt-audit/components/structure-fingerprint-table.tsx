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

import { DataTablePage, useDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

import {
  getPromptAuditStructureFingerprints,
  type PromptAuditStructureFingerprint,
  type PromptAuditStructureFingerprintSort,
} from '../api'
import { useStructureFingerprintColumns } from './structure-fingerprint-columns'

const EMPTY_FINGERPRINTS: PromptAuditStructureFingerprint[] = []

/** System prompt fingerprints ranked by distinct users or by requests. */
export function StructureFingerprintTable() {
  const { t } = useTranslation()
  const [sort, setSort] = useState<PromptAuditStructureFingerprintSort>('users')
  const [page, setPage] = useState({ pageIndex: 0, pageSize: 20 })
  const query = useQuery({
    queryKey: ['prompt-audit', 'structure', 'fingerprints', sort, page],
    queryFn: () =>
      getPromptAuditStructureFingerprints(
        page.pageIndex + 1,
        page.pageSize,
        sort
      ),
    placeholderData: keepPreviousData,
    retry: false,
  })
  const columns = useStructureFingerprintColumns()
  const { table } = useDataTable({
    columns,
    data: query.data?.items ?? EMPTY_FINGERPRINTS,
    getRowId: (item) => item.fingerprint,
    totalCount: query.data?.total ?? 0,
    pagination: page,
    onPaginationChange: (updater) =>
      setPage((previous) =>
        typeof updater === 'function' ? updater(previous) : updater
      ),
    enableSorting: false,
    manualFiltering: true,
    manualPagination: true,
  })

  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load structure statistics')}
        onRetry={() => void query.refetch()}
        className='min-h-[200px]'
      />
    )
  }
  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={query.isPending}
      isFetching={query.isFetching}
      emptyTitle={t('No structure statistics yet')}
      emptyDescription={t(
        'Turn on request structure statistics in the prompt audit settings to start measuring.'
      )}
      fixedHeight={false}
      paginationInFooter={false}
      applyHeaderSize
      getColumnClassName={() => 'py-2'}
      toolbar={
        <ToggleGroup
          value={[sort]}
          onValueChange={(values) => {
            if (values[0] !== 'users' && values[0] !== 'requests') return
            setSort(values[0])
            setPage((previous) => ({ ...previous, pageIndex: 0 }))
          }}
          variant='outline'
          size='sm'
          aria-label={t('Sort fingerprints')}
        >
          <ToggleGroupItem value='users'>
            {t('By distinct users')}
          </ToggleGroupItem>
          <ToggleGroupItem value='requests'>{t('By requests')}</ToggleGroupItem>
        </ToggleGroup>
      }
    />
  )
}
