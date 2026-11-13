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
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { handleServerError } from '@/lib/handle-server-error'

import {
  testAuditPolicy,
  type AuditPolicyConfig,
  type AuditPolicyTestResult,
} from './api'
import {
  ACTION_LABELS,
  ENGINE_MODE_LABELS,
  FAIL_POLICY_LABELS,
  INHERIT,
  RELAY_FORMAT_LABELS,
} from './lib/constants'
import { PolicySelect } from './policy-select'

const TEXT_FIELDS = [
  { name: 'user_id', label: 'User ID', type: 'number' },
  { name: 'token_id', label: 'Token ID', type: 'number' },
  { name: 'group', label: 'Group', type: 'text' },
  { name: 'requested_group', label: 'Requested group', type: 'text' },
  { name: 'model', label: 'Model', type: 'text' },
] as const

type TestInput = Record<(typeof TEXT_FIELDS)[number]['name'], string> & {
  format: string
}

export function AuditPolicyTestPanel(props: {
  relayFormats: string[]
  /** The rules being edited; tested as they are, without saving them. */
  draft: AuditPolicyConfig
}) {
  const { t } = useTranslation()
  const [input, setInput] = useState<TestInput>({
    user_id: '',
    token_id: '',
    group: '',
    requested_group: '',
    model: '',
    format: INHERIT,
  })
  const test = useMutation({
    mutationFn: () =>
      testAuditPolicy({
        user_id: Number.parseInt(input.user_id, 10) || 0,
        token_id: Number.parseInt(input.token_id, 10) || 0,
        group: input.group.trim(),
        requested_group: input.requested_group.trim(),
        model: input.model.trim(),
        format: input.format === INHERIT ? '' : input.format,
        config: props.draft,
      }),
    onError: (error) => handleServerError(error),
  })
  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    test.mutate()
  }
  const formatOptions = [
    { value: INHERIT, label: 'Any format' },
    ...props.relayFormats.map((format) => ({
      value: format,
      label: RELAY_FORMAT_LABELS[format] ?? format,
    })),
  ]
  return (
    <div className='flex flex-col gap-3 rounded-lg border p-4'>
      <div>
        <h4 className='text-sm font-medium'>{t('Test a request')}</h4>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Shows which rule a request would match in the rules being edited, saved or not, and what each engine would do. No check is run and nothing is recorded.'
          )}
        </p>
      </div>
      <form
        onSubmit={onSubmit}
        className='grid gap-3 sm:grid-cols-2 lg:grid-cols-6'
      >
        {TEXT_FIELDS.map((field) => (
          <div key={field.name} className='flex min-w-0 flex-col gap-1.5'>
            <Label htmlFor={`audit-policy-test-${field.name}`}>
              {t(field.label)}
            </Label>
            <Input
              id={`audit-policy-test-${field.name}`}
              type={field.type}
              min={field.type === 'number' ? 0 : undefined}
              value={input[field.name]}
              onChange={(event) =>
                setInput({ ...input, [field.name]: event.target.value })
              }
            />
          </div>
        ))}
        <div className='flex min-w-0 flex-col gap-1.5'>
          <Label>{t('Request format')}</Label>
          <PolicySelect
            value={input.format}
            onChange={(format) => setInput({ ...input, format })}
            options={formatOptions}
            ariaLabel={t('Request format')}
          />
        </div>
        <div className='sm:col-span-2 lg:col-span-6'>
          <Button type='submit' size='sm' disabled={test.isPending}>
            {t('Test')}
          </Button>
        </div>
      </form>
      {test.data && <TestResult result={test.data} />}
    </div>
  )
}

function TestResult(props: { result: AuditPolicyTestResult }) {
  const { t } = useTranslation()
  const result = props.result
  const ruleLabel =
    result.rule_index === -1
      ? t('Default rule')
      : t('Rule {{position}}: {{rule}}', {
          position: result.rule_index + 1,
          rule: result.rule_name || result.rule_id,
        })
  const memberships = [
    result.allowlisted && t('Allowlist'),
    result.verified && t('Verified allowlist'),
    result.blocklisted && t('Blocklist'),
  ].filter((item): item is string => typeof item === 'string')
  const engines = [
    {
      label: 'Prompt audit',
      mode: result.prompt_audit,
      action: result.prompt_audit_action,
      fail: result.prompt_audit_fail_policy,
    },
    {
      label: 'Content moderation',
      mode: result.content_moderation,
      action: result.content_moderation_action,
      fail: result.content_moderation_fail_policy,
    },
  ]
  return (
    <div className='bg-muted/30 flex flex-col gap-2 rounded-md p-3 text-sm'>
      <dl className='grid gap-x-4 gap-y-2 sm:grid-cols-2'>
        <div className='flex min-w-0 flex-col'>
          <dt className='text-muted-foreground text-xs'>{t('Matched rule')}</dt>
          <dd className='truncate'>
            {ruleLabel}
            {!result.saved && (
              <span className='text-muted-foreground'>
                {' '}
                · {t('derived from the engine settings')}
              </span>
            )}
          </dd>
        </div>
        <div className='flex min-w-0 flex-col'>
          <dt className='text-muted-foreground text-xs'>{t('Lists')}</dt>
          <dd>
            {memberships.length > 0
              ? memberships.join(', ')
              : t('On neither list')}
          </dd>
        </div>
        {engines.map((engine) => (
          <div key={engine.label} className='flex min-w-0 flex-col'>
            <dt className='text-muted-foreground text-xs'>{t(engine.label)}</dt>
            <dd className='flex flex-wrap items-center gap-1.5'>
              <StatusBadge
                label={t(ENGINE_MODE_LABELS[engine.mode] ?? engine.mode)}
                variant={engine.mode === 'sync' ? 'warning' : 'neutral'}
                copyable={false}
              />
              {engine.action && (
                <span>{t(ACTION_LABELS[engine.action] ?? engine.action)}</span>
              )}
              {engine.fail && (
                <span className='text-muted-foreground text-xs'>
                  {t(FAIL_POLICY_LABELS[engine.fail] ?? engine.fail)}
                </span>
              )}
            </dd>
          </div>
        ))}
        {result.reroute && (
          <div className='flex min-w-0 flex-col'>
            <dt className='text-muted-foreground text-xs'>{t('Reroute to')}</dt>
            <dd className='font-mono text-xs'>
              {[result.reroute.model, result.reroute.group]
                .filter(Boolean)
                .join(' · ')}
            </dd>
          </div>
        )}
      </dl>
      {result.notes.length > 0 && (
        <ul className='text-muted-foreground list-disc space-y-1 pl-5 text-xs'>
          {result.notes.map((note) => (
            <li key={note}>{t(note)}</li>
          ))}
        </ul>
      )}
    </div>
  )
}
