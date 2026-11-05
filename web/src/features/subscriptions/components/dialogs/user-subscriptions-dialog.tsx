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
import {
  Activity,
  Ban,
  Link2,
  Plus,
  RotateCcw,
  Trash2,
  Unlink,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTableRowActionMenu,
  StaticDataTable,
} from '@/components/data-table'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { getClaudeUsage, getCodexUsage } from '@/features/channels/api'
import {
  ClaudeUsageDialog,
  type ClaudeUsageDialogData,
} from '@/features/channels/components/dialogs/claude-usage-dialog'
import {
  CodexUsageDialog,
  type CodexUsageDialogData,
} from '@/features/channels/components/dialogs/codex-usage-dialog'
import { formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError, getServerMessage } from '@/lib/server-error-message'

import {
  getAdminPlans,
  getUserSubscriptions,
  createUserSubscription,
  invalidateUserSubscription,
  deleteUserSubscription,
  resetUserSubscriptionsByPlan,
  getBindableChannels,
  unbindSubscriptionChannel,
} from '../../api'
import { formatTimestamp } from '../../lib'
import {
  BOUND_CHANNEL_TYPE_CLAUDE_OAUTH,
  BOUND_CHANNEL_TYPE_CODEX,
  bindableChannelLabel,
  boundAccountLabelKey,
  isSubscriptionBound,
} from '../../lib/channel-binding'
import type {
  PlanRecord,
  SubscriptionBindableChannel,
  UserSubscription,
  UserSubscriptionRecord,
} from '../../types'
import { BindChannelDialog } from './bind-channel-dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  user: { id: number; username?: string } | null
  onSuccess?: () => void
}

function SubscriptionStatusBadge(props: {
  sub: UserSubscriptionRecord['subscription']
  t: (key: string) => string
}) {
  // eslint-disable-next-line react-hooks/purity
  const now = Date.now() / 1000
  const isExpired = (props.sub.end_time || 0) > 0 && props.sub.end_time < now
  const isActive = props.sub.status === 'active' && !isExpired
  if (isActive) {
    return (
      <StatusBadge
        label={props.t('Active')}
        variant='success'
        copyable={false}
      />
    )
  }
  if (props.sub.status === 'cancelled') {
    return (
      <StatusBadge
        label={props.t('Invalidated')}
        variant='neutral'
        copyable={false}
      />
    )
  }
  return (
    <StatusBadge
      label={props.t('Expired')}
      variant='neutral'
      copyable={false}
    />
  )
}

export function UserSubscriptionsDialog(props: Props) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [plans, setPlans] = useState<PlanRecord[]>([])
  const [subs, setSubs] = useState<UserSubscriptionRecord[]>([])
  const [selectedPlanId, setSelectedPlanId] = useState<string>('')
  const [resetting, setResetting] = useState(false)
  const [advanceResetTime, setAdvanceResetTime] = useState(true)
  const [resetAction, setResetAction] = useState<{
    planId: number
    planTitle: string
  } | null>(null)
  const [confirmAction, setConfirmAction] = useState<{
    type: 'invalidate' | 'delete' | 'unbind'
    subId: number
  } | null>(null)
  const [bindableChannels, setBindableChannels] = useState<
    SubscriptionBindableChannel[]
  >([])
  const [selectedChannelId, setSelectedChannelId] = useState<string>('')
  const [bindTarget, setBindTarget] = useState<UserSubscription | null>(null)
  const [usageTarget, setUsageTarget] = useState<UserSubscription | null>(null)
  const [usageLoading, setUsageLoading] = useState(false)
  const [claudeUsageResponse, setClaudeUsageResponse] =
    useState<ClaudeUsageDialogData | null>(null)
  const [codexUsageResponse, setCodexUsageResponse] =
    useState<CodexUsageDialogData | null>(null)

  const planTitleMap = useMemo(() => {
    const map = new Map<number, string>()
    plans.forEach((p) => {
      if (p.plan.id) map.set(p.plan.id, p.plan.title || `#${p.plan.id}`)
    })
    return map
  }, [plans])

  const loadData = useCallback(async () => {
    if (!props.user?.id) return
    setLoading(true)
    try {
      const [plansRes, subsRes, channelsRes] = await Promise.all([
        getAdminPlans(),
        getUserSubscriptions(props.user.id),
        getBindableChannels(),
      ])
      if (plansRes.success) {
        setPlans(plansRes.data || [])
      } else {
        handleServerError(plansRes)
      }
      if (subsRes.success) {
        setSubs(subsRes.data || [])
      } else {
        handleServerError(subsRes)
      }
      if (channelsRes.success) {
        setBindableChannels(channelsRes.data || [])
      } else {
        handleServerError(channelsRes)
      }
    } catch (error) {
      handleServerError(error, t('Loading failed'))
    } finally {
      setLoading(false)
    }
  }, [props.user?.id, t])

  useEffect(() => {
    if (props.open && props.user?.id) {
      setSelectedPlanId('')
      setSelectedChannelId('')
      loadData()
    }
  }, [props.open, props.user?.id, loadData])

  const channelOptions = useMemo(
    () => [
      { value: '', label: t('No account binding') },
      ...bindableChannels.map((channel) => ({
        value: String(channel.id),
        label: bindableChannelLabel(channel, t),
        disabled: channel.bound_subscription_id > 0,
      })),
    ],
    [bindableChannels, t]
  )

  const openUsage = async (sub: UserSubscription) => {
    if (!isSubscriptionBound(sub) || usageLoading) return
    setUsageLoading(true)
    try {
      if (sub.bound_channel_type === BOUND_CHANNEL_TYPE_CLAUDE_OAUTH) {
        const res = await getClaudeUsage(Number(sub.bound_channel_id))
        if (!res.success) {
          throw createServerError(res, t('Failed to fetch usage'))
        }
        setClaudeUsageResponse(res)
      } else if (sub.bound_channel_type === BOUND_CHANNEL_TYPE_CODEX) {
        const res = await getCodexUsage(Number(sub.bound_channel_id))
        if (!res.success) {
          throw createServerError(res, t('Failed to fetch usage'))
        }
        setCodexUsageResponse(res)
      } else {
        return
      }
      setUsageTarget(sub)
    } catch (error) {
      handleServerError(error, t('Failed to fetch usage'))
    } finally {
      setUsageLoading(false)
    }
  }

  const refreshUsage = async () => {
    if (!usageTarget || usageLoading) return
    setUsageLoading(true)
    try {
      if (usageTarget.bound_channel_type === BOUND_CHANNEL_TYPE_CLAUDE_OAUTH) {
        const res = await getClaudeUsage(Number(usageTarget.bound_channel_id))
        if (!res.success) {
          throw createServerError(res, t('Failed to fetch usage'))
        }
        setClaudeUsageResponse(res)
      } else {
        const res = await getCodexUsage(Number(usageTarget.bound_channel_id))
        if (!res.success) {
          throw createServerError(res, t('Failed to fetch usage'))
        }
        setCodexUsageResponse(res)
      }
    } catch (error) {
      handleServerError(error, t('Failed to fetch usage'))
    } finally {
      setUsageLoading(false)
    }
  }

  const handleCreate = async () => {
    if (!props.user?.id || !selectedPlanId) {
      toast.error(t('Please select a subscription plan'))
      return
    }
    setCreating(true)
    try {
      const res = await createUserSubscription(props.user.id, {
        plan_id: Number(selectedPlanId),
        channel_id: selectedChannelId ? Number(selectedChannelId) : undefined,
      })
      if (res.success) {
        toast.success(getServerMessage(res.data) || t('Added successfully'))
        setSelectedPlanId('')
        setSelectedChannelId('')
        await loadData()
        props.onSuccess?.()
      } else {
        handleServerError(res)
      }
    } catch (error) {
      handleServerError(error, t('Request failed'))
    } finally {
      setCreating(false)
    }
  }

  const handleConfirmAction = async () => {
    if (!confirmAction) return
    try {
      if (confirmAction.type === 'invalidate') {
        const res = await invalidateUserSubscription(confirmAction.subId)
        if (res.success) {
          toast.success(getServerMessage(res.data) || t('Has been invalidated'))
          await loadData()
          props.onSuccess?.()
        } else {
          handleServerError(res)
        }
      } else if (confirmAction.type === 'unbind') {
        const res = await unbindSubscriptionChannel(confirmAction.subId)
        if (res.success) {
          toast.success(getServerMessage(res.data) || t('Unbound successfully'))
          await loadData()
          props.onSuccess?.()
        } else {
          handleServerError(res)
        }
      } else {
        const res = await deleteUserSubscription(confirmAction.subId)
        if (res.success) {
          toast.success(t('Deleted'))
          await loadData()
          props.onSuccess?.()
        } else {
          handleServerError(res)
        }
      }
    } catch (error) {
      handleServerError(error, t('Operation failed'))
    } finally {
      setConfirmAction(null)
    }
  }

  const handleResetConfirm = async () => {
    if (!props.user?.id || !resetAction) return
    setResetting(true)
    try {
      const res = await resetUserSubscriptionsByPlan(props.user.id, {
        plan_id: resetAction.planId,
        advance_reset_time: advanceResetTime,
      })
      if (res.success) {
        toast.success(
          t('Reset {{count}} active subscriptions', {
            count: res.data?.reset_count || 0,
          })
        )
        await loadData()
        props.onSuccess?.()
      } else {
        handleServerError(res)
      }
    } catch (error) {
      handleServerError(error, t('Operation failed'))
    } finally {
      setResetting(false)
      setResetAction(null)
    }
  }

  return (
    <>
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('User Subscription Management')}</SheetTitle>
            <SheetDescription>
              {props.user?.username || '-'} (ID: {props.user?.id || '-'})
            </SheetDescription>
          </SheetHeader>

          <div className={sideDrawerFormClassName()}>
            <div className='flex flex-col gap-2 sm:flex-row'>
              <Combobox
                options={plans.map((p) => ({
                  value: String(p.plan.id),
                  label: `${p.plan.title} ($${Number(p.plan.price_amount || 0).toFixed(2)})`,
                }))}
                value={selectedPlanId}
                onValueChange={(v) => v !== null && setSelectedPlanId(v)}
                className='flex-1'
                placeholder={t('Select subscription plan')}
              />
              <Combobox
                options={channelOptions}
                value={selectedChannelId}
                onValueChange={(v) => setSelectedChannelId(v ?? '')}
                className='flex-1'
                placeholder={t('Bind account (optional)')}
                aria-label={t('Bind account (optional)')}
              />
              <Button
                onClick={handleCreate}
                disabled={creating || !selectedPlanId}
              >
                <Plus className='mr-1 h-4 w-4' />
                {t('Add subscription')}
              </Button>
            </div>

            <StaticDataTable
              data={loading ? [] : subs}
              getRowKey={(record) => record.subscription.id}
              emptyClassName={loading ? 'py-8' : 'text-muted-foreground py-8'}
              emptyContent={
                loading ? t('Loading...') : t('No subscription records')
              }
              columns={[
                {
                  id: 'id',
                  header: t('ID'),
                  cell: (record) => <TableId value={record.subscription.id} />,
                },
                {
                  id: 'plan',
                  header: t('Plan'),
                  cell: (record) => {
                    const sub = record.subscription

                    return (
                      <div>
                        <div className='font-medium'>
                          {planTitleMap.get(sub.plan_id) || `#${sub.plan_id}`}
                        </div>
                        <div className='text-muted-foreground text-sm'>
                          {t('Source')}: {sub.source || '-'}
                        </div>
                      </div>
                    )
                  },
                },
                {
                  id: 'status',
                  header: t('Status'),
                  cell: (record) => (
                    <SubscriptionStatusBadge sub={record.subscription} t={t} />
                  ),
                },
                {
                  id: 'validity',
                  header: t('Validity'),
                  cell: (record) => {
                    const sub = record.subscription

                    return (
                      <div className='text-sm'>
                        <div>
                          {t('Start')}: {formatTimestamp(sub.start_time)}
                        </div>
                        <div>
                          {t('End')}: {formatTimestamp(sub.end_time)}
                        </div>
                      </div>
                    )
                  },
                },
                {
                  id: 'quota',
                  header: t('Total Quota'),
                  cell: (record) => {
                    const sub = record.subscription
                    const total = Number(sub.amount_total || 0)
                    const used = Number(sub.amount_used || 0)
                    return total > 0
                      ? `${formatQuota(used)}/${formatQuota(total)}`
                      : t('Unlimited')
                  },
                },
                {
                  id: 'bound',
                  header: t('Bound account'),
                  cell: (record) => {
                    const sub = record.subscription
                    if (!isSubscriptionBound(sub)) {
                      return <span className='text-muted-foreground'>-</span>
                    }
                    return (
                      <div className='text-sm'>
                        <div className='font-medium'>
                          #{sub.bound_channel_id} {sub.bound_channel_name || ''}
                        </div>
                        <div className='text-muted-foreground text-xs'>
                          {t(boundAccountLabelKey(sub.bound_channel_type))}
                          {sub.bound_group ? ` · ${sub.bound_group}` : ''}
                        </div>
                      </div>
                    )
                  },
                },
                {
                  id: 'actions',
                  header: t('Actions'),
                  className: 'text-right',
                  cellClassName: 'text-right',
                  cell: (record) => {
                    const sub = record.subscription
                    const now = Date.now() / 1000
                    const isExpired =
                      (sub.end_time || 0) > 0 && sub.end_time < now
                    const isActive = sub.status === 'active' && !isExpired

                    const bound = isSubscriptionBound(sub)
                    const usageSupported =
                      bound &&
                      (sub.bound_channel_type ===
                        BOUND_CHANNEL_TYPE_CLAUDE_OAUTH ||
                        sub.bound_channel_type === BOUND_CHANNEL_TYPE_CODEX)

                    return (
                      <DataTableRowActionMenu ariaLabel={t('Actions')}>
                        <DropdownMenuItem
                          disabled={!isActive}
                          onClick={() => setBindTarget(sub)}
                        >
                          {bound
                            ? t('Change bound account')
                            : t('Bind account')}
                          <DropdownMenuShortcut>
                            <Link2 size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={!usageSupported || usageLoading}
                          onClick={() => void openUsage(sub)}
                        >
                          {t('View account usage')}
                          <DropdownMenuShortcut>
                            <Activity size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={!bound}
                          onClick={() =>
                            setConfirmAction({ type: 'unbind', subId: sub.id })
                          }
                        >
                          {t('Unbind account')}
                          <DropdownMenuShortcut>
                            <Unlink size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          disabled={!isActive}
                          onClick={() => {
                            setAdvanceResetTime(true)
                            setResetAction({
                              planId: sub.plan_id,
                              planTitle:
                                planTitleMap.get(sub.plan_id) ||
                                `#${sub.plan_id}`,
                            })
                          }}
                        >
                          {t('Reset quota')}
                          <DropdownMenuShortcut>
                            <RotateCcw size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={!isActive}
                          onClick={() =>
                            setConfirmAction({
                              type: 'invalidate',
                              subId: sub.id,
                            })
                          }
                        >
                          {t('Invalidate')}
                          <DropdownMenuShortcut>
                            <Ban size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant='destructive'
                          onClick={() =>
                            setConfirmAction({
                              type: 'delete',
                              subId: sub.id,
                            })
                          }
                        >
                          {t('Delete')}
                          <DropdownMenuShortcut>
                            <Trash2 size={16} />
                          </DropdownMenuShortcut>
                        </DropdownMenuItem>
                      </DataTableRowActionMenu>
                    )
                  },
                },
              ]}
            />
          </div>
        </SheetContent>
      </Sheet>

      {confirmAction && (
        <ConfirmDialog
          open
          onOpenChange={(v) => !v && setConfirmAction(null)}
          title={confirmDialogTitle(confirmAction.type, t)}
          desc={confirmDialogDescription(confirmAction.type, t)}
          handleConfirm={handleConfirmAction}
          destructive={confirmAction.type === 'delete'}
        />
      )}

      {resetAction && (
        <ConfirmDialog
          open
          onOpenChange={(v) => !v && setResetAction(null)}
          title={t('Reset subscription quota')}
          desc={t('Reset active {{plan}} subscriptions for this user?', {
            plan: resetAction.planTitle,
          })}
          confirmText={t('Reset quota')}
          handleConfirm={handleResetConfirm}
          isLoading={resetting}
        >
          <label className='flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm'>
            <span>{t('Advance next reset time')}</span>
            <Switch
              checked={advanceResetTime}
              onCheckedChange={(checked) => setAdvanceResetTime(!!checked)}
              aria-label={t('Advance next reset time')}
            />
          </label>
        </ConfirmDialog>
      )}

      <BindChannelDialog
        open={bindTarget !== null}
        onOpenChange={(open) => !open && setBindTarget(null)}
        subscription={bindTarget}
        onSuccess={async () => {
          await loadData()
          props.onSuccess?.()
        }}
      />

      <ClaudeUsageDialog
        open={
          usageTarget !== null &&
          usageTarget.bound_channel_type === BOUND_CHANNEL_TYPE_CLAUDE_OAUTH
        }
        onOpenChange={(open) => !open && setUsageTarget(null)}
        channelName={usageTarget?.bound_channel_name}
        channelId={usageTarget?.bound_channel_id}
        response={claudeUsageResponse}
        onRefresh={refreshUsage}
        isRefreshing={usageLoading}
      />
      <CodexUsageDialog
        open={
          usageTarget !== null &&
          usageTarget.bound_channel_type === BOUND_CHANNEL_TYPE_CODEX
        }
        onOpenChange={(open) => !open && setUsageTarget(null)}
        channelName={usageTarget?.bound_channel_name}
        channelId={usageTarget?.bound_channel_id}
        response={codexUsageResponse}
        onRefresh={refreshUsage}
        isRefreshing={usageLoading}
      />
    </>
  )
}

function confirmDialogTitle(
  type: 'invalidate' | 'delete' | 'unbind',
  t: (key: string) => string
): string {
  if (type === 'invalidate') return t('Confirm invalidate')
  if (type === 'unbind') return t('Confirm unbind')
  return t('Confirm delete')
}

function confirmDialogDescription(
  type: 'invalidate' | 'delete' | 'unbind',
  t: (key: string) => string
): string {
  if (type === 'invalidate') {
    return t(
      'After invalidating, this subscription will be immediately deactivated. Historical records are not affected. Continue?'
    )
  }
  if (type === 'unbind') {
    return t(
      'The channel returns to its previous group and the user leaves the dedicated group. Continue?'
    )
  }
  return t(
    'Deleting will permanently remove this subscription record (including benefit details). Continue?'
  )
}
