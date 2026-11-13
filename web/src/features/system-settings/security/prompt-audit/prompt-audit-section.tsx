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
import { FAIL_POLICY_OPTIONS } from '../audit-policy/lib/constants'
import {
  getPromptAuditConfig,
  updatePromptAuditConfig,
  type PromptAuditConfigResponse,
} from './api'
import { PromptAuditEndpointFields } from './endpoint-fields'
import {
  PROMPT_AUDIT_MODES,
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
      'Scan prompts with the Guard model and record flagged requests. Requests are only blocked or delayed by audit policy rules while the mode allows blocking.',
  },
  {
    name: 'all_groups',
    label: 'Audit all groups',
    description:
      'Covers API key requests and subscription OAuth channel groups alike. Turn off to choose groups.',
    policyScoped: true,
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
    policyScoped: true,
  },
  {
    name: 'store_pass_events',
    label: 'Record passed requests',
    description:
      'Also record an event when a scan finds nothing. This can create many events.',
  },
  {
    name: 'structure_stats_enabled',
    label: 'Request structure statistics',
    description:
      'Measurement only: counts structural features of requests and a hash fingerprint of the system prompt. It never changes audit decisions and stores only hashes and counts, never prompt text.',
  },
] as const

const POLICY_SCOPED_NOTE = 'Used only while no audit policy is saved.'

const NUMBER_FIELDS = [
  { name: 'retention_days', label: 'Retention (days)', min: 1, max: 365 },
  {
    name: 'structure_stats_retention_days',
    label: 'Structure statistics retention (days)',
    min: 1,
    max: 365,
  },
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
      // Rules derived from these settings change with them.
      void queryClient.invalidateQueries({ queryKey: ['audit-policy'] })
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
            'Which requests are scanned and what happens to findings is set by the audit policy. The Guard endpoint address is not checked against the SSRF settings because Guard models usually run on a private network; only the root user can change it.'
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
                      <FormDescription>
                        {t(item.description)}
                        {'policyScoped' in item && ` ${t(POLICY_SCOPED_NOTE)}`}
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
            name='mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Mode')}</FormLabel>
                <Select
                  items={PROMPT_AUDIT_MODES.map((mode) => ({
                    value: mode.value,
                    label: t(mode.label),
                  }))}
                  onValueChange={field.onChange}
                  value={field.value}
                >
                  <FormControl>
                    <SelectTrigger aria-label={t('Mode')}>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {PROMPT_AUDIT_MODES.map((mode) => (
                        <SelectItem key={mode.value} value={mode.value}>
                          {t(mode.label)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t(
                    'Blocking only takes effect for audit policy rules whose action is block or reroute; those requests wait for the scan. The default rules never block.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='fail_policy'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('When the Guard is unavailable')}</FormLabel>
                <Select
                  items={FAIL_POLICY_OPTIONS.map((policy) => ({
                    value: policy.value,
                    label: t(policy.label),
                  }))}
                  onValueChange={field.onChange}
                  value={field.value}
                >
                  <FormControl>
                    <SelectTrigger
                      aria-label={t('When the Guard is unavailable')}
                    >
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {FAIL_POLICY_OPTIONS.map((policy) => (
                        <SelectItem key={policy.value} value={policy.value}>
                          {t(policy.label)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t(
                    'Used by rules derived from these settings while no audit policy is saved'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
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
                  )}{' '}
                  {t(POLICY_SCOPED_NOTE)}
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
