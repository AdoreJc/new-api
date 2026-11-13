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
import {
  BadgeDollarSign,
  CircleAlert,
  Coins,
  Percent,
  Receipt,
  TrendingUp,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  getCostBackfillStatus,
  getCostSummary,
  startCostBackfill,
} from '@/features/dashboard/api'
import { getDefaultDays } from '@/features/dashboard/lib'
import type {
  CostDimension,
  CostSummaryRow,
  DashboardFilters,
} from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatPercent, formatQuota } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { computeTimeRange, formatDate } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'

import { PanelWrapper } from '../ui/panel-wrapper'
import { StatCard } from '../ui/stat-card'

interface CostAccountingProps {
  filters?: DashboardFilters
}

const DIMENSION_OPTIONS: { value: CostDimension; labelKey: string }[] = [
  { value: 'group', labelKey: 'By group' },
  { value: 'channel', labelKey: 'By channel' },
  { value: 'model', labelKey: 'By model' },
  { value: 'user', labelKey: 'By user' },
]

const DIMENSION_HEADER_KEYS: Record<CostDimension, string> = {
  group: 'Group',
  channel: 'Channel',
  model: 'Model',
  user: 'User',
}

// Usage-based cost plus, unless hidden, the amortized fixed monthly cost.
function totalCost(row: CostSummaryRow, includeFixed: boolean): number {
  return row.cost_quota + (includeFixed ? row.fixed_cost_quota : 0)
}

// Profit and margin only count revenue of rows that carry a cost; rows still
// waiting for the backfill have revenue but no cost yet.
function grossProfit(row: CostSummaryRow, includeFixed: boolean): number {
  return row.accounted_quota - totalCost(row, includeFixed)
}

// Gross margin in percent of revenue; undefined when nothing was charged.
function grossMargin(
  row: CostSummaryRow,
  includeFixed: boolean
): number | undefined {
  if (row.accounted_quota <= 0) return undefined
  return (grossProfit(row, includeFixed) / row.accounted_quota) * 100
}

function dimensionLabel(
  row: CostSummaryRow,
  dimension: CostDimension,
  unallocatedLabel: string
): string {
  if (row.unallocated) return unallocatedLabel
  switch (dimension) {
    case 'group':
      return row.use_group || '-'
    case 'channel':
      return row.channel_name
        ? `${row.channel_name} (#${row.channel_id})`
        : `#${row.channel_id ?? 0}`
    case 'model':
      return row.model_name || '-'
    case 'user':
      return row.username || `#${row.user_id ?? 0}`
  }
}

function rowKey(row: CostSummaryRow, dimension: CostDimension): string {
  if (row.unallocated) return 'unallocated'
  switch (dimension) {
    case 'group':
      return `g:${row.use_group ?? ''}`
    case 'channel':
      return `c:${row.channel_id ?? 0}`
    case 'model':
      return `m:${row.model_name ?? ''}`
    case 'user':
      return `u:${row.user_id ?? 0}`
  }
}

export function CostAccounting(props: CostAccountingProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const isRoot = useAuthStore(
    (state) => (state.auth.user?.role ?? 0) >= ROLE.SUPER_ADMIN
  )
  const [dimension, setDimension] = useState<CostDimension>('group')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [usageCostOnly, setUsageCostOnly] = useState(false)
  const includeFixed = !usageCostOnly

  const timeRange = useMemo(
    () =>
      computeTimeRange(
        getDefaultDays(props.filters?.time_granularity),
        props.filters?.start_timestamp,
        props.filters?.end_timestamp
      ),
    [
      props.filters?.end_timestamp,
      props.filters?.start_timestamp,
      props.filters?.time_granularity,
    ]
  )
  const username = props.filters?.username

  const summaryQuery = useQuery({
    queryKey: ['dashboard', 'cost', timeRange, username ?? '', dimension],
    queryFn: async () =>
      requireServerSuccess(
        await getCostSummary({
          ...timeRange,
          ...(username && { username }),
          dimension,
        })
      ),
    select: (res) => res.data,
    staleTime: 60_000,
  })

  const backfillQuery = useQuery({
    queryKey: ['dashboard', 'cost', 'backfill'],
    queryFn: async () => requireServerSuccess(await getCostBackfillStatus()),
    select: (res) => res.data,
    refetchInterval: (query) => {
      const status = query.state.data?.data?.task?.status
      return status === 'pending' || status === 'running' ? 3000 : false
    },
  })

  const backfillMutation = useMutation({
    mutationFn: async () => requireServerSuccess(await startCostBackfill()),
    onSuccess: () => {
      toast.success(t('Historical cost backfill started'))
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['dashboard', 'cost'],
      })
    },
  })

  const summary = summaryQuery.data
  const total = summary?.total
  const backfill = backfillQuery.data
  const backfillTask = backfill?.task
  const backfillRunning =
    backfillTask?.status === 'pending' || backfillTask?.status === 'running'
  const pendingRows = backfill?.pending_quota_data ?? 0
  // Logs are only counted by the backfill run itself, so offer it until one
  // run has succeeded, and again whenever dashboard rows are still missing.
  const canStartBackfill =
    isRoot &&
    !backfillRunning &&
    (pendingRows > 0 || backfillTask?.status !== 'succeeded')
  const startTime = summary?.accounting_start_time ?? 0
  const rangeHasEstimates =
    startTime > 0 && timeRange.start_timestamp < startTime
  const fixedChannels = summary?.fixed_channels ?? []
  const missingFixed = fixedChannels.filter((channel) => channel.missing)
  const expiredFixed = fixedChannels.filter((channel) => channel.expired)
  const channelNames = (list: typeof fixedChannels) =>
    list
      .map((channel) => channel.channel_name || `#${channel.channel_id}`)
      .join(', ')
  const allCost = total ? totalCost(total, true) : 0
  const fixedShare =
    total && allCost > 0 ? (total.fixed_cost_quota / allCost) * 100 : 0

  const cards = [
    {
      key: 'standard',
      title: t('List price'),
      description: t('Standard price before group ratios'),
      icon: Receipt,
      value: formatQuota(total?.standard_quota ?? 0),
      tone: 'accent-1' as const,
    },
    {
      key: 'revenue',
      title: t('Charged revenue'),
      description: t('Quota actually charged'),
      icon: Coins,
      value: formatQuota(total?.quota ?? 0),
      tone: 'accent-2' as const,
    },
    {
      key: 'cost',
      title: t('Channel cost'),
      description: includeFixed
        ? t('Fixed monthly cost {{share}} of the total', {
            share: formatPercent(fixedShare),
          })
        : t('Usage-based cost only'),
      icon: BadgeDollarSign,
      value: formatQuota(total ? totalCost(total, includeFixed) : 0),
      tone: 'accent-3' as const,
    },
    {
      key: 'profit',
      title: t('Gross profit'),
      description: t('Revenue minus channel cost'),
      icon: TrendingUp,
      value: formatQuota(total ? grossProfit(total, includeFixed) : 0),
      tone: 'accent-1' as const,
    },
    {
      key: 'margin',
      title: t('Gross margin'),
      description: t('Gross profit as a share of revenue'),
      icon: Percent,
      value: formatPercent(
        total ? grossMargin(total, includeFixed) : undefined
      ),
      tone: 'accent-2' as const,
    },
  ]

  const columns: StaticDataTableColumn<CostSummaryRow>[] = [
    {
      id: 'name',
      header: t(DIMENSION_HEADER_KEYS[dimension]),
      cellClassName: 'max-w-64 truncate font-medium',
      cell: (row) =>
        dimensionLabel(row, dimension, t('Unallocated fixed cost')),
    },
    {
      id: 'count',
      header: t('Requests'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatNumber(row.count, locale),
    },
    {
      id: 'standard',
      header: t('List price'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatQuota(row.standard_quota),
    },
    {
      id: 'revenue',
      header: t('Charged revenue'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatQuota(row.quota),
    },
    {
      id: 'cost',
      header: t('Channel cost'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatQuota(totalCost(row, includeFixed)),
    },
    {
      id: 'profit',
      header: t('Gross profit'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatQuota(grossProfit(row, includeFixed)),
    },
    {
      id: 'margin',
      header: t('Gross margin'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatPercent(grossMargin(row, includeFixed)),
    },
  ]

  return (
    <div className='space-y-3 sm:space-y-4'>
      {(missingFixed.length > 0 || expiredFixed.length > 0) && (
        <Alert variant='destructive'>
          <CircleAlert />
          <AlertTitle>{t('Fixed monthly costs need attention')}</AlertTitle>
          <AlertDescription>
            {missingFixed.length > 0 && (
              <p>
                {t(
                  'Monthly cost not entered for: {{channels}}. Their cost is left out until it is entered.',
                  { channels: channelNames(missingFixed) }
                )}
              </p>
            )}
            {expiredFixed.length > 0 && (
              <p>
                {t(
                  'Subscription expired for: {{channels}}. Their fixed cost stops at the end date until a renewal is read.',
                  { channels: channelNames(expiredFixed) }
                )}
              </p>
            )}
          </AlertDescription>
        </Alert>
      )}
      {(rangeHasEstimates || pendingRows > 0) && (
        <Alert>
          <CircleAlert />
          <AlertTitle>{t('Historical data is estimated')}</AlertTitle>
          <AlertDescription>
            <p>
              {t(
                'Data before {{date}} has no recorded list price. It is estimated as charged quota divided by the group ratio, with a channel cost ratio of 1.',
                { date: startTime > 0 ? formatDate(startTime) : '-' }
              )}
            </p>
            {pendingRows > 0 && (
              <p>
                {t(
                  '{{count}} historical rows are not estimated yet. Their list price and cost are missing, and profit and margin leave them out.',
                  { count: pendingRows }
                )}
              </p>
            )}
            {backfillRunning && (
              <p>
                {t('Backfill in progress: {{progress}}%', {
                  progress: backfillTask?.state?.progress ?? 0,
                })}
              </p>
            )}
            {backfillTask?.status === 'failed' && (
              <p className='text-destructive'>
                {t('The last backfill failed: {{error}}', {
                  error: backfillTask.error || '-',
                })}
              </p>
            )}
            {canStartBackfill && (
              <Button
                size='sm'
                variant='outline'
                className='mt-2'
                onClick={() => setConfirmOpen(true)}
              >
                {t('Estimate historical data')}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}

      <div className='bg-card grid grid-cols-2 gap-1.5 rounded-2xl border p-3 shadow-xs sm:grid-cols-3 sm:gap-3 sm:p-5 xl:grid-cols-5'>
        {cards.map((card) => (
          <div
            key={card.key}
            className='bg-background/60 rounded-lg border px-2 py-1.5 sm:rounded-xl sm:p-3'
          >
            <StatCard
              title={card.title}
              value={card.value}
              description={card.description}
              icon={card.icon}
              tone={card.tone}
              loading={summaryQuery.isLoading}
              error={summaryQuery.isError}
              compactMobile
            />
          </div>
        ))}
      </div>

      <PanelWrapper
        title={t('Cost breakdown')}
        description={t(
          'Revenue is the quota charged, including quota drawn from subscriptions.'
        )}
        loading={summaryQuery.isLoading}
        headerActions={
          <div className='flex flex-wrap items-center gap-3'>
            <Label className='flex items-center gap-2 text-xs font-normal'>
              <Switch
                checked={usageCostOnly}
                onCheckedChange={setUsageCostOnly}
              />
              {t('Usage-based cost only')}
            </Label>
            <Tabs
              value={dimension}
              onValueChange={(value) => setDimension(value as CostDimension)}
            >
              <TabsList>
                {DIMENSION_OPTIONS.map((option) => (
                  <TabsTrigger key={option.value} value={option.value}>
                    {t(option.labelKey)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </div>
        }
        contentClassName='p-0 sm:p-0'
      >
        <StaticDataTable
          columns={columns}
          data={summary?.rows ?? []}
          getRowKey={(row) => rowKey(row, dimension)}
          emptyContent={t('No data available')}
        />
      </PanelWrapper>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Estimate historical data')}
        desc={t(
          'Fill in list price and cost for logs and dashboard data written before cost accounting. It runs in the background in batches and can be started again if interrupted. Charged quota and balances are not changed.'
        )}
        confirmText={t('Start')}
        isLoading={backfillMutation.isPending}
        handleConfirm={() => backfillMutation.mutate()}
      />
    </div>
  )
}
