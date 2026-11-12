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
import { useState } from 'react'
import { useFieldArray, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { PasswordInput } from '@/components/password-input'
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
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../../components/settings-form-layout'
import { probePromptAuditEndpoint, type PromptAuditProbeResult } from './api'
import {
  newEndpointFormValues,
  type PromptAuditFormInput,
  type PromptAuditFormValues,
} from './lib/form'

type PromptAuditForm = UseFormReturn<
  PromptAuditFormInput,
  unknown,
  PromptAuditFormValues
>

export function PromptAuditEndpointFields(props: { form: PromptAuditForm }) {
  const { t } = useTranslation()
  const endpoints = useFieldArray({
    control: props.form.control,
    name: 'endpoints',
  })
  const [probes, setProbes] = useState<Record<string, PromptAuditProbeResult>>(
    {}
  )
  const probe = useMutation({
    mutationFn: async (index: number) => {
      const endpoint = props.form.getValues(`endpoints.${index}`)
      const result = await probePromptAuditEndpoint(
        {
          id: endpoint.id,
          name: endpoint.name,
          base_url: endpoint.base_url,
          model: endpoint.model,
          timeout_ms: Number(endpoint.timeout_ms),
          input_limit: Number(endpoint.input_limit),
          enabled: endpoint.enabled,
        },
        endpoint.clear_token ? '' : endpoint.token
      )
      return { id: endpoint.id, result }
    },
    onSuccess: (data) =>
      setProbes((previous) => ({ ...previous, [data.id]: data.result })),
    onError: (error) => handleServerError(error),
  })
  const endpointsError = props.form.formState.errors.endpoints?.root?.message
  const values = props.form.watch('endpoints')

  return (
    <div className='flex flex-col gap-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0'>
          <h4 className='text-sm font-medium'>{t('Guard endpoints')}</h4>
          <p className='text-muted-foreground text-xs'>
            {t(
              'OpenAI-compatible chat endpoints serving Qwen3Guard, tried in order. Private and local addresses are allowed.'
            )}
          </p>
        </div>
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={endpoints.fields.length >= 8}
          onClick={() =>
            endpoints.append(
              newEndpointFormValues(`guard-${Date.now().toString(36)}`)
            )
          }
        >
          {t('Add endpoint')}
        </Button>
      </div>
      {endpointsError && (
        <p role='alert' className='text-destructive text-sm'>
          {t(endpointsError)}
        </p>
      )}
      {endpoints.fields.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No Guard endpoints configured')}
        </p>
      )}
      {endpoints.fields.map((field, index) => {
        const endpoint = values?.[index]
        const result = endpoint ? probes[endpoint.id] : undefined
        return (
          <div
            key={field.id}
            className='flex flex-col gap-4 rounded-lg border p-4'
          >
            <SettingsFormGrid>
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.name`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Name')}</FormLabel>
                    <FormControl>
                      <Input {...input} placeholder={t('Local Guard')} />
                    </FormControl>
                  </FormItem>
                )}
              />
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.base_url`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Address')}</FormLabel>
                    <FormControl>
                      <Input {...input} placeholder='http://127.0.0.1:11434' />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Changing the address removes the saved token unless a new one is entered'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.model`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Model')}</FormLabel>
                    <FormControl>
                      <Input {...input} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.token`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Bearer token')}</FormLabel>
                    <FormControl>
                      <PasswordInput
                        {...input}
                        autoComplete='new-password'
                        placeholder={
                          endpoint?.has_token
                            ? t('Enter a new key to update')
                            : t('Optional')
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.timeout_ms`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Timeout (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={100}
                        max={30000}
                        {...input}
                        value={String(input.value ?? '')}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.input_limit`}
                render={({ field: input }) => (
                  <FormItem>
                    <FormLabel>{t('Characters per scan')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={128}
                        max={100000}
                        {...input}
                        value={String(input.value ?? '')}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
            <FormField
              control={props.form.control}
              name={`endpoints.${index}.enabled`}
              render={({ field: input }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Use this endpoint')}</FormLabel>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={input.value}
                      onCheckedChange={input.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            {endpoint?.has_token && (
              <FormField
                control={props.form.control}
                name={`endpoints.${index}.clear_token`}
                render={({ field: input }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Remove saved token')}</FormLabel>
                      <FormDescription>
                        {t('A token is saved for this endpoint')}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={input.value}
                        onCheckedChange={input.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            )}
            <div className='flex flex-wrap items-center gap-2'>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={probe.isPending}
                onClick={() => probe.mutate(index)}
              >
                {t('Test endpoint')}
              </Button>
              <Button
                type='button'
                size='sm'
                variant='ghost'
                onClick={() => endpoints.remove(index)}
              >
                {t('Remove endpoint')}
              </Button>
              {result && (
                <span
                  role='status'
                  className={
                    result.ok
                      ? 'text-sm text-emerald-600 dark:text-emerald-400'
                      : 'text-destructive text-sm break-all'
                  }
                >
                  {result.ok
                    ? t('Endpoint responded in {{ms}} ms', {
                        ms: result.latency_ms,
                      })
                    : t('Endpoint test failed: {{error}}', {
                        error: result.error,
                      })}
                </span>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}
