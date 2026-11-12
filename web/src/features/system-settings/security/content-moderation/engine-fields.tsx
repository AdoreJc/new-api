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
import { useMutation } from '@tanstack/react-query'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../../components/settings-form-layout'
import { probeContentModeration } from './api'
import {
  CONTENT_MODERATION_CATEGORY_LABELS,
  splitList,
  type ContentModerationFormInput,
  type ContentModerationFormValues,
} from './lib/form'

type ContentModerationForm = UseFormReturn<
  ContentModerationFormInput,
  unknown,
  ContentModerationFormValues
>

const NUMBER_FIELDS = [
  { name: 'timeout_ms', label: 'Timeout (ms)', min: 100, max: 30000 },
  { name: 'retry_count', label: 'Retries with other keys', min: 0, max: 5 },
] as const

export function ContentModerationEngineFields(props: {
  form: ContentModerationForm
}) {
  const { t } = useTranslation()
  const probe = useMutation({
    mutationFn: () => {
      const values = props.form.getValues()
      return probeContentModeration({
        base_url: values.base_url.trim(),
        model: values.model.trim(),
        timeout_ms: Number(values.timeout_ms),
        keys: splitList(values.new_keys),
        text: 'Hello',
      })
    },
    onError: (error) => handleServerError(error),
  })
  const storedKeys = props.form.watch('stored_keys')
  const removedKeys = props.form.watch('removed_keys')

  return (
    <div className='flex flex-col gap-4 rounded-lg border p-4'>
      <div className='min-w-0'>
        <h4 className='text-sm font-medium'>{t('Moderation API')}</h4>
        <p className='text-muted-foreground text-xs'>
          {t(
            'An OpenAI-compatible /v1/moderations endpoint. Keys are used in turn; a key is paused for 1 minute after HTTP 429, 10 minutes after 401 or 403, and 10 seconds after other HTTP errors.'
          )}
        </p>
      </div>
      <SettingsFormGrid>
        <FormField
          control={props.form.control}
          name='base_url'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Address')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='https://api.openai.com' />
              </FormControl>
              <FormDescription>
                {t(
                  'Changing the address removes the saved keys unless new ones are entered'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='model'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Model')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='omni-moderation-latest' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        {NUMBER_FIELDS.map((item) => (
          <FormField
            key={item.name}
            control={props.form.control}
            name={item.name}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t(item.label)}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={item.min}
                    max={item.max}
                    {...field}
                    value={String(field.value ?? '')}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        ))}
      </SettingsFormGrid>
      <FormField
        control={props.form.control}
        name='include_images'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Send one image with the text')}</FormLabel>
              <FormDescription>
                {t(
                  'Sends at most one image of the latest user message. Models named text-moderation-* never receive images.'
                )}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch checked={field.value} onCheckedChange={field.onChange} />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />
      <FormField
        control={props.form.control}
        name='removed_keys'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Saved keys')}</FormLabel>
            <FormDescription>
              {t(
                'Keys are never shown again after saving; each is listed by a fingerprint. Tick a key to remove it on save.'
              )}
            </FormDescription>
            {storedKeys.length === 0 && (
              <p className='text-muted-foreground text-sm'>
                {t('No keys saved')}
              </p>
            )}
            <div className='flex flex-col gap-2'>
              {storedKeys.map((id) => (
                <label key={id} className='flex items-center gap-2 text-sm'>
                  <Checkbox
                    checked={(removedKeys ?? []).includes(id)}
                    onCheckedChange={(next) =>
                      field.onChange(
                        next
                          ? [...(field.value ?? []), id]
                          : (field.value ?? []).filter((value) => value !== id)
                      )
                    }
                  />
                  <span className='font-mono'>{id}</span>
                  <span className='text-muted-foreground text-xs'>
                    {t('Remove on save')}
                  </span>
                </label>
              ))}
            </div>
          </FormItem>
        )}
      />
      <FormField
        control={props.form.control}
        name='new_keys'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Add keys')}</FormLabel>
            <FormControl>
              <Textarea
                {...field}
                autoComplete='off'
                spellCheck={false}
                rows={2}
                className='font-mono'
                placeholder={t('One key per line')}
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <div className='flex flex-col gap-2'>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={probe.isPending}
            onClick={() => probe.mutate()}
          >
            {t('Test moderation API')}
          </Button>
          <span className='text-muted-foreground text-xs'>
            {t(
              'Tests the keys entered above, or the saved keys when none are entered and the address is unchanged.'
            )}
          </span>
        </div>
        {probe.data && (
          <ul role='status' className='flex flex-col gap-1 text-sm'>
            {probe.data.keys.map((key) => (
              <li
                key={key.id}
                className={
                  key.ok
                    ? 'text-emerald-600 dark:text-emerald-400'
                    : 'text-destructive break-all'
                }
              >
                <span className='font-mono'>{key.id}</span>{' '}
                {key.ok
                  ? t('responded in {{ms}} ms', { ms: key.latency_ms })
                  : t('failed: {{error}}', { error: key.error })}
              </li>
            ))}
            {probe.data.highest_category && (
              <li className='text-muted-foreground text-xs'>
                {t('Highest score: {{category}} {{score}}', {
                  category: t(
                    CONTENT_MODERATION_CATEGORY_LABELS[
                      probe.data.highest_category
                    ] ?? probe.data.highest_category
                  ),
                  score: probe.data.highest_score.toFixed(3),
                })}
              </li>
            )}
          </ul>
        )}
      </div>
    </div>
  )
}
