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
import { AlarmClock, FolderPlus, Plus, Trash2, X } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DateTimePicker } from '@/components/datetime-picker'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

import {
  AUTO_RESET_MAX_ALARMS,
  type AutoResetConditionType,
  type AutoResetRule,
  type AutoResetRulePath,
  addAutoResetAlarm,
  canAddAutoResetChild,
  countAutoResetAlarms,
  createAutoResetCondition,
  isAutoResetGroup,
  newAutoResetNodeId,
  updateAutoResetNode,
} from '../../lib/auto-reset-rule'

const CONDITION_LABELS: Record<AutoResetConditionType, string> = {
  five_hour_used_pct: '5-hour usage at or above',
  weekly_used_pct: 'Weekly usage at or above',
  at_time: 'At a set time',
  credit_expires_within: 'Earliest credit expires within',
}

const WEEKLY_WINDOW_LABELS: Record<string, string> = {
  seven_day: '7-day window',
  seven_day_sonnet: '7-day Sonnet window',
  seven_day_overage_included: '7-day Fable window',
}

type RuleNodeProps = {
  root: AutoResetRule
  node: AutoResetRule
  path: AutoResetRulePath
  depth: number
  weeklyWindows: string[]
  disabled?: boolean
  onChange: (root: AutoResetRule) => void
}

function parseNumber(value: string): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

/** ISO 8601 UTC without milliseconds, the format the server stores. */
function toUtcIso(date: Date): string {
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function AlarmList(props: RuleNodeProps) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<Date | undefined>(undefined)
  const times = props.node.times ?? []
  const atLimit = countAutoResetAlarms(props.root) >= AUTO_RESET_MAX_ALARMS
  const pendingInPast = pending ? pending.getTime() <= Date.now() : false

  const update = (fn: (node: AutoResetRule) => AutoResetRule) =>
    props.onChange(updateAutoResetNode(props.root, props.path, fn))

  return (
    <div className='flex w-full flex-col gap-2'>
      {times.length === 0 ? (
        <p className='text-muted-foreground text-xs'>{t('No alarm times')}</p>
      ) : (
        <ul className='flex flex-wrap gap-2' aria-label={t('Alarm times')}>
          {times.map((time) => (
            <li
              key={time}
              className='bg-background flex items-center gap-1 rounded-md border px-2 py-1 text-xs'
            >
              <AlarmClock className='size-3.5' aria-hidden='true' />
              <span>{dayjs(time).format('YYYY-MM-DD HH:mm')}</span>
              <Button
                type='button'
                variant='ghost'
                size='icon-xs'
                disabled={props.disabled}
                aria-label={t('Remove alarm {{time}}', {
                  time: dayjs(time).format('YYYY-MM-DD HH:mm'),
                })}
                onClick={() =>
                  update((node) => ({
                    ...node,
                    times: (node.times ?? []).filter((item) => item !== time),
                  }))
                }
              >
                <X aria-hidden='true' />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <div className='flex flex-wrap items-center gap-2'>
        <DateTimePicker
          value={pending}
          onChange={setPending}
          className='min-w-0 flex-1'
        />
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled || !pending || pendingInPast || atLimit}
          onClick={() => {
            if (!pending) return
            update((node) => addAutoResetAlarm(node, toUtcIso(pending)))
            setPending(undefined)
          }}
        >
          <Plus data-icon='inline-start' />
          {t('Add alarm')}
        </Button>
      </div>
      {pendingInPast ? (
        <p className='text-destructive text-xs'>
          {t('Pick a time in the future')}
        </p>
      ) : null}
    </div>
  )
}

function ConditionNode(props: RuleNodeProps) {
  const { t } = useTranslation()
  const node = props.node
  const update = (fn: (node: AutoResetRule) => AutoResetRule | null) =>
    props.onChange(updateAutoResetNode(props.root, props.path, fn))
  const weeklyItems = props.weeklyWindows.map((value) => ({
    value,
    label: t(WEEKLY_WINDOW_LABELS[value] ?? value),
  }))

  let editor: ReactNode = null
  switch (node.type) {
    case 'five_hour_used_pct':
    case 'weekly_used_pct':
      editor = (
        <div className='flex flex-wrap items-center gap-2'>
          {node.type === 'weekly_used_pct' ? (
            <Select
              value={node.window ?? 'seven_day'}
              disabled={props.disabled}
              onValueChange={(value) => {
                if (value) update((current) => ({ ...current, window: value }))
              }}
              items={weeklyItems}
            >
              <SelectTrigger size='sm' aria-label={t('Usage window')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {weeklyItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null}
          <Input
            type='number'
            min={1}
            max={100}
            step={1}
            className='h-8 w-24'
            aria-label={t('Usage threshold (%)')}
            disabled={props.disabled}
            value={node.threshold ?? ''}
            onChange={(event) =>
              update((current) => ({
                ...current,
                threshold: parseNumber(event.target.value),
              }))
            }
          />
          <span className='text-muted-foreground text-sm'>%</span>
        </div>
      )
      break
    case 'credit_expires_within':
      editor = (
        <div className='flex items-center gap-2'>
          <Input
            type='number'
            min={1}
            max={720}
            step={1}
            className='h-8 w-24'
            aria-label={t('Hours before expiry')}
            disabled={props.disabled}
            value={node.hours ?? ''}
            onChange={(event) =>
              update((current) => ({
                ...current,
                hours: parseNumber(event.target.value),
              }))
            }
          />
          <span className='text-muted-foreground text-sm'>{t('hours')}</span>
        </div>
      )
      break
    case 'at_time':
      editor = <AlarmList {...props} />
      break
  }

  return (
    <div className='bg-background flex flex-wrap items-start gap-3 rounded-md border p-3'>
      <span className='min-w-40 pt-1.5 text-sm font-medium'>
        {t(CONDITION_LABELS[node.type ?? 'five_hour_used_pct'])}
      </span>
      <div className='min-w-0 flex-1'>{editor}</div>
      <Button
        type='button'
        variant='ghost'
        size='icon-sm'
        disabled={props.disabled}
        aria-label={t('Remove condition')}
        onClick={() => update(() => null)}
      >
        <Trash2 aria-hidden='true' />
      </Button>
    </div>
  )
}

function GroupNode(props: RuleNodeProps) {
  const { t } = useTranslation()
  const node = props.node
  const children = node.children ?? []
  const update = (fn: (node: AutoResetRule) => AutoResetRule | null) =>
    props.onChange(updateAutoResetNode(props.root, props.path, fn))
  const canAddCondition = canAddAutoResetChild(props.root, props.depth, false)
  const canAddGroup = canAddAutoResetChild(props.root, props.depth, true)
  const opItems = [
    { value: 'and', label: t('All of (AND)') },
    { value: 'or', label: t('Any of (OR)') },
  ]
  const conditionTypes = Object.keys(
    CONDITION_LABELS
  ) as AutoResetConditionType[]

  return (
    <div
      className={cn(
        'flex flex-col gap-2 rounded-lg border p-3',
        props.depth === 1 ? 'bg-muted/30' : 'bg-muted/60'
      )}
      role='group'
      aria-label={t('Condition group')}
    >
      <div className='flex flex-wrap items-center gap-2'>
        <Select
          value={node.op ?? 'or'}
          disabled={props.disabled}
          onValueChange={(value) => {
            if (value === 'and' || value === 'or') {
              update((current) => ({ ...current, op: value }))
            }
          }}
          items={opItems}
        >
          <SelectTrigger size='sm' aria-label={t('Match mode')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {opItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={props.disabled || !canAddCondition}
              />
            }
          >
            <Plus data-icon='inline-start' />
            {t('Add condition')}
          </DropdownMenuTrigger>
          <DropdownMenuContent align='start'>
            {conditionTypes.map((type) => (
              <DropdownMenuItem
                key={type}
                onClick={() =>
                  update((current) => ({
                    ...current,
                    children: [
                      ...(current.children ?? []),
                      createAutoResetCondition(
                        type,
                        props.weeklyWindows[0] ?? 'seven_day'
                      ),
                    ],
                  }))
                }
              >
                {t(CONDITION_LABELS[type])}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled || !canAddGroup}
          onClick={() =>
            update((current) => ({
              ...current,
              children: [
                ...(current.children ?? []),
                { id: newAutoResetNodeId(), op: 'and', children: [] },
              ],
            }))
          }
        >
          <FolderPlus data-icon='inline-start' />
          {t('Add group')}
        </Button>
        {props.path.length > 0 ? (
          <Button
            type='button'
            variant='ghost'
            size='icon-sm'
            className='ml-auto'
            disabled={props.disabled}
            aria-label={t('Remove group')}
            onClick={() => update(() => null)}
          >
            <Trash2 aria-hidden='true' />
          </Button>
        ) : null}
      </div>
      {children.length === 0 ? (
        <p className='text-muted-foreground text-xs'>
          {t('An empty group never matches')}
        </p>
      ) : (
        children.map((child, index) => (
          <AutoResetRuleNode
            key={child.id}
            {...props}
            node={child}
            path={[...props.path, index]}
            depth={props.depth + 1}
          />
        ))
      )}
    </div>
  )
}

export function AutoResetRuleNode(props: RuleNodeProps) {
  return isAutoResetGroup(props.node) ? (
    <GroupNode {...props} />
  ) : (
    <ConditionNode {...props} />
  )
}
