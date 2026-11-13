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
import { useTranslation } from 'react-i18next'

import dayjs from '@/lib/dayjs'

import type { PromptAuditEventFilter } from '../api'
import { PROMPT_AUDIT_DECISION_LABELS } from '../lib/event-format'
import {
  PROMPT_AUDIT_KEYWORD_FIELDS,
  promptAuditKeywordFields,
} from '../lib/keyword-search'

function formatTime(seconds: number | undefined): string {
  return seconds === undefined
    ? '…'
    : dayjs.unix(seconds).format('YYYY-MM-DD HH:mm:ss')
}

/** The conditions a filtered deletion uses, listed for its confirmation. */
export function PromptAuditFilterSummary(props: {
  filter: PromptAuditEventFilter
}) {
  const { t } = useTranslation()
  const filter = props.filter
  const rows: { label: string; value: string }[] = []
  if (filter.start_time !== undefined || filter.end_time !== undefined) {
    rows.push({
      label: t('Time range'),
      value: `${formatTime(filter.start_time)} – ${formatTime(filter.end_time)}`,
    })
  }
  if (filter.decision) {
    rows.push({
      label: t('Result'),
      value: t(
        PROMPT_AUDIT_DECISION_LABELS[filter.decision] ?? filter.decision
      ),
    })
  }
  if (filter.keyword?.trim()) {
    const fields = promptAuditKeywordFields(filter).map((value) => {
      const field = PROMPT_AUDIT_KEYWORD_FIELDS.find(
        (option) => option.value === value
      )
      return field ? t(field.labelKey) : value
    })
    rows.push({
      label: t('Keyword'),
      value: fields.length
        ? `${filter.keyword.trim()} (${fields.join(', ')})`
        : filter.keyword.trim(),
    })
  }
  if (filter.group) rows.push({ label: t('Group'), value: filter.group })
  if (filter.request_id) {
    rows.push({ label: t('Request ID'), value: filter.request_id })
  }

  if (rows.length === 0) {
    return (
      <p className='text-destructive text-sm font-medium'>
        {t('No filters are set: every event up to this preview is deleted.')}
      </p>
    )
  }
  return (
    <dl
      aria-label={t('Deletion filters')}
      className='bg-muted/50 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md p-2 text-sm'
    >
      {rows.map((row) => (
        <div key={row.label} className='contents'>
          <dt className='text-muted-foreground'>{row.label}</dt>
          <dd className='min-w-0 font-mono break-all'>{row.value}</dd>
        </div>
      ))}
    </dl>
  )
}
