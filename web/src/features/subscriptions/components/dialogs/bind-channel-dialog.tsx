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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerMessage } from '@/lib/server-error-message'

import { bindSubscriptionChannel, getBindableChannels } from '../../api'
import {
  bindableChannelLabel,
  subscriptionBoundGroupName,
} from '../../lib/channel-binding'
import type { SubscriptionBindableChannel, UserSubscription } from '../../types'

type BindChannelDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  subscription: UserSubscription | null
  onSuccess?: () => void | Promise<void>
}

export function BindChannelDialog(props: BindChannelDialogProps) {
  const { t } = useTranslation()
  const [channels, setChannels] = useState<SubscriptionBindableChannel[]>([])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [selected, setSelected] = useState<string>('')

  const subscription = props.subscription
  const currentChannelId = Number(subscription?.bound_channel_id ?? 0)

  useEffect(() => {
    if (!props.open) return
    let cancelled = false
    setSelected('')
    setLoading(true)
    const load = async () => {
      try {
        const res = await getBindableChannels()
        if (cancelled) return
        if (res.success) {
          setChannels(res.data || [])
        } else {
          handleServerError(res)
        }
      } catch (error) {
        if (!cancelled) handleServerError(error, t('Loading failed'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [props.open, t])

  const options = useMemo(
    () =>
      channels.map((channel) => ({
        value: String(channel.id),
        label: bindableChannelLabel(channel, t),
        disabled:
          channel.bound_subscription_id > 0 &&
          channel.bound_subscription_id !== subscription?.id,
      })),
    [channels, subscription?.id, t]
  )

  const selectedChannel = channels.find((c) => String(c.id) === selected)
  // Keeping the current channel keeps its stored group, which may still use
  // the legacy oauth-ch<id> name.
  let dedicatedGroup = ''
  if (selectedChannel) {
    dedicatedGroup =
      selectedChannel.id === currentChannelId && subscription?.bound_group
        ? subscription.bound_group
        : subscriptionBoundGroupName(selectedChannel)
  }
  const channelLeavesGroup =
    selectedChannel &&
    selectedChannel.group &&
    dedicatedGroup &&
    selectedChannel.group !== dedicatedGroup

  const handleSubmit = async () => {
    if (!subscription?.id || !selected) {
      toast.error(t('Please select a channel'))
      return
    }
    setSubmitting(true)
    try {
      const res = await bindSubscriptionChannel(subscription.id, {
        channel_id: Number(selected),
      })
      if (res.success) {
        toast.success(getServerMessage(res.data) || t('Bound successfully'))
        props.onOpenChange(false)
        await props.onSuccess?.()
      } else {
        handleServerError(res)
      }
    } catch (error) {
      handleServerError(error, t('Request failed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        currentChannelId > 0 ? t('Change bound account') : t('Bind account')
      }
      description={t(
        'The channel moves into a dedicated group that only this user can use, and the user is switched to that group while the subscription is active.'
      )}
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={submitting}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={handleSubmit}
            disabled={submitting || loading || !selected}
          >
            {submitting ? t('Binding...') : t('Bind')}
          </Button>
        </>
      }
    >
      {currentChannelId > 0 ? (
        <div className='text-muted-foreground text-sm'>
          {t('Currently bound')}: #{currentChannelId}{' '}
          {subscription?.bound_channel_name || ''}
        </div>
      ) : null}
      <Combobox
        options={options}
        value={selected}
        onValueChange={(v) => setSelected(v ?? '')}
        placeholder={loading ? t('Loading...') : t('Select a channel')}
        emptyText={t('No OAuth subscription channels')}
        aria-label={t('Select a channel')}
        disabled={loading}
      />
      {channelLeavesGroup ? (
        <Alert>
          <AlertTitle>{t('The channel leaves its current group')}</AlertTitle>
          <AlertDescription>
            {t(
              'Channel {{channel}} is moved from group {{from}} to {{to}} and stops serving other users. Its original group is restored when the binding is released.',
              {
                channel: `#${selectedChannel?.id}`,
                from: selectedChannel?.group,
                to: dedicatedGroup,
              }
            )}
          </AlertDescription>
        </Alert>
      ) : null}
      <p className='text-muted-foreground text-xs leading-5'>
        {t(
          'Tokens that follow the user group or use Auto are routed to the bound account only. Tokens pinned to another group are not affected.'
        )}
      </p>
    </Dialog>
  )
}
