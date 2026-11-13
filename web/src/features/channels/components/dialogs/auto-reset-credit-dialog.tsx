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
import { FlaskConical, Save } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import {
  dryRunChannelAutoResetCredit,
  getChannelAutoResetCredit,
  updateChannelAutoResetCredit,
} from '../../api'
import {
  type AutoResetConfig,
  type AutoResetDecision,
  countAutoResetConditions,
  toEditableAutoResetConfig,
} from '../../lib/auto-reset-rule'
import {
  AutoResetDecisionPanel,
  AutoResetStatePanel,
} from './auto-reset-credit-status'
import { AutoResetRuleNode } from './auto-reset-rule-editor'

type AutoResetCreditDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelId: number
}

type NumberSetting =
  | 'min_interval_minutes'
  | 'max_per_day'
  | 'max_per_week'
  | 'skip_if_reset_within_minutes'

const NUMBER_SETTINGS: { key: NumberSetting; label: string; min: number }[] = [
  { key: 'min_interval_minutes', label: 'Minimum interval (minutes)', min: 10 },
  { key: 'max_per_day', label: 'At most per 24 hours', min: 1 },
  { key: 'max_per_week', label: 'At most per 7 days', min: 1 },
  {
    key: 'skip_if_reset_within_minutes',
    label: 'Skip when the window resets within (minutes)',
    min: 0,
  },
]

export function AutoResetCreditDialog(props: AutoResetCreditDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const queryKey = ['channels', props.channelId, 'auto-reset-credit']
  const [draft, setDraft] = useState<AutoResetConfig | null>(null)
  const [trial, setTrial] = useState<AutoResetDecision | null>(null)

  const query = useQuery({
    queryKey,
    enabled: props.open,
    queryFn: async () => {
      const res = await getChannelAutoResetCredit(props.channelId)
      if (!res.success || !res.data) {
        throw createServerError(
          res,
          t('Failed to load the automatic reset setting')
        )
      }
      return res.data
    },
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })

  useEffect(() => {
    if (query.data && draft === null) {
      setDraft(toEditableAutoResetConfig(query.data.config))
    }
  }, [query.data, draft])

  const save = useMutation({
    mutationFn: async (config: AutoResetConfig) => {
      const res = await updateChannelAutoResetCredit(props.channelId, config)
      if (!res.success || !res.data) {
        throw createServerError(
          res,
          t('Failed to save the automatic reset setting')
        )
      }
      return res.data
    },
    onSuccess: (view) => {
      queryClient.setQueryData(queryKey, view)
      setDraft(toEditableAutoResetConfig(view.config))
      toast.success(t('Automatic reset setting saved'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to save the automatic reset setting')),
  })

  const dryRun = useMutation({
    mutationFn: async (config: AutoResetConfig) => {
      const res = await dryRunChannelAutoResetCredit(props.channelId, config)
      if (!res.success || !res.data) {
        throw createServerError(res, t('Trial run failed'))
      }
      return res.data
    },
    onSuccess: setTrial,
    onError: (error) => handleServerError(error, t('Trial run failed')),
  })

  const handleOpenChange = (open: boolean) => {
    if (!open) {
      setDraft(null)
      setTrial(null)
    }
    props.onOpenChange(open)
  }

  const view = query.data
  const busy = save.isPending || dryRun.isPending
  const canEnable = draft ? countAutoResetConditions(draft.rule) > 0 : false
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone

  let body: ReactNode
  if (query.isError) {
    body = (
      <ErrorState
        title={t('Failed to load the automatic reset setting')}
        description={query.error.message}
        onRetry={() => query.refetch()}
      />
    )
  } else if (!view || !draft) {
    body = <LoadingState />
  } else {
    body = (
      <div className='flex flex-col gap-4'>
        <div className='flex items-center gap-3 rounded-lg border p-3'>
          <Switch
            id='auto-reset-enabled'
            checked={draft.enabled}
            disabled={busy || (!draft.enabled && !canEnable)}
            onCheckedChange={(checked) =>
              setDraft({ ...draft, enabled: checked })
            }
          />
          <Label
            htmlFor='auto-reset-enabled'
            className='flex flex-col items-start gap-0.5'
          >
            <span>{t('Use reset credits automatically')}</span>
            <span className='text-muted-foreground text-xs font-normal'>
              {t(
                'Sends the same redemption request as the manual reset when the conditions below match.'
              )}
            </span>
          </Label>
        </div>

        <section className='flex flex-col gap-2' aria-label={t('Conditions')}>
          <h3 className='text-sm font-medium'>{t('Conditions')}</h3>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Times use your browser time zone ({{zone}}). An alarm fires once and is then removed; an alarm missed by more than {{minutes}} minutes is removed without firing.',
              { zone: timeZone, minutes: view.alarm_grace_minutes }
            )}
          </p>
          {draft.rule ? (
            <AutoResetRuleNode
              root={draft.rule}
              node={draft.rule}
              path={[]}
              depth={1}
              weeklyWindows={view.weekly_windows}
              disabled={busy}
              onChange={(rule) => setDraft({ ...draft, rule })}
            />
          ) : null}
        </section>

        <section className='flex flex-col gap-3' aria-label={t('Safeguards')}>
          <h3 className='text-sm font-medium'>{t('Safeguards')}</h3>
          <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
            {NUMBER_SETTINGS.map((setting) => (
              <div key={setting.key} className='flex flex-col gap-1.5'>
                <Label htmlFor={`auto-reset-${setting.key}`}>
                  {t(setting.label)}
                </Label>
                <Input
                  id={`auto-reset-${setting.key}`}
                  type='number'
                  min={setting.min}
                  step={1}
                  disabled={busy}
                  value={draft[setting.key]}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      [setting.key]: Math.trunc(
                        Number(event.target.value) || 0
                      ),
                    })
                  }
                />
              </div>
            ))}
          </div>
          <div className='flex flex-col gap-2'>
            {(
              [
                [
                  'alarm_ignores_reset_soon',
                  'Alarms redeem even when the window resets soon',
                ],
                [
                  'expiring_ignores_reset_soon',
                  'Expiring credits are redeemed even when the window resets soon',
                ],
              ] as const
            ).map(([key, label]) => (
              <div key={key} className='flex items-center gap-2'>
                <Switch
                  id={`auto-reset-${key}`}
                  size='sm'
                  checked={draft[key]}
                  disabled={busy}
                  onCheckedChange={(checked) =>
                    setDraft({ ...draft, [key]: checked })
                  }
                />
                <Label htmlFor={`auto-reset-${key}`} className='font-normal'>
                  {t(label)}
                </Label>
              </div>
            ))}
          </div>
        </section>

        <AutoResetStatePanel view={view} />
        {trial ? (
          <AutoResetDecisionPanel decision={trial} title={t('Trial result')} />
        ) : null}
        {view.last_decision ? (
          <AutoResetDecisionPanel
            decision={view.last_decision}
            title={t('Last evaluation on this node')}
          />
        ) : null}
      </div>
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={t('Automatic reset credit')}
      description={t(
        "Redeem one of this account's reset credits automatically when the conditions match."
      )}
      contentClassName='sm:max-w-[820px]'
      bodyClassName='flex flex-col gap-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            disabled={!draft || busy}
            onClick={() => draft && dryRun.mutate(draft)}
          >
            <FlaskConical data-icon='inline-start' />
            {t('Trial run')}
          </Button>
          <Button
            type='button'
            disabled={!draft || busy}
            onClick={() => draft && save.mutate(draft)}
          >
            <Save data-icon='inline-start' />
            {t('Save')}
          </Button>
        </>
      }
    >
      {body}
    </Dialog>
  )
}
