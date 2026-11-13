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
import { useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { TagInput } from '@/components/tag-input'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
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
import { Switch } from '@/components/ui/switch'

import {
  SettingsControlGroup,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../../components/settings-form-layout'
import type { AuditPolicyRule } from './api'
import {
  LIST_CONDITION_OPTIONS,
  RELAY_FORMAT_LABELS,
  REROUTE_FALLBACK_OPTIONS,
} from './lib/constants'
import {
  createRuleFormSchema,
  ruleUsesReroute,
  toRule,
  toRuleFormValues,
  type RuleFormInput,
  type RuleFormValues,
} from './lib/rule-form'
import { PolicySelect } from './policy-select'
import {
  ContentModerationRuleFields,
  PromptAuditRuleFields,
  type RuleForm,
} from './rule-engine-fields'

export type RuleEditorProps = {
  rule: AuditPolicyRule
  isDefault: boolean
  /** Ids of the other rules, which this rule id must not repeat. */
  otherIds: string[]
  promptAuditCategories: string[]
  moderationCategories: string[]
  relayFormats: string[]
  onSave: (rule: AuditPolicyRule) => void
  onOpenChange: (open: boolean) => void
}

const RULE_FORM_ID = 'audit-policy-rule-form'

const PATTERN_FIELDS = [
  {
    name: 'match.groups',
    label: 'Groups',
    placeholder: 'Group name or pattern, for example claude-ch*',
  },
  {
    name: 'match.models',
    label: 'Models',
    placeholder: 'Model name or pattern, for example gpt-*',
  },
  {
    name: 'match.exclude_models',
    label: 'Excluded models',
    placeholder: 'Model name or pattern',
  },
  { name: 'match.user_ids', label: 'User IDs', placeholder: 'User ID' },
  { name: 'match.token_ids', label: 'Token IDs', placeholder: 'Token ID' },
] as const

/** Edits one rule; the dialog mounts only while a rule is being edited. */
export function RuleEditorDialog(props: RuleEditorProps) {
  const { t } = useTranslation()
  const schema = useMemo(
    () =>
      createRuleFormSchema({
        otherIds: props.otherIds,
        isDefault: props.isDefault,
      }),
    [props.otherIds, props.isDefault]
  )
  const form = useForm<RuleFormInput, unknown, RuleFormValues>({
    resolver: zodResolver(schema),
    defaultValues: toRuleFormValues(props.rule),
  })
  const onSubmit = form.handleSubmit((values) => props.onSave(toRule(values)))
  const promptAudit = form.watch('prompt_audit')
  const moderation = form.watch('content_moderation')
  const showReroute = ruleUsesReroute({
    prompt_audit: promptAudit,
    content_moderation: moderation,
  })
  const title = props.isDefault ? t('Edit default rule') : t('Edit rule')

  return (
    <Dialog
      open
      onOpenChange={props.onOpenChange}
      title={title}
      description={
        props.isDefault
          ? t(
              'The default rule applies to requests that no other rule matches.'
            )
          : t(
              'All set conditions must hold. Empty lists match any value; * matches any characters.'
            )
      }
      contentClassName='sm:max-w-3xl max-sm:max-h-(--dialog-available-height)'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={RULE_FORM_ID}>
            {t('Apply')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form id={RULE_FORM_ID} onSubmit={onSubmit} className='space-y-4'>
          {!props.isDefault && (
            <RuleMatchFields form={form} relayFormats={props.relayFormats} />
          )}
          <PromptAuditRuleFields
            form={form}
            categories={props.promptAuditCategories}
          />
          <ContentModerationRuleFields
            form={form}
            categories={props.moderationCategories}
          />
          {showReroute && <RerouteFields form={form} />}
        </form>
      </Form>
    </Dialog>
  )
}

function RuleMatchFields(props: { form: RuleForm; relayFormats: string[] }) {
  const { t } = useTranslation()
  return (
    <>
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={props.form.control}
          name='id'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Rule ID')}</FormLabel>
              <FormControl>
                <Input {...field} maxLength={64} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='name'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Name')}</FormLabel>
              <FormControl>
                <Input {...field} maxLength={128} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
      <FormField
        control={props.form.control}
        name='enabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Enabled')}</FormLabel>
              <FormDescription>
                {t('A disabled rule is skipped when matching requests.')}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch checked={field.value} onCheckedChange={field.onChange} />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />
      <SettingsControlGroup>
        <p className='text-sm font-medium'>{t('Conditions')}</p>
        <div className='grid gap-4 sm:grid-cols-2'>
          {PATTERN_FIELDS.map((item) => (
            <FormField
              key={item.name}
              control={props.form.control}
              name={item.name}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t(item.label)}</FormLabel>
                  <FormControl>
                    <TagInput
                      value={field.value ?? []}
                      onChange={field.onChange}
                      placeholder={t(item.placeholder)}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}
          <FormField
            control={props.form.control}
            name='match.list'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('List')}</FormLabel>
                <PolicySelect
                  value={field.value}
                  onChange={field.onChange}
                  options={LIST_CONDITION_OPTIONS}
                  ariaLabel={t('List')}
                />
                <FormMessage />
              </FormItem>
            )}
          />
        </div>
        <FormField
          control={props.form.control}
          name='match.formats'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Request formats')}</FormLabel>
              <FormDescription>
                {t('Select none to match every format.')}
              </FormDescription>
              <div className='grid gap-2 sm:grid-cols-2'>
                {props.relayFormats.map((format) => (
                  <label
                    key={format}
                    className='flex items-center gap-2 text-sm'
                  >
                    <Checkbox
                      checked={field.value.includes(format)}
                      onCheckedChange={(next) =>
                        field.onChange(
                          next
                            ? [...field.value, format]
                            : field.value.filter((value) => value !== format)
                        )
                      }
                    />
                    {t(RELAY_FORMAT_LABELS[format] ?? format)}
                  </label>
                ))}
              </div>
            </FormItem>
          )}
        />
      </SettingsControlGroup>
    </>
  )
}

function RerouteFields(props: { form: RuleForm }) {
  const { t } = useTranslation()
  return (
    <SettingsControlGroup>
      <p className='text-sm font-medium'>{t('Reroute')}</p>
      <Alert>
        <AlertDescription>
          {t(
            "Reroute only applies to verified allowlist users; other users get the fallback. Make sure rerouting complies with the upstream model's terms of use and licensing."
          )}
        </AlertDescription>
      </Alert>
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={props.form.control}
          name='reroute.model'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Reroute model')}</FormLabel>
              <FormControl>
                <Input {...field} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='reroute.group'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Reroute group')}</FormLabel>
              <FormControl>
                <Input {...field} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='reroute_fallback'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Fallback for other users')}</FormLabel>
              <PolicySelect
                value={field.value}
                onChange={field.onChange}
                options={REROUTE_FALLBACK_OPTIONS}
                ariaLabel={t('Fallback for other users')}
              />
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
    </SettingsControlGroup>
  )
}
