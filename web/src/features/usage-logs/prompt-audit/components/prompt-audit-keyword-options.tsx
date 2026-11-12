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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import type { PromptAuditEventFilter } from '../api'
import {
  PROMPT_AUDIT_FULL_PROMPT_FIELD,
  PROMPT_AUDIT_KEYWORD_FIELDS,
  PROMPT_AUDIT_KEYWORD_MAX_LENGTH,
  promptAuditKeywordFields,
  promptAuditRegexProblem,
} from '../lib/keyword-search'

type PromptAuditKeywordOptionsProps = {
  draft: PromptAuditEventFilter
  onChange: (patch: Partial<PromptAuditEventFilter>) => void
}

/** Regex and case switches plus the search field picker for the keyword. */
export function PromptAuditKeywordOptions(
  props: PromptAuditKeywordOptionsProps
) {
  const { t } = useTranslation()
  const id = useId()
  const regex = props.draft.keyword_mode === 'regex'
  const fields = promptAuditKeywordFields(props.draft)
  const problem = regex
    ? promptAuditRegexProblem(props.draft.keyword ?? '')
    : null

  return (
    <div className='flex min-w-0 flex-col gap-1.5'>
      <div className='flex flex-wrap items-center gap-x-4 gap-y-1.5'>
        <Label htmlFor={`${id}-regex`} className='font-normal'>
          <Switch
            id={`${id}-regex`}
            size='sm'
            checked={regex}
            onCheckedChange={(checked) =>
              props.onChange({
                keyword_mode: checked ? 'regex' : undefined,
                case_sensitive: checked
                  ? props.draft.case_sensitive
                  : undefined,
              })
            }
          />
          {t('Regex')}
        </Label>
        <Label htmlFor={`${id}-case`} className='font-normal'>
          <Switch
            id={`${id}-case`}
            size='sm'
            disabled={!regex}
            checked={regex && props.draft.case_sensitive === true}
            onCheckedChange={(checked) =>
              props.onChange({ case_sensitive: checked || undefined })
            }
          />
          {t('Case sensitive')}
        </Label>
      </div>
      <MultiSelect
        options={PROMPT_AUDIT_KEYWORD_FIELDS.map((field) => ({
          value: field.value,
          label: t(field.labelKey),
          hint:
            field.value === PROMPT_AUDIT_FULL_PROMPT_FIELD
              ? t('Recorded in the audit log')
              : undefined,
        }))}
        selected={fields}
        onChange={(values) =>
          props.onChange({ keyword_fields: values.join(',') || undefined })
        }
        placeholder={t('Search fields: all but full prompt')}
      />
      {fields.includes(PROMPT_AUDIT_FULL_PROMPT_FIELD) && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Searching full prompts is recorded in the audit log, including the search pattern and time range.'
          )}
        </p>
      )}
      {problem && (
        <p role='alert' className='text-destructive text-xs'>
          {problem === 'too_long'
            ? t('The search pattern is longer than {{max}} characters', {
                max: PROMPT_AUDIT_KEYWORD_MAX_LENGTH,
              })
            : t('This does not look like a valid regular expression.')}{' '}
          {t('Patterns use RE2 syntax: no lookaround or backreferences.')}
        </p>
      )}
    </div>
  )
}
