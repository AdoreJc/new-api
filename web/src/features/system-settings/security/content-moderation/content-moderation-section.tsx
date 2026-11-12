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
  getContentModerationConfig,
  RUNTIME_QUERY_KEY,
  updateContentModerationConfig,
  type ContentModerationConfigResponse,
} from './api'
import { ContentModerationEngineFields } from './engine-fields'
import { ContentModerationKeywordFields } from './keyword-fields'
import {
  CONTENT_MODERATION_CATEGORY_LABELS,
  contentModerationFormSchema,
  toContentModerationConfigRequest,
  toContentModerationFormValues,
  type ContentModerationFormInput,
  type ContentModerationFormValues,
} from './lib/form'
import { ContentModerationRuntimeCard } from './runtime-card'

const CONFIG_QUERY_KEY = ['content-moderation', 'config'] as const

const MODES = [
  { value: 'off', label: 'Off' },
  { value: 'observe', label: 'Observe (check in the background, never block)' },
  { value: 'pre_block', label: 'Block (check before billing, reject flagged)' },
] as const

const MODEL_FILTERS = [
  { value: 'all', label: 'All models' },
  { value: 'include', label: 'Only these models' },
  { value: 'exclude', label: 'All except these models' },
] as const

const SWITCHES = [
  {
    name: 'all_groups',
    label: 'Moderate all groups',
    description: 'Turn off to choose groups.',
  },
  {
    name: 'hash_blocklist_enabled',
    label: 'Remember flagged inputs',
    description:
      'Stores the hash of every flagged input. The same input is then blocked (or recorded in observe mode) without calling the API.',
  },
  {
    name: 'record_non_hits',
    label: 'Also record passed inputs and failed calls',
    description:
      'Flagged inputs are always recorded. Other records are kept for at most 3 days.',
  },
  {
    name: 'email_on_hit',
    label: 'Email the user on flagged input',
    description:
      'At most one notice per user per hour. The notice names no category or content.',
  },
  {
    name: 'auto_disable_enabled',
    label: 'Disable users automatically',
    description:
      'Disables a user (never an administrator) once the threshold is reached within the window, emails the user and writes the audit log. The count restarts after each automatic disable.',
  },
] as const

const NUMBER_FIELDS = [
  { name: 'sample_rate', label: 'Sample rate (%)', min: 0, max: 100 },
  {
    name: 'disable_threshold',
    label: 'Flags before disabling',
    min: 1,
    max: 10000,
  },
  {
    name: 'violation_window_hours',
    label: 'Counting window (hours)',
    min: 1,
    max: 8760,
  },
  {
    name: 'hit_retention_days',
    label: 'Keep flagged records (days)',
    min: 1,
    max: 3650,
  },
  {
    name: 'non_hit_retention_days',
    label: 'Keep other records (days)',
    min: 1,
    max: 3,
  },
  { name: 'worker_count', label: 'Background workers', min: 1, max: 32 },
  { name: 'queue_size', label: 'Background queue size', min: 1, max: 10000 },
] as const

export function ContentModerationSection() {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: CONFIG_QUERY_KEY,
    queryFn: getContentModerationConfig,
  })
  if (config.isPending) return <LoadingState />
  if (config.isError || !config.data) {
    return (
      <ErrorState
        title={t('Failed to load content moderation settings')}
        onRetry={() => void config.refetch()}
      />
    )
  }
  return (
    <ContentModerationForm
      data={config.data}
      onSaveFailed={() => void config.refetch()}
    />
  )
}

// The form mounts only with loaded settings so every control starts
// controlled with its saved value.
function ContentModerationForm(props: {
  data: ContentModerationConfigResponse
  onSaveFailed: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm<
    ContentModerationFormInput,
    unknown,
    ContentModerationFormValues
  >({
    resolver: zodResolver(contentModerationFormSchema),
    defaultValues: toContentModerationFormValues(props.data),
  })
  useEffect(() => {
    form.reset(toContentModerationFormValues(props.data))
  }, [props.data, form])

  const save = useMutation({
    mutationFn: (values: ContentModerationFormValues) =>
      updateContentModerationConfig(
        toContentModerationConfigRequest(values, props.data.config)
      ),
    onSuccess: (data) => {
      queryClient.setQueryData(CONFIG_QUERY_KEY, data)
      void queryClient.invalidateQueries({ queryKey: RUNTIME_QUERY_KEY })
      toast.success(t('Content moderation settings saved'))
    },
    onError: (error) => {
      handleServerError(error)
      props.onSaveFailed()
    },
  })
  const onSubmit = form.handleSubmit((values) => save.mutate(values))
  const allGroups = form.watch('all_groups')
  const modelFilterType = form.watch('model_filter_type')

  return (
    <SettingsSection title={t('Content Moderation')}>
      <Alert>
        <AlertDescription>
          {t(
            'Checks the latest user message of chat, Claude, Responses, Gemini and image requests with a moderation API. Each checked message is sent to that API. If the API fails, requests are always allowed.'
          )}
        </AlertDescription>
      </Alert>
      <Form {...form}>
        <SettingsForm onSubmit={onSubmit}>
          <SettingsPageFormActions
            onSave={onSubmit}
            isSaving={save.isPending}
            saveLabel='Save content moderation settings'
          />
          <FormField
            control={form.control}
            name='mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Mode')}</FormLabel>
                <Select
                  items={MODES.map((mode) => ({
                    value: mode.value,
                    label: t(mode.label),
                  }))}
                  onValueChange={(value) => {
                    if (value !== null) field.onChange(value)
                  }}
                  value={field.value}
                >
                  <FormControl>
                    <SelectTrigger aria-label={t('Mode')}>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {MODES.map((mode) => (
                        <SelectItem key={mode.value} value={mode.value}>
                          {t(mode.label)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t(
                    'Block mode adds one moderation call before each checked request and answers flagged requests with HTTP 403, before any charge.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <ContentModerationEngineFields form={form} />
          <ContentModerationKeywordFields form={form} />
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
                        <FormLabel>{t('Moderated groups')}</FormLabel>
                        <FormControl>
                          <TagInput
                            value={field.value ?? []}
                            onChange={field.onChange}
                            placeholder={t(
                              'Group name, or a prefix ending in *'
                            )}
                          />
                        </FormControl>
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
            name='model_filter_type'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Models')}</FormLabel>
                <Select
                  items={MODEL_FILTERS.map((item) => ({
                    value: item.value,
                    label: t(item.label),
                  }))}
                  onValueChange={(value) => {
                    if (value !== null) field.onChange(value)
                  }}
                  value={field.value}
                >
                  <FormControl>
                    <SelectTrigger aria-label={t('Models')}>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {MODEL_FILTERS.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {t(item.label)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </FormItem>
            )}
          />
          {modelFilterType !== 'all' && (
            <FormField
              control={form.control}
              name='model_filter_models'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Model names')}</FormLabel>
                  <FormControl>
                    <TagInput
                      value={field.value ?? []}
                      onChange={field.onChange}
                      placeholder={t('Exact model name')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
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
          <FormField
            control={form.control}
            name='allowlist'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Allowlisted user IDs')}</FormLabel>
                <FormControl>
                  <Input {...field} placeholder='12, 34' />
                </FormControl>
                <FormDescription>
                  {t(
                    'Allowlisted users are checked in the background and recorded, but never blocked, emailed or disabled.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='flex flex-col gap-2'>
            <h4 className='text-sm font-medium'>{t('Thresholds')}</h4>
            <p className='text-muted-foreground text-xs'>
              {t(
                'An input is flagged when any category scores at or above its threshold (0 to 1).'
              )}
            </p>
            <SettingsFormGrid>
              {props.data.categories.map((category) => (
                <FormField
                  key={category}
                  control={form.control}
                  name={`thresholds.${category}`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t(
                          CONTENT_MODERATION_CATEGORY_LABELS[category] ??
                            category
                        )}
                      </FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={1}
                          step={0.01}
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
          </div>
        </SettingsForm>
      </Form>
      <ContentModerationRuntimeCard />
    </SettingsSection>
  )
}
