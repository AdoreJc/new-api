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
import { Plus, Radar, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useFieldArray, useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { PasswordInput } from '@/components/password-input'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { SettingsControlGroup } from '@/features/system-settings/components/settings-form-layout'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { probeContentAuditEndpoint } from '../api'
import { CONTENT_AUDIT_MAX_ENDPOINTS } from '../constants'
import {
  createContentAuditEndpointId,
  type ContentAuditFormValues,
} from '../lib/config-form'
import type { ContentAuditProbeResult } from '../types'

function GuardEndpointRow(props: { index: number; onRemove: () => void }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const form = useFormContext<ContentAuditFormValues>()
  const prefix = `endpoints.${props.index}` as const
  const endpoint = useWatch({ control: form.control, name: prefix })
  const [probeResult, setProbeResult] =
    useState<ContentAuditProbeResult | null>(null)
  const probe = useMutation({
    mutationFn: probeContentAuditEndpoint,
    onSuccess: setProbeResult,
    onError: (error) => {
      setProbeResult(null)
      handleServerError(error, t('Failed to probe guard endpoint'))
    },
  })

  const handleProbe = async () => {
    const valid = await form.trigger([`${prefix}.base_url`, `${prefix}.model`])
    if (!valid) return
    const current = form.getValues(prefix)
    const token = current.token.trim()
    probe.mutate({
      endpoint: {
        id: current.id,
        base_url: current.base_url.trim(),
        model: current.model.trim(),
        ...(token ? { token } : {}),
      },
    })
  }

  const title = endpoint?.name?.trim() || t('Guard endpoint')
  const tokenStored = Boolean(endpoint?.has_token)
  const clearingToken = Boolean(endpoint?.clear_token)

  return (
    <SettingsControlGroup className='space-y-4 py-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0'>
          <p className='truncate text-sm font-medium'>{title}</p>
          <p className='text-muted-foreground font-mono text-xs'>
            {endpoint?.id}
          </p>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <FormField
            control={form.control}
            name={`${prefix}.enabled`}
            render={({ field }) => (
              <FormItem className='flex flex-row items-center gap-2'>
                <FormLabel className='text-xs'>{t('Enabled')}</FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => void handleProbe()}
            disabled={probe.isPending}
          >
            <Radar data-icon='inline-start' />
            {probe.isPending ? t('Probing...') : t('Probe')}
          </Button>
          <Button
            type='button'
            size='icon'
            variant='ghost'
            className='size-8'
            aria-label={t('Remove guard endpoint')}
            onClick={props.onRemove}
          >
            <Trash2 aria-hidden='true' />
          </Button>
        </div>
      </div>

      <div className='grid min-w-0 gap-4 lg:grid-cols-2'>
        <FormField
          control={form.control}
          name={`${prefix}.name`}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Name')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder={t('Optional display name')} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name={`${prefix}.model`}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Model')}</FormLabel>
              <FormControl>
                <Input {...field} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name={`${prefix}.base_url`}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Base URL')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='https://' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name={`${prefix}.token`}
          render={({ field }) => (
            <FormItem>
              <div className='flex flex-wrap items-center gap-2'>
                <FormLabel>{t('Token')}</FormLabel>
                {tokenStored && !clearingToken && (
                  <StatusBadge
                    label={t('Configured')}
                    variant='success'
                    copyable={false}
                  />
                )}
                {clearingToken && (
                  <StatusBadge
                    label={t('Will be cleared on save')}
                    variant='warning'
                    copyable={false}
                  />
                )}
              </div>
              <div className='flex min-w-0 items-center gap-2'>
                <FormControl>
                  <PasswordInput
                    {...field}
                    autoComplete='new-password'
                    className='min-w-0 flex-1'
                    placeholder={
                      tokenStored
                        ? t('Enter new secret to update')
                        : t('Optional')
                    }
                  />
                </FormControl>
                {tokenStored && (
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    onClick={() =>
                      form.setValue(`${prefix}.clear_token`, !clearingToken, {
                        shouldDirty: true,
                      })
                    }
                  >
                    {clearingToken ? t('Undo') : t('Clear token')}
                  </Button>
                )}
              </div>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>

      {probeResult && (
        <div
          role='status'
          className='flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'
        >
          <StatusBadge
            label={probeResult.ok ? t('Reachable') : t('Failed')}
            variant={probeResult.ok ? 'success' : 'danger'}
            copyable={false}
          />
          <span className='text-muted-foreground tabular-nums'>
            {t('{{value}} ms', {
              value: formatNumber(probeResult.latency_ms, locale),
            })}
          </span>
          <span className='text-muted-foreground'>
            {probeResult.model_listed
              ? t('Model listed by endpoint')
              : t('Model not listed by endpoint')}
          </span>
          {probeResult.safety && (
            <span className='text-muted-foreground'>
              {t('Probe verdict: {{safety}}', { safety: probeResult.safety })}
            </span>
          )}
          {probeResult.error && (
            <span className='text-destructive break-all'>
              {probeResult.error}
            </span>
          )}
        </div>
      )}
    </SettingsControlGroup>
  )
}

export function GuardEndpointsEditor() {
  const { t } = useTranslation()
  const form = useFormContext<ContentAuditFormValues>()
  const endpoints = useFieldArray({
    control: form.control,
    name: 'endpoints',
    // The endpoint `id` is business data; keep RHF's row key separate.
    keyName: 'rowKey',
  })
  const atLimit = endpoints.fields.length >= CONTENT_AUDIT_MAX_ENDPOINTS

  return (
    <div data-settings-form-span='full' className='min-w-0 space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0 space-y-0.5'>
          <p className='text-sm font-medium'>{t('Guard endpoints')}</p>
          <p className='text-muted-foreground text-xs'>
            {t(
              'OpenAI-compatible safety model endpoints. Tokens are write-only and never shown again after saving.'
            )}
          </p>
        </div>
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={atLimit}
          onClick={() =>
            endpoints.append({
              id: createContentAuditEndpointId(),
              name: '',
              base_url: '',
              model: '',
              enabled: true,
              has_token: false,
              token: '',
              clear_token: false,
            })
          }
        >
          <Plus data-icon='inline-start' />
          {t('Add endpoint')}
        </Button>
      </div>
      {atLimit && (
        <p className='text-muted-foreground text-xs'>
          {t('You can add up to {{count}} guard endpoints.', {
            count: CONTENT_AUDIT_MAX_ENDPOINTS,
          })}
        </p>
      )}
      {endpoints.fields.length === 0 && (
        <EmptyState
          icon={Radar}
          bordered
          className='min-h-0 py-6'
          title={t('No guard endpoints configured.')}
          description={t(
            'Add and enable at least one endpoint before enabling content audit.'
          )}
        />
      )}
      {endpoints.fields.map((field, index) => (
        <GuardEndpointRow
          key={field.rowKey}
          index={index}
          onRemove={() => endpoints.remove(index)}
        />
      ))}
    </div>
  )
}
