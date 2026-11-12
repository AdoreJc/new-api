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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Info } from 'lucide-react'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { FormDirtyIndicator } from '@/features/system-settings/components/form-dirty-indicator'
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '@/features/system-settings/components/settings-form-layout'
import { SettingsPageFormActions } from '@/features/system-settings/components/settings-page-context'
import { SettingsSection } from '@/features/system-settings/components/settings-section'
import { getGroups } from '@/features/users/api'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import {
  handleServerError,
  markServerErrorHandled,
} from '@/lib/handle-server-error'
import {
  getServerErrorStatus,
  requireServerSuccess,
} from '@/lib/server-error-message'

import { getContentAuditConfig, updateContentAuditConfig } from '../api'
import {
  CONTENT_AUDIT_QUERY_KEYS,
  contentAuditScannerLabelKey,
} from '../constants'
import {
  buildContentAuditFormValues,
  buildContentAuditSaveRequest,
  contentAuditConfigSchema,
  type ContentAuditFormValues,
} from '../lib/config-form'
import type { ContentAuditConfigResponse } from '../types'
import { ContentAuditRuntimeCard } from './content-audit-runtime-card'
import { GuardEndpointsEditor } from './guard-endpoints-editor'

type NumericFieldName =
  | 'worker_count'
  | 'queue_capacity'
  | 'queue_max_mb'
  | 'timeout_ms'
  | 'max_chunk_runes'
  | 'max_body_mb'
  | 'retention_days'

const NUMERIC_FIELDS: {
  name: NumericFieldName
  labelKey: string
  descriptionKey: string
  min: number
  max: number
}[] = [
  {
    name: 'worker_count',
    labelKey: 'Worker count',
    descriptionKey: 'Background workers that scan queued requests.',
    min: 1,
    max: 8,
  },
  {
    name: 'queue_capacity',
    labelKey: 'Queue capacity',
    descriptionKey:
      'Maximum queued requests. New requests are skipped when the queue is full.',
    min: 1,
    max: 20000,
  },
  {
    name: 'queue_max_mb',
    labelKey: 'Queue memory limit (MB)',
    descriptionKey: 'Maximum memory held by queued request bodies.',
    min: 1,
    max: 1024,
  },
  {
    name: 'timeout_ms',
    labelKey: 'Guard timeout (ms)',
    descriptionKey: 'Timeout for each call to a guard endpoint.',
    min: 100,
    max: 30000,
  },
  {
    name: 'max_chunk_runes',
    labelKey: 'Maximum chunk length (characters)',
    descriptionKey: 'Long prompts are split into chunks of this size.',
    min: 128,
    max: 100000,
  },
  {
    name: 'max_body_mb',
    labelKey: 'Maximum request body (MB)',
    descriptionKey: 'Larger request bodies are skipped.',
    min: 1,
    max: 64,
  },
  {
    name: 'retention_days',
    labelKey: 'Event retention (days)',
    descriptionKey: 'Audit events older than this are deleted automatically.',
    min: 1,
    max: 365,
  },
]

function ContentAuditConfigForm(props: {
  data: ContentAuditConfigResponse
  onReload: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const form = useForm<ContentAuditFormValues>({
    resolver: zodResolver(contentAuditConfigSchema),
    mode: 'onChange',
    defaultValues: buildContentAuditFormValues(props.data.config),
  })
  const allGroups = form.watch('all_groups')

  useEffect(() => {
    form.reset(buildContentAuditFormValues(props.data.config))
  }, [props.data.config, form])

  const groupsQuery = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
  })
  const groupOptions = (groupsQuery.data?.data ?? []).map((group) => ({
    label: group,
    value: group,
  }))

  const save = useMutation({
    mutationFn: (values: ContentAuditFormValues) =>
      updateContentAuditConfig(
        buildContentAuditSaveRequest(values, props.data.config)
      ),
    onSuccess: (data) => {
      queryClient.setQueryData(CONTENT_AUDIT_QUERY_KEYS.config, data)
      void queryClient.invalidateQueries({
        queryKey: CONTENT_AUDIT_QUERY_KEYS.runtime,
      })
      toast.success(t('Content audit settings saved'))
    },
    onError: (error) => {
      if (getServerErrorStatus(error) === 409) {
        markServerErrorHandled(error)
        toast.error(
          t(
            'Content audit configuration changed elsewhere. Reload and try again.'
          ),
          { action: { label: t('Reload'), onClick: props.onReload } }
        )
        return
      }
      handleServerError(error, t('Failed to save content audit settings'))
    },
  })

  const onSubmit = form.handleSubmit((values) => save.mutate(values))

  return (
    <>
      <FormNavigationGuard when={form.formState.isDirty} />
      <Form {...form}>
        <SettingsForm onSubmit={onSubmit}>
          <SettingsPageFormActions
            onSave={() => void onSubmit()}
            onReset={() =>
              form.reset(buildContentAuditFormValues(props.data.config))
            }
            isSaving={save.isPending}
            isResetDisabled={!form.formState.isDirty}
            saveLabel='Save content audit settings'
          />
          <FormDirtyIndicator isDirty={form.formState.isDirty} />
          <Alert>
            <Info aria-hidden='true' />
            <AlertDescription>
              {t(
                'Shadow mode: requests are scanned asynchronously after they are accepted and results are only recorded. Content audit never blocks or delays a request, and prompt text is never stored.'
              )}
            </AlertDescription>
          </Alert>

          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable content audit')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Record warn and block verdicts for matching requests. Off by default.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='all_groups'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Audit all groups')}</FormLabel>
                  <FormDescription>
                    {t('When off, only the groups listed below are audited.')}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='groups'
            render={({ field }) => (
              <FormItem data-settings-form-span='full'>
                <FormLabel>{t('Audited groups')}</FormLabel>
                <FormControl>
                  <MultiSelect
                    options={groupOptions}
                    selected={field.value}
                    onChange={field.onChange}
                    allowCreate
                    disabled={allGroups}
                    placeholder={t('Select or enter groups')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'End a name with * to match a prefix, for example claude-ch*.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='scanners'
            render={({ field }) => (
              <FormItem data-settings-form-span='full'>
                <FormLabel>{t('Scanners')}</FormLabel>
                <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
                  {props.data.scanners.map((scanner) => {
                    const id = `content-audit-scanner-${scanner}`
                    return (
                      <div key={scanner} className='flex items-center gap-2'>
                        <Checkbox
                          id={id}
                          checked={field.value.includes(scanner)}
                          onCheckedChange={(checked) =>
                            field.onChange(
                              checked
                                ? [...field.value, scanner]
                                : field.value.filter((item) => item !== scanner)
                            )
                          }
                        />
                        <Label htmlFor={id} className='text-sm font-normal'>
                          {t(contentAuditScannerLabelKey(scanner))}
                        </Label>
                      </div>
                    )
                  })}
                </div>
                <FormDescription>
                  {t(
                    'Categories reported by the guard model that are recorded.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {NUMERIC_FIELDS.map((config) => (
            <FormField
              key={config.name}
              control={form.control}
              name={config.name}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t(config.labelKey)}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={config.min}
                      max={config.max}
                      step={1}
                      name={field.name}
                      ref={field.ref}
                      onBlur={field.onBlur}
                      value={Number.isNaN(field.value) ? '' : field.value}
                      onChange={(event) =>
                        field.onChange(
                          event.target.value === ''
                            ? Number.NaN
                            : Number(event.target.value)
                        )
                      }
                    />
                  </FormControl>
                  <FormDescription>
                    {t(config.descriptionKey)}{' '}
                    {t('Allowed range: {{min}}–{{max}}', {
                      min: formatNumber(config.min, locale),
                      max: formatNumber(config.max, locale),
                    })}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}

          <GuardEndpointsEditor />
        </SettingsForm>
      </Form>
    </>
  )
}

export function ContentAuditSettingsSection() {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: CONTENT_AUDIT_QUERY_KEYS.config,
    queryFn: getContentAuditConfig,
  })

  let content
  if (config.isPending) {
    content = <LoadingState message={t('Loading settings...')} />
  } else if (config.isError) {
    content = (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void config.refetch()}
      />
    )
  } else {
    content = (
      <ContentAuditConfigForm
        data={config.data}
        onReload={() => void config.refetch()}
      />
    )
  }

  return (
    <SettingsSection title={t('Content Audit')}>
      {content}
      <ContentAuditRuntimeCard />
    </SettingsSection>
  )
}
