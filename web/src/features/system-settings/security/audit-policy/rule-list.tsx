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
import {
  ArrowDown,
  ArrowUp,
  Copy,
  GripVertical,
  Pencil,
  Trash2,
} from 'lucide-react'
import { useState, type DragEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTableRowActionMenu,
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import { Switch } from '@/components/ui/switch'
import { TableCell, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'

import type { AuditPolicyRule } from './api'
import {
  ACTION_LABELS,
  LIST_CONDITION_OPTIONS,
  RELAY_FORMAT_LABELS,
} from './lib/constants'
import { moveItem } from './lib/rule-form'

type RuleListProps = {
  rules: AuditPolicyRule[]
  defaultRule: AuditPolicyRule
  onRulesChange: (rules: AuditPolicyRule[]) => void
  /** index -1 edits the default rule. */
  onEdit: (index: number) => void
  onDuplicate: (index: number) => void
}

type RuleRow = { rule: AuditPolicyRule; index: number }

function useRuleSummaries() {
  const { t } = useTranslation()
  const matchSummary = (rule: AuditPolicyRule): string => {
    const match = rule.match
    const parts: string[] = []
    if (match.groups?.length) {
      parts.push(t('Groups: {{value}}', { value: match.groups.join(', ') }))
    }
    if (match.requested_groups?.length) {
      parts.push(
        t('Requested groups: {{value}}', {
          value: match.requested_groups.join(', '),
        })
      )
    }
    if (match.models?.length) {
      parts.push(t('Models: {{value}}', { value: match.models.join(', ') }))
    }
    if (match.exclude_models?.length) {
      parts.push(
        t('Except models: {{value}}', {
          value: match.exclude_models.join(', '),
        })
      )
    }
    if (match.user_ids?.length) {
      parts.push(t('Users: {{value}}', { value: match.user_ids.join(', ') }))
    }
    if (match.token_ids?.length) {
      parts.push(t('Tokens: {{value}}', { value: match.token_ids.join(', ') }))
    }
    const list = LIST_CONDITION_OPTIONS.find(
      (option) => option.value === match.list
    )
    if (match.list && list) parts.push(t(list.label))
    if (match.formats?.length) {
      parts.push(
        match.formats
          .map((format) => t(RELAY_FORMAT_LABELS[format] ?? format))
          .join(', ')
      )
    }
    return parts.length > 0 ? parts.join(' · ') : t('All requests')
  }
  const engineSummary = (rule: AuditPolicyRule): string => {
    const promptAudit = rule.prompt_audit.enabled
      ? t('Prompt audit: {{action}}', {
          action: t(ACTION_LABELS[rule.prompt_audit.action] ?? ''),
        })
      : t('Prompt audit off')
    const moderation = rule.content_moderation.enabled
      ? t('Content moderation: {{action}}', {
          action: t(ACTION_LABELS[rule.content_moderation.action] ?? ''),
        })
      : t('Content moderation off')
    return `${promptAudit} · ${moderation}`
  }
  return { matchSummary, engineSummary }
}

export function RuleList(props: RuleListProps) {
  const { t } = useTranslation()
  const summaries = useRuleSummaries()
  const [deleting, setDeleting] = useState<number | null>(null)
  const [dragging, setDragging] = useState<number | null>(null)
  const [dropTarget, setDropTarget] = useState<number | null>(null)
  const count = props.rules.length

  const move = (from: number, to: number) =>
    props.onRulesChange(moveItem(props.rules, from, to))
  const setEnabled = (index: number, enabled: boolean) =>
    props.onRulesChange(
      props.rules.map((rule, position) =>
        position === index ? { ...rule, enabled } : rule
      )
    )
  const endDrag = () => {
    setDragging(null)
    setDropTarget(null)
  }
  const onDrop = (event: DragEvent, index: number) => {
    event.preventDefault()
    if (dragging !== null) move(dragging, index)
    endDrag()
  }

  const columns: StaticDataTableColumn<RuleRow>[] = [
    { id: 'order', header: <span className='sr-only'>{t('Order')}</span> },
    { id: 'enabled', header: t('Enabled') },
    { id: 'rule', header: t('Rule') },
    { id: 'match', header: t('Conditions') },
    { id: 'engines', header: t('Engines and actions') },
    {
      id: 'actions',
      header: <span className='sr-only'>{t('Actions')}</span>,
    },
  ]
  const rows: RuleRow[] = [
    ...props.rules.map((rule, index) => ({ rule, index })),
    { rule: props.defaultRule, index: -1 },
  ]

  const renderRow = (row: RuleRow) => {
    const isDefault = row.index === -1
    const rule = row.rule
    const label = rule.name || rule.id
    return (
      <TableRow
        key={isDefault ? '__default' : `${row.index}-${rule.id}`}
        draggable={!isDefault && count > 1}
        onDragStart={(event) => {
          setDragging(row.index)
          event.dataTransfer.effectAllowed = 'move'
          event.dataTransfer.setData('text/plain', String(row.index))
        }}
        onDragOver={(event) => {
          if (isDefault || dragging === null) return
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
          setDropTarget(row.index)
        }}
        onDrop={(event) => onDrop(event, row.index)}
        onDragEnd={endDrag}
        className={cn(
          dragging === row.index && 'opacity-50',
          dropTarget === row.index &&
            dragging !== row.index &&
            'outline-primary outline-2 -outline-offset-2',
          isDefault && 'bg-muted/30'
        )}
      >
        <TableCell className='w-20'>
          {!isDefault && (
            <div className='flex items-center gap-0.5'>
              <GripVertical
                aria-hidden='true'
                className='text-muted-foreground size-4 cursor-grab'
              />
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                disabled={row.index === 0}
                onClick={() => move(row.index, row.index - 1)}
                aria-label={t('Move {{rule}} up', { rule: label })}
              >
                <ArrowUp />
              </Button>
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                disabled={row.index === count - 1}
                onClick={() => move(row.index, row.index + 1)}
                aria-label={t('Move {{rule}} down', { rule: label })}
              >
                <ArrowDown />
              </Button>
            </div>
          )}
        </TableCell>
        <TableCell>
          {isDefault ? (
            <span className='text-muted-foreground text-xs'>{t('Always')}</span>
          ) : (
            <Switch
              checked={rule.enabled}
              onCheckedChange={(next) => setEnabled(row.index, next)}
              aria-label={t('Enable rule {{rule}}', { rule: label })}
            />
          )}
        </TableCell>
        <TableCell className='max-w-48'>
          <div className='flex min-w-0 flex-col'>
            <span className='flex min-w-0 items-center gap-1.5'>
              <span className='truncate font-medium'>
                {isDefault ? t('Default rule') : label}
              </span>
              {rule.degraded && (
                <StatusBadge
                  label={t('Unreadable')}
                  variant='danger'
                  copyable={false}
                />
              )}
            </span>
            <span className='text-muted-foreground truncate font-mono text-xs'>
              {rule.id}
            </span>
          </div>
        </TableCell>
        <TableCell className='max-w-64 text-xs whitespace-normal'>
          {isDefault
            ? t('Requests no other rule matches')
            : summaries.matchSummary(rule)}
        </TableCell>
        <TableCell className='max-w-64 text-xs whitespace-normal'>
          {summaries.engineSummary(rule)}
        </TableCell>
        <TableCell>
          <div className='flex justify-end gap-1'>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              onClick={() => props.onEdit(row.index)}
              aria-label={t('Edit {{rule}}', {
                rule: isDefault ? t('Default rule') : label,
              })}
            >
              <Pencil />
            </Button>
            {!isDefault && (
              <DataTableRowActionMenu
                ariaLabel={t('More actions for {{rule}}', { rule: label })}
              >
                <DropdownMenuItem onClick={() => props.onDuplicate(row.index)}>
                  {t('Duplicate')}
                  <DropdownMenuShortcut>
                    <Copy size={16} />
                  </DropdownMenuShortcut>
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => setDeleting(row.index)}
                  className='text-destructive focus:text-destructive'
                >
                  {t('Delete')}
                  <DropdownMenuShortcut>
                    <Trash2 size={16} />
                  </DropdownMenuShortcut>
                </DropdownMenuItem>
              </DataTableRowActionMenu>
            )}
          </div>
        </TableCell>
      </TableRow>
    )
  }

  const deletingRule = deleting === null ? null : props.rules[deleting]
  return (
    <>
      <StaticDataTable
        columns={columns}
        data={rows}
        getRowKey={(row) => (row.index === -1 ? '__default' : row.rule.id)}
        renderRow={renderRow}
      />
      <p className='text-muted-foreground text-xs'>
        {t(
          'Rules are matched from top to bottom; the first enabled rule that matches decides. Drag a row or use the arrows to change the order.'
        )}
      </p>
      <ConfirmDialog
        open={deletingRule !== null && deletingRule !== undefined}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('Delete rule')}
        desc={t(
          'Rule {{rule}} will be removed from the table. It takes effect when you save the audit policy.',
          { rule: deletingRule?.id ?? '' }
        )}
        destructive
        confirmText={t('Delete')}
        handleConfirm={() => {
          if (deleting !== null) {
            props.onRulesChange(
              props.rules.filter((_, position) => position !== deleting)
            )
          }
          setDeleting(null)
        }}
      />
    </>
  )
}
