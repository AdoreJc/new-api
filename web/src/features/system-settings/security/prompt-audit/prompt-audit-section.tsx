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
import { Fragment, useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { TagInput } from '@/components/tag-input'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsControlChildren,
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../../components/settings-form-layout'
import { SettingsPageFormActions } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import {
  getPromptAuditConfig,
  updatePromptAuditConfig,
  type PromptAuditConfigResponse,
} from './api'
import { PromptAuditEndpointFields } from './endpoint-fields'
import {
  PROMPT_AUDIT_SCAN_SCOPES,
  PROMPT_AUDIT_SCANNER_LABELS,
  promptAuditFormSchema,
  toPromptAuditConfigRequest,
  toPromptAuditFormValues,
  type PromptAuditFormInput,
  type PromptAuditFormValues,
} from './lib/form'
import { PromptAuditRuntimeCard } from './runtime-card'

const CONFIG_QUERY_KEY = ['prompt-audit', 'config'] as const

const SWITCHES = [
  {
    name: 'enabled',
    label: 'Enable prompt audit',
    description:
      'Scan prompts in the background with the Guard model and record flagged requests. Requests are never blocked or delayed.',
  },
  {
    name: 'all_groups',
    label: 'Audit all groups',
    description:
      'Covers API key requests and subscription OAuth channel groups alike. Turn off to choose groups.',
  },
  {
    name: 'store_full_prompt',
    label: 'Store the full prompt of flagged requests',
    description:
      'Turn off to keep only a redacted preview and a hash. Only the root user can view full prompts.',
  },
  {
    name: 'strict_jailbreak_mapping',
    label: 'Treat controversial jailbreak results as critical',
    description:
      'Off by default: a controversial jailbreak verdict is recorded as flagged, because coding agent prompts often get it. Unsafe results are not affected.',
  },
] as const

const NUMBER_FIELDS = [
  { name: 'retention_days', label: 'Retention (days)', min: 1, max: 365 },
  { name: 'worker_count', label: 'Concurrent scans', min: 1, max: 8 },
  { name: 'queue_capacity', label: 'Queue capacity', min: 1, max: 10000 },
  {
    name: 'max_chunks_per_request',
    label: 'Guard calls per request',
    min: 1,
    max: 100,
  },
] as const

export function PromptAuditSection() {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: CONFIG_QUERY_KEY,
    queryFn: getPromptAuditConfig,
  })
  if (config.isPending) return <LoadingState />
  if (config.isError || !config.data) {
    return (
      <ErrorState
        title={t('Failed to load prompt audit settings')}
        onRetry={() => void config.refetch()}
      />
    )
  }
  return (
    <PromptAuditForm
      data={config.data}
      onSaveFailed={() => void config.refetch()}
    />
  )
}

// The form mounts only with loaded settings so every control starts
// controlled with its saved value.
function PromptAuditForm(props: {
  data: PromptAuditConfigResponse
  onSaveFailed: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm<PromptAuditFormInput, unknown, PromptAuditFormValues>({
    resolver: zodResolver(promptAuditFormSchema),
    defaultValues: toPromptAuditFormValues(props.data),
  })
  useEffect(() => {
    form.reset(toPromptAuditFormValues(props.data))
  }, [props.data, form])

  const save = useMutation({
    mutationFn: (values: PromptAuditFormValues) =>
      updatePromptAuditConfig(
        toPromptAuditConfigRequest(values, props.data.config)
      ),
    onSuccess: (data) => {
      queryClient.setQueryData(CONFIG_QUERY_KEY, data)
      void queryClient.invalidateQueries({
        queryKey: ['prompt-audit', 'runtime'],
      })
      toast.success(t('Prompt audit settings saved'))
    },
    onError: (error) => {
      handleServerError(error)
      props.onSaveFailed()
    },
  })
  const onSubmit = form.handleSubmit((values) => save.mutate(values))
  const allGroups = form.watch('all_groups')
  const scanScope = form.watch('scan_scope')
  const scanScopeDescription = PROMPT_AUDIT_SCAN_SCOPES.find(
    (scope) => scope.value === scanScope
  )?.description

  return (
    <SettingsSection title={t('Prompt Audit')}>
      <Alert>
        <AlertDescription>
          {t(
            'Prompt audit only records findings in this version. The Guard endpoint address is not checked against the SSRF settings because Guard models usually run on a private network; only the root user can change it.'
          )}
        </AlertDescription>
      </Alert>
      <Form {...form}>
        <SettingsForm onSubmit={onSubmit}>
          <SettingsPageFormActions
            onSave={onSubmit}
            isSaving={save.isPending}
            saveLabel='Save prompt audit settings'
          />
          {SWITCHES.map((item) => (
            <Fragment key={item.name}>
              <FormField
                control={form.control}
                name={item.name}
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t(item.label)}</FormLabel>
                      <FormDescription>{t(item.description)}</FormDescription>
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
              {item.name === 'all_groups' && !allGroups && (
                <SettingsControlChildren>
                  <FormField
                    control={form.control}
                    name='groups'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Audited groups')}</FormLabel>
                        <FormControl>
                          <TagInput
                            value={field.value ?? []}
                            onChange={field.onChange}
                            placeholder={t(
                              'Group name, or a prefix ending in *'
                            )}
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Press Enter after each group. A trailing * matches a prefix, for example claude-ch* for all Claude subscription groups.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </SettingsControlChildren>
              )}
            </Fragment>
          ))}
          <FormField
            control={form.control}
            name='scan_scope'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Scan scope')}</FormLabel>
                <Select
                  items={PROMPT_AUDIT_SCAN_SCOPES.map((scope) => ({
                    value: scope.value,
                    label: t(scope.label),
                  }))}
                  onValueChange={field.onChange}
                  value={field.value}
                >
                  <FormControl>
                    <SelectTrigger aria-label={t('Scan scope')}>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {PROMPT_AUDIT_SCAN_SCOPES.map((scope) => (
                        <SelectItem key={scope.value} value={scope.value}>
                          {t(scope.label)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {scanScopeDescription
                    ? `${t(scanScopeDescription)} ${t('Stored prompts always cover the whole request.')}`
                    : t('Stored prompts always cover the whole request.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='scanners'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Risk categories')}</FormLabel>
                <FormDescription>
                  {t(
                    'Unsafe results in a selected category are recorded as critical; other unsafe or controversial results are recorded as flagged.'
                  )}
                </FormDescription>
                <div className='grid gap-2 sm:grid-cols-2'>
                  {props.data.scanners.map((scanner) => {
                    const checked = (field.value ?? []).includes(scanner)
                    return (
                      <label
                        key={scanner}
                        className='flex items-center gap-2 text-sm'
                      >
                        <Checkbox
                          checked={checked}
                          onCheckedChange={(next) =>
                            field.onChange(
                              next
                                ? [...(field.value ?? []), scanner]
                                : (field.value ?? []).filter(
                                    (value) => value !== scanner
                                  )
                            )
                          }
                        />
                        {t(PROMPT_AUDIT_SCANNER_LABELS[scanner] ?? scanner)}
                      </label>
                    )
                  })}
                </div>
              </FormItem>
            )}
          />
          <SettingsFormGrid>
            {NUMBER_FIELDS.map((item) => (
              <FormField
                key={item.name}
                control={form.control}
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
          <PromptAuditEndpointFields form={form} />
        </SettingsForm>
      </Form>
      <PromptAuditRuntimeCard />
    </SettingsSection>
  )
}
