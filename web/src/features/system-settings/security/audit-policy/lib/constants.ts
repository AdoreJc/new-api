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
  PROMPT_AUDIT_MODES,
  PROMPT_AUDIT_SCAN_SCOPES,
} from '../../prompt-audit/lib/form'

/** Option lists of the rule editor; every label is an English i18n key. */
export type PolicyOption = { value: string; label: string }

/** Form value for "use the engine setting" or "same as the rule". */
export const INHERIT = 'inherit'

export const ACTION_OPTIONS: PolicyOption[] = [
  { value: 'observe', label: 'Observe (record only)' },
  { value: 'block', label: 'Block' },
  { value: 'reroute', label: 'Reroute' },
]

export const CATEGORY_ACTION_OPTIONS: PolicyOption[] = [
  { value: INHERIT, label: 'Rule action' },
  ...ACTION_OPTIONS,
  { value: 'ignore', label: 'Ignore' },
]

export const MIN_LEVEL_OPTIONS: PolicyOption[] = [
  { value: 'controversial', label: 'Controversial or unsafe' },
  { value: 'unsafe', label: 'Unsafe only' },
]

export const CATEGORY_LEVEL_OPTIONS: PolicyOption[] = [
  { value: INHERIT, label: 'Rule level' },
  ...MIN_LEVEL_OPTIONS,
  { value: 'off', label: 'Off' },
]

export const LIST_CONDITION_OPTIONS: PolicyOption[] = [
  { value: INHERIT, label: 'Any user' },
  { value: 'allowlist', label: 'Allowlist' },
  { value: 'verified', label: 'Verified allowlist' },
  { value: 'blocklist', label: 'Blocklist' },
  { value: 'unlisted', label: 'Users on neither list' },
]

export const FAIL_POLICY_OPTIONS: PolicyOption[] = [
  { value: 'open', label: 'Allow the request (fail open)' },
  { value: 'closed', label: 'Reject the request (fail closed)' },
]

export const REROUTE_FALLBACK_OPTIONS: PolicyOption[] = [
  { value: 'observe', label: 'Observe (record only)' },
  { value: 'block', label: 'Block' },
]

export const SCAN_SCOPE_OPTIONS: PolicyOption[] = [
  { value: INHERIT, label: 'Use the prompt audit setting' },
  ...PROMPT_AUDIT_SCAN_SCOPES.map((scope) => ({
    value: scope.value,
    label: scope.label,
  })),
]

export const KEYWORD_MODE_OPTIONS: PolicyOption[] = [
  { value: INHERIT, label: 'Use the content moderation setting' },
  { value: 'api_only', label: 'Moderation API only' },
  { value: 'keyword_only', label: 'Keywords only (no API call)' },
  {
    value: 'keyword_and_api',
    label: 'Keywords flag at once; other inputs go to the API',
  },
  {
    value: 'keyword_gated_api',
    label: 'Only inputs that match a list are reviewed by the API',
  },
]

export const RELAY_FORMAT_LABELS: Record<string, string> = {
  openai: 'OpenAI Chat Completions',
  claude: 'Claude Messages',
  gemini: 'Gemini',
  openai_responses: 'OpenAI Responses',
  openai_responses_compaction: 'OpenAI Responses compaction',
  openai_image: 'OpenAI Images',
  responses_websocket: 'Responses WebSocket',
}

export const ACTION_LABELS: Record<string, string> = {
  observe: 'Observe (record only)',
  block: 'Block',
  reroute: 'Reroute',
  ignore: 'Ignore',
}

export const FAIL_POLICY_LABELS: Record<string, string> = {
  open: 'Fail open',
  closed: 'Fail closed',
}

/** How an engine runs for a request in the test result. */
export const ENGINE_MODE_LABELS: Record<string, string> = {
  off: 'Off',
  async: 'In the background (never blocks)',
  sync: 'Before the request (can block)',
}

export const PROMPT_AUDIT_MODE_LABELS: Record<string, string> =
  Object.fromEntries(PROMPT_AUDIT_MODES.map((mode) => [mode.value, mode.label]))

export const CONTENT_MODERATION_MODE_LABELS: Record<string, string> = {
  off: 'Off',
  observe: 'Observe (check in the background, never block)',
  pre_block: 'Block (check before billing, reject flagged)',
}
