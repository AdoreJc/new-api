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

import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { PromptAuditEventsPage } from '../api'

type PromptAuditScanStatusProps = {
  page: PromptAuditEventsPage
  /** Whether this page continued from an earlier cursor. */
  continued: boolean
  loading: boolean
  onContinue: (cursor: number) => void
  onRestart: () => void
}

/**
 * Footer of a regex or full prompt search: how far the scan read, and the
 * button that continues it below the last id read.
 */
export function PromptAuditScanStatus(props: PromptAuditScanStatusProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const scanned = formatNumber(props.page.scanned ?? 0, locale)
  const nextCursor = props.page.next_cursor ?? 0

  return (
    <div
      role='status'
      className='text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-2 py-2 text-sm'
    >
      <span>
        {props.page.truncated && nextCursor > 0
          ? t('Scanned {{rows}} events, up to #{{id}}.', {
              rows: scanned,
              id: nextCursor,
            })
          : t('Scanned {{rows}} events. The search reached the oldest event.', {
              rows: scanned,
            })}
      </span>
      {props.page.truncated && nextCursor > 0 && (
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={props.loading}
          onClick={() => props.onContinue(nextCursor)}
        >
          {t('Continue search')}
        </Button>
      )}
      {props.continued && (
        <Button
          type='button'
          size='sm'
          variant='ghost'
          disabled={props.loading}
          onClick={props.onRestart}
        >
          {t('Back to newest')}
        </Button>
      )}
    </div>
  )
}
