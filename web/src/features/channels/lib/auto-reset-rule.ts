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

// Condition tree of the automatic reset-credit setting. The server enforces
// the same limits; the editor uses them to disable actions that would fail.

export const AUTO_RESET_MAX_DEPTH = 3
export const AUTO_RESET_MAX_NODES = 20
export const AUTO_RESET_MAX_ALARMS = 20

export type AutoResetConditionType =
  | 'five_hour_used_pct'
  | 'weekly_used_pct'
  | 'at_time'
  | 'credit_expires_within'

export type AutoResetRule = {
  /** Editor-only identity used as the React key; the server ignores it. */
  id?: string
  op?: 'and' | 'or'
  children?: AutoResetRule[]
  type?: AutoResetConditionType
  threshold?: number
  window?: string
  hours?: number
  times?: string[]
}

export type AutoResetConfig = {
  enabled: boolean
  rule?: AutoResetRule
  min_interval_minutes: number
  max_per_day: number
  max_per_week: number
  skip_if_reset_within_minutes: number
  alarm_ignores_reset_soon: boolean
  expiring_ignores_reset_soon: boolean
  updated_by?: number
  updated_at?: number
}

export type AutoResetHit = {
  type: AutoResetConditionType
  window?: string
  threshold?: number
  hours?: number
  value?: number
  alarm?: string
}

export type AutoResetDecision = {
  channel_id: number
  evaluated_at: number
  dry_run: boolean
  matched: boolean
  hits: AutoResetHit[]
  reason: string
  result?: string
  usage?: Record<string, { utilization: number; resets_at: string }>
  credits?: { available: number; earliest_expiry?: string }
  next_allowed_at?: number
}

export type AutoResetState = {
  /** Automatic redemptions that may have used a credit, last 7 days. */
  redeemed_at?: number[]
  /** Last redemption from any source; the minimum interval counts from it. */
  last_redeem_at?: number
  last_redeem_source?: string
  /** Set while a redemption request is out, or after one was interrupted. */
  in_flight_since?: number
  in_flight_source?: string
  /** The in-flight record outlived any request: interrupted, outcome unknown. */
  in_flight_interrupted?: boolean
  last_attempt_at?: number
  last_result?: string
  last_success?: boolean
  last_hits?: AutoResetHit[]
  fail_count?: number
  backoff_until?: number
  fired_alarms?: string[]
  last_skip_reason?: string
  last_skip_at?: number
}

export type AutoResetView = {
  config: AutoResetConfig
  state: AutoResetState
  last_decision?: AutoResetDecision
  next_allowed_at?: number
  next_allowed_reason?: string
  next_alarm_at?: string
  alarm_grace_minutes: number
  weekly_windows: string[]
}

/** Path of a node: child indexes from the root. */
export type AutoResetRulePath = number[]

let autoResetNodeSeq = 0

export function newAutoResetNodeId(): string {
  autoResetNodeSeq += 1
  return `rule-${autoResetNodeSeq}`
}

/** Returns a copy of node in which every node has an editor id. */
export function withAutoResetIds(node: AutoResetRule): AutoResetRule {
  return {
    ...node,
    id: node.id ?? newAutoResetNodeId(),
    children: node.children?.map(withAutoResetIds),
  }
}

export function isAutoResetGroup(node: AutoResetRule): boolean {
  return node.op === 'and' || node.op === 'or'
}

export function createAutoResetCondition(
  type: AutoResetConditionType,
  weeklyWindow = 'seven_day'
): AutoResetRule {
  switch (type) {
    case 'five_hour_used_pct':
      return { id: newAutoResetNodeId(), type, threshold: 90 }
    case 'weekly_used_pct':
      return {
        id: newAutoResetNodeId(),
        type,
        threshold: 90,
        window: weeklyWindow,
      }
    case 'credit_expires_within':
      return { id: newAutoResetNodeId(), type, hours: 24 }
    case 'at_time':
      return { id: newAutoResetNodeId(), type, times: [] }
  }
}

export function countAutoResetNodes(node: AutoResetRule | undefined): number {
  if (!node) return 0
  return (
    1 +
    (node.children ?? []).reduce(
      (sum, child) => sum + countAutoResetNodes(child),
      0
    )
  )
}

export function countAutoResetAlarms(node: AutoResetRule | undefined): number {
  if (!node) return 0
  if (!isAutoResetGroup(node)) return node.times?.length ?? 0
  return (node.children ?? []).reduce(
    (sum, child) => sum + countAutoResetAlarms(child),
    0
  )
}

export function countAutoResetConditions(
  node: AutoResetRule | undefined
): number {
  if (!node) return 0
  if (!isAutoResetGroup(node)) return 1
  return (node.children ?? []).reduce(
    (sum, child) => sum + countAutoResetConditions(child),
    0
  )
}

/**
 * Returns a copy of root with the node at path replaced by update(node).
 * update may return null to remove the node (not allowed for the root).
 */
export function updateAutoResetNode(
  root: AutoResetRule,
  path: AutoResetRulePath,
  update: (node: AutoResetRule) => AutoResetRule | null
): AutoResetRule {
  if (path.length === 0) {
    return update(root) ?? root
  }
  const [index, ...rest] = path
  const children = [...(root.children ?? [])]
  if (index < 0 || index >= children.length) return root
  if (rest.length === 0) {
    const next = update(children[index])
    if (next === null) {
      children.splice(index, 1)
    } else {
      children[index] = next
    }
  } else {
    children[index] = updateAutoResetNode(children[index], rest, update)
  }
  return { ...root, children }
}

/** Whether a child can be added under a group at path depth (root = 1). */
export function canAddAutoResetChild(
  root: AutoResetRule,
  depth: number,
  childIsGroup: boolean
): boolean {
  if (countAutoResetNodes(root) >= AUTO_RESET_MAX_NODES) return false
  const childDepth = depth + 1
  if (childDepth > AUTO_RESET_MAX_DEPTH) return false
  // A new group must leave room for at least one condition below it.
  return !childIsGroup || childDepth < AUTO_RESET_MAX_DEPTH
}

/** Adds an alarm (ISO string) to an at_time node, keeping the list sorted. */
export function addAutoResetAlarm(
  node: AutoResetRule,
  isoTime: string
): AutoResetRule {
  const times = new Set(node.times ?? [])
  times.add(isoTime)
  return { ...node, times: [...times].sort() }
}

export function defaultAutoResetConfig(): AutoResetConfig {
  return {
    enabled: false,
    rule: {
      op: 'or',
      children: [createAutoResetCondition('five_hour_used_pct')],
    },
    min_interval_minutes: 60,
    max_per_day: 2,
    max_per_week: 5,
    skip_if_reset_within_minutes: 15,
    alarm_ignores_reset_soon: true,
    expiring_ignores_reset_soon: true,
  }
}

/** Editor state from the stored setting; a bare condition is wrapped in a group. */
export function toEditableAutoResetConfig(
  config: AutoResetConfig | undefined
): AutoResetConfig {
  const base = config ?? defaultAutoResetConfig()
  let rule = base.rule
  if (!rule) {
    rule = { op: 'or', children: [] }
  } else if (!isAutoResetGroup(rule)) {
    rule = { op: 'or', children: [rule] }
  }
  return { ...base, rule: withAutoResetIds(rule) }
}
