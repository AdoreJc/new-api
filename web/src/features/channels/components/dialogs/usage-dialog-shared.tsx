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
import { Check, Copy } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { cn } from '@/lib/utils'

import {
  usagePercentTextClassName,
  usagePercentVariant,
} from '../../lib/usage-format'

/**
 * Presentational pieces shared by the subscription usage dialogs (Codex and
 * Claude). They render precomputed strings so each dialog keeps its own
 * upstream document parsing.
 */

export type UsageWindowCardProps = {
  title: string
  /** Secondary line under the title, e.g. the window length */
  subtitle?: string
  /** Badge rendered next to the title, e.g. the data source */
  badge?: ReactNode
  /** 0-100 when data is present; null renders the empty state */
  percent: number | null
  /** Label/value pairs shown under the progress bar */
  details: { label: string; value: string }[]
}

export function UsageWindowCard(props: UsageWindowCardProps) {
  const { t } = useTranslation()
  const hasData = props.percent !== null
  const percent = props.percent ?? 0
  const variant = usagePercentVariant(percent)

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <CardTitle className='text-sm font-semibold'>
                {props.title}
              </CardTitle>
              {props.badge}
            </div>
            {props.subtitle ? (
              <CardDescription className='mt-1 text-xs'>
                {props.subtitle}
              </CardDescription>
            ) : null}
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={cn(
                'text-xl leading-none font-semibold tabular-nums',
                usagePercentTextClassName[variant]
              )}
            >
              {hasData ? `${percent}%` : '-'}
            </div>
            <div className='text-muted-foreground mt-1 text-[11px]'>
              {t('Used')}
            </div>
          </div>
        </div>
      </CardHeader>
      <CardContent className='p-3 pt-0'>
        {hasData ? (
          <Progress
            value={percent}
            aria-label={`${props.title} usage: ${percent}%`}
            className='mt-1'
          />
        ) : (
          <div className='text-muted-foreground mt-1 text-sm'>-</div>
        )}
        <div className='mt-3 grid grid-cols-1 gap-2 text-xs sm:grid-cols-2'>
          {props.details.map((detail, index) => (
            <div
              key={detail.label}
              className={cn('min-w-0', index % 2 === 1 && 'sm:text-right')}
            >
              <div className='text-muted-foreground text-[11px]'>
                {detail.label}
              </div>
              <div className='break-all tabular-nums'>{detail.value}</div>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}

export function UsageSectionHeading(props: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <div className='flex flex-wrap items-start justify-between gap-3'>
      <div className='min-w-0'>
        <div className='text-sm font-semibold'>{props.title}</div>
        {props.description ? (
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {props.description}
          </div>
        ) : null}
      </div>
      {props.children ? (
        <div className='flex shrink-0 flex-wrap items-center gap-2'>
          {props.children}
        </div>
      ) : null}
    </div>
  )
}

export function UsageInfoField(props: {
  label: string
  value?: string | null
  mono?: boolean
  copyable?: boolean
  className?: string
}) {
  const { t } = useTranslation()
  const { copyToClipboard, copiedText } = useCopyToClipboard({ notify: false })
  const text = props.value?.trim() || ''
  const hasCopied = copiedText === text

  return (
    <div
      className={cn(
        'bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1',
        props.className
      )}
    >
      <div className='text-muted-foreground text-[11px] font-medium'>
        {props.label}
      </div>
      <div className='mt-1 flex min-w-0 items-start justify-between gap-2'>
        <span
          className={cn(
            'min-w-0 flex-1 text-xs leading-5 break-all',
            props.mono && 'font-mono tabular-nums'
          )}
        >
          {text || '-'}
        </span>
        {props.copyable !== false && text ? (
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={t('Copy')}
            onClick={() => copyToClipboard(text)}
          >
            {hasCopied ? <Check className='text-success' /> : <Copy />}
          </Button>
        ) : null}
      </div>
    </div>
  )
}
