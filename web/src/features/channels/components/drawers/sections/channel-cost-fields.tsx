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
import type { ReactNode } from 'react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { formatTimestamp } from '@/lib/format'

import { CHANNEL_TYPE_CLAUDE_OAUTH } from '../../../constants'
import {
  defaultChannelCostMode,
  unixToDateInput,
  type ChannelFormValues,
} from '../../../lib'
import type { Channel } from '../../../types'

const CODEX_CHANNEL_TYPE = 57

type ChannelCostFieldsProps = {
  form: UseFormReturn<ChannelFormValues>
  channelType: number
  // The saved channel, for the read-only subscription values.
  savedChannel?: Channel | null
  heading: ReactNode
}

type DateFieldName = 'cost_effective_from' | 'cost_effective_to'

export function ChannelCostFields(props: ChannelCostFieldsProps) {
  const { t } = useTranslation()
  const storedMode = props.form.watch('cost_mode')
  const mode = storedMode || defaultChannelCostMode(props.channelType)
  const isCodex = props.channelType === CODEX_CHANNEL_TYPE
  const subscription = props.savedChannel
  const autoDates: Record<DateFieldName, string> = {
    cost_effective_from: isCodex
      ? unixToDateInput(subscription?.subscription_active_start)
      : '',
    cost_effective_to: isCodex
      ? unixToDateInput(subscription?.subscription_active_until, true)
      : '',
  }

  const renderDateField = (name: DateFieldName, label: string) => (
    <FormField
      control={props.form.control}
      name={name}
      render={({ field }) => {
        const manual = field.value !== ''
        const auto = autoDates[name]
        let source: string | null = null
        if (manual) {
          source = t('Manual')
        } else if (auto) {
          source = t('Automatic')
        }
        return (
          <FormItem>
            <div className='flex items-center gap-2'>
              <FormLabel>{label}</FormLabel>
              {source && <Badge variant='outline'>{source}</Badge>}
            </div>
            <div className='flex items-center gap-2'>
              <FormControl>
                <Input
                  type='date'
                  {...field}
                  value={manual ? field.value : auto}
                  onChange={(e) => field.onChange(e.target.value)}
                />
              </FormControl>
              {manual && auto && (
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => field.onChange('')}
                >
                  {t('Restore automatic')}
                </Button>
              )}
            </div>
            <FormMessage />
          </FormItem>
        )
      }}
    />
  )

  return (
    <div
      role='group'
      aria-label={t('Cost accounting')}
      className='flex flex-col gap-4'
    >
      {props.heading}
      <FormField
        control={props.form.control}
        name='cost_mode'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Cost mode')}</FormLabel>
            <Select
              value={mode}
              onValueChange={(value) => field.onChange(value ?? '')}
            >
              <FormControl>
                <SelectTrigger className='w-full'>
                  <SelectValue>
                    {mode === 'fixed_monthly'
                      ? t('Fixed monthly cost')
                      : t('Ratio of list price')}
                  </SelectValue>
                </SelectTrigger>
              </FormControl>
              <SelectContent>
                <SelectItem value='ratio'>
                  {t('Ratio of list price')}
                </SelectItem>
                <SelectItem value='fixed_monthly'>
                  {t('Fixed monthly cost')}
                </SelectItem>
              </SelectContent>
            </Select>
            <FormDescription>
              {t(
                'Subscription accounts usually cost a fixed amount per month; API keys usually cost a ratio of the list price.'
              )}
            </FormDescription>
          </FormItem>
        )}
      />

      {mode === 'ratio' ? (
        <FormField
          control={props.form.control}
          name='cost_ratio'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Channel cost ratio')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  inputMode='decimal'
                  min={0}
                  max={10}
                  step='0.01'
                  placeholder='1'
                  {...field}
                  onChange={(e) => field.onChange(Number(e.target.value))}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'What this channel costs relative to the list price. Channel cost = list price x this ratio. Changes apply to new requests only.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      ) : (
        <>
          <FormField
            control={props.form.control}
            name='monthly_cost_usd'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Monthly cost (USD)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    inputMode='decimal'
                    min={0}
                    step='0.01'
                    placeholder={t('Not entered')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Amortized per day on the cost dashboard: monthly cost / days in the month. Leave empty until known; it is then shown as not entered instead of counting as 0.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='grid gap-4 sm:grid-cols-2'>
            {renderDateField('cost_effective_from', t('Cost starts'))}
            {renderDateField('cost_effective_to', t('Cost ends'))}
          </div>
          {isCodex && (
            <p className='text-muted-foreground text-xs'>
              {subscription?.subscription_synced_at
                ? t(
                    'Plan {{plan}}. Subscription dates are read from the account when it is saved or its credential is refreshed (last read {{time}}). Manual dates take priority.',
                    {
                      plan: subscription.subscription_plan_type || '-',
                      time: formatTimestamp(
                        subscription.subscription_synced_at
                      ),
                    }
                  )
                : t(
                    'Subscription dates are read from the account when it is saved or its credential is refreshed. Manual dates take priority.'
                  )}
            </p>
          )}
          {props.channelType === CHANNEL_TYPE_CLAUDE_OAUTH && (
            <p className='text-muted-foreground text-xs'>
              {t(
                'Claude accounts do not report their subscription dates, so enter the start and end dates by hand.'
              )}
            </p>
          )}
        </>
      )}
    </div>
  )
}
