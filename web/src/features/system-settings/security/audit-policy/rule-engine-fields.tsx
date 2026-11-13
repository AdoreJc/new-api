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
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { TagInput } from '@/components/tag-input'
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

import {
  SettingsControlGroup,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../../components/settings-form-layout'
import { CONTENT_MODERATION_CATEGORY_LABELS } from '../content-moderation/lib/form'
import { PROMPT_AUDIT_SCANNER_LABELS } from '../prompt-audit/lib/form'
import {
  ACTION_OPTIONS,
  CATEGORY_ACTION_OPTIONS,
  CATEGORY_LEVEL_OPTIONS,
  FAIL_POLICY_OPTIONS,
  INHERIT,
  KEYWORD_MODE_OPTIONS,
  MIN_LEVEL_OPTIONS,
  SCAN_SCOPE_OPTIONS,
  type PolicyOption,
} from './lib/constants'
import type { RuleFormInput, RuleFormValues } from './lib/rule-form'
import { PolicySelect } from './policy-select'

export type RuleForm = UseFormReturn<RuleFormInput, unknown, RuleFormValues>

type SelectFieldName =
  | 'prompt_audit.scan_scope'
  | 'prompt_audit.min_level'
  | 'prompt_audit.action'
  | 'prompt_audit.fail_policy'
  | 'content_moderation.action'
  | 'content_moderation.keyword_mode'
  | 'content_moderation.fail_policy'

function SelectField(props: {
  form: RuleForm
  name: SelectFieldName
  label: string
  options: PolicyOption[]
  description?: string
}) {
  const { t } = useTranslation()
  return (
    <FormField
      control={props.form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t(props.label)}</FormLabel>
          <PolicySelect
            value={field.value}
            onChange={field.onChange}
            options={props.options}
            ariaLabel={t(props.label)}
          />
          {props.description && (
            <FormDescription>{t(props.description)}</FormDescription>
          )}
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

function SwitchField(props: {
  form: RuleForm
  name:
    | 'prompt_audit.enabled'
    | 'prompt_audit.count_disposal'
    | 'content_moderation.enabled'
    | 'content_moderation.count_disposal'
    | 'content_moderation.override_keyword_lists'
  label: string
  description: string
}) {
  const { t } = useTranslation()
  return (
    <FormField
      control={props.form.control}
      name={props.name}
      render={({ field }) => (
        <SettingsSwitchItem>
          <SettingsSwitchContent>
            <FormLabel>{t(props.label)}</FormLabel>
            <FormDescription>{t(props.description)}</FormDescription>
          </SettingsSwitchContent>
          <FormControl>
            <Switch checked={field.value} onCheckedChange={field.onChange} />
          </FormControl>
        </SettingsSwitchItem>
      )}
    />
  )
}

export function PromptAuditRuleFields(props: {
  form: RuleForm
  categories: string[]
}) {
  const enabled = props.form.watch('prompt_audit.enabled')
  return (
    <SettingsControlGroup>
      <SwitchField
        form={props.form}
        name='prompt_audit.enabled'
        label='Prompt audit'
        description='Scan prompts with the Guard model for requests this rule matches.'
      />
      {enabled && (
        <>
          <div className='grid gap-4 sm:grid-cols-2'>
            <SelectField
              form={props.form}
              name='prompt_audit.action'
              label='Action'
              options={ACTION_OPTIONS}
            />
            <SelectField
              form={props.form}
              name='prompt_audit.min_level'
              label='Trigger level'
              options={MIN_LEVEL_OPTIONS}
            />
            <SelectField
              form={props.form}
              name='prompt_audit.scan_scope'
              label='Scan scope'
              options={SCAN_SCOPE_OPTIONS}
            />
            <SelectField
              form={props.form}
              name='prompt_audit.fail_policy'
              label='When the Guard is unavailable'
              options={FAIL_POLICY_OPTIONS}
              description='Applies only to checks that run before the request.'
            />
          </div>
          <SwitchField
            form={props.form}
            name='prompt_audit.count_disposal'
            label='Count toward violation disposal'
            description='Findings under this rule count toward the violation limit that can disable a user.'
          />
          <CategoryOverrides form={props.form} categories={props.categories} />
        </>
      )}
    </SettingsControlGroup>
  )
}

function CategoryOverrides(props: { form: RuleForm; categories: string[] }) {
  const { t } = useTranslation()
  const levels = props.form.watch('prompt_audit.category_levels')
  const actions = props.form.watch('prompt_audit.category_actions')
  return (
    <div className='space-y-2'>
      <p className='text-sm font-medium'>{t('Per-category overrides')}</p>
      <p className='text-muted-foreground text-xs'>
        {t(
          'A category level of Off never triggers. Ignore drops the category from the decision.'
        )}
      </p>
      <ul className='space-y-2'>
        {props.categories.map((category) => {
          const label = t(PROMPT_AUDIT_SCANNER_LABELS[category] ?? category)
          return (
            <li
              key={category}
              className='grid grid-cols-2 items-center gap-2 sm:grid-cols-[minmax(0,1fr)_11rem_11rem]'
            >
              <span className='col-span-2 truncate text-sm sm:col-span-1'>
                {label}
              </span>
              <PolicySelect
                size='sm'
                value={levels[category] ?? INHERIT}
                onChange={(value) =>
                  props.form.setValue(
                    'prompt_audit.category_levels',
                    { ...levels, [category]: value },
                    { shouldDirty: true }
                  )
                }
                options={CATEGORY_LEVEL_OPTIONS}
                ariaLabel={t('{{category}} trigger level', {
                  category: label,
                })}
              />
              <PolicySelect
                size='sm'
                value={actions[category] ?? INHERIT}
                onChange={(value) =>
                  props.form.setValue(
                    'prompt_audit.category_actions',
                    { ...actions, [category]: value },
                    { shouldDirty: true }
                  )
                }
                options={CATEGORY_ACTION_OPTIONS}
                ariaLabel={t('{{category}} action', { category: label })}
              />
            </li>
          )
        })}
      </ul>
    </div>
  )
}

export function ContentModerationRuleFields(props: {
  form: RuleForm
  categories: string[]
}) {
  const { t } = useTranslation()
  const enabled = props.form.watch('content_moderation.enabled')
  const overrideLists = props.form.watch(
    'content_moderation.override_keyword_lists'
  )
  const thresholds = props.form.watch('content_moderation.thresholds')
  return (
    <SettingsControlGroup>
      <SwitchField
        form={props.form}
        name='content_moderation.enabled'
        label='Content moderation'
        description='Check inputs with the moderation API and keyword lists for requests this rule matches.'
      />
      {enabled && (
        <>
          <div className='grid gap-4 sm:grid-cols-2'>
            <SelectField
              form={props.form}
              name='content_moderation.action'
              label='Action'
              options={ACTION_OPTIONS}
            />
            <FormField
              control={props.form.control}
              name='content_moderation.sample_rate'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Sample rate (%)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0}
                      max={100}
                      {...field}
                      value={String(field.value ?? '')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <SelectField
              form={props.form}
              name='content_moderation.keyword_mode'
              label='Keyword mode'
              options={KEYWORD_MODE_OPTIONS}
            />
            <SelectField
              form={props.form}
              name='content_moderation.fail_policy'
              label='When the moderation API is unavailable'
              options={FAIL_POLICY_OPTIONS}
              description='Applies only to checks that run before the request.'
            />
          </div>
          <SwitchField
            form={props.form}
            name='content_moderation.override_keyword_lists'
            label='Use specific keyword lists'
            description='Turn off to use the keyword lists of the content moderation settings.'
          />
          {overrideLists && (
            <FormField
              control={props.form.control}
              name='content_moderation.keyword_lists'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Keyword lists')}</FormLabel>
                  <FormControl>
                    <TagInput
                      value={field.value ?? []}
                      onChange={field.onChange}
                      placeholder={t('Keyword list name')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
          <SwitchField
            form={props.form}
            name='content_moderation.count_disposal'
            label='Count toward violation disposal'
            description='Findings under this rule count toward the violation limit that can disable a user.'
          />
          <FormField
            control={props.form.control}
            name='content_moderation.thresholds'
            render={() => (
              <FormItem>
                <FormLabel>{t('Threshold overrides')}</FormLabel>
                <FormDescription>
                  {t(
                    'Leave empty to use the threshold of the content moderation settings. Scores at or above the threshold are flagged.'
                  )}
                </FormDescription>
                <div className='grid gap-2 sm:grid-cols-2'>
                  {props.categories.map((category) => {
                    const label = t(
                      CONTENT_MODERATION_CATEGORY_LABELS[category] ?? category
                    )
                    return (
                      <label
                        key={category}
                        className='flex items-center justify-between gap-2 text-sm'
                      >
                        <span className='min-w-0 truncate'>{label}</span>
                        <Input
                          type='number'
                          min={0}
                          max={1}
                          step={0.01}
                          className='w-24'
                          placeholder={t('Default')}
                          value={thresholds[category] ?? ''}
                          onChange={(event) =>
                            props.form.setValue(
                              'content_moderation.thresholds',
                              {
                                ...thresholds,
                                [category]: event.target.value,
                              },
                              { shouldDirty: true }
                            )
                          }
                        />
                      </label>
                    )
                  })}
                </div>
                <FormMessage />
              </FormItem>
            )}
          />
        </>
      )}
    </SettingsControlGroup>
  )
}
