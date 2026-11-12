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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { PromptAuditKeywordOptions } from '../components/prompt-audit-keyword-options'
import { PromptAuditScanStatus } from '../components/prompt-audit-scan-status'
import {
  isScannedPromptAuditSearch,
  promptAuditRegexProblem,
} from '../lib/keyword-search'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('scans only regex searches and searches that include full prompts', () => {
  expect(isScannedPromptAuditSearch({ keyword: 'alice' })).toBe(false)
  expect(
    isScannedPromptAuditSearch({ keyword: 'a', keyword_fields: 'username' })
  ).toBe(false)
  expect(
    isScannedPromptAuditSearch({ keyword: 'a', keyword_mode: 'regex' })
  ).toBe(true)
  expect(
    isScannedPromptAuditSearch({
      keyword: 'a',
      keyword_fields: 'username,full_prompt',
    })
  ).toBe(true)
  expect(
    isScannedPromptAuditSearch({ keyword: '  ', keyword_mode: 'regex' })
  ).toBe(false)
})

it('accepts RE2-only syntax and reports invalid or overlong patterns', () => {
  expect(promptAuditRegexProblem('(?i)secret\\d+')).toBeNull()
  expect(promptAuditRegexProblem('(?P<word>secret)')).toBeNull()
  expect(promptAuditRegexProblem('begin(?s).*end')).toBeNull()
  expect(promptAuditRegexProblem('(?i:secret) code')).toBeNull()
  expect(promptAuditRegexProblem('(unclosed')).toBe('invalid')
  expect(promptAuditRegexProblem('a'.repeat(513))).toBe('too_long')
  expect(promptAuditRegexProblem('')).toBeNull()
})

it('shows an RE2 syntax hint under a regex the browser cannot parse', () => {
  render(
    <PromptAuditKeywordOptions
      draft={{ keyword: '(unclosed', keyword_mode: 'regex' }}
      onChange={vi.fn()}
    />
  )
  expect(screen.getByRole('status')).toHaveTextContent('RE2')
})

it('turning regex off clears the case sensitive setting', async () => {
  const onChange = vi.fn()
  render(
    <PromptAuditKeywordOptions
      draft={{ keyword: 'a', keyword_mode: 'regex', case_sensitive: true }}
      onChange={onChange}
    />
  )
  await userEvent.click(screen.getByRole('switch', { name: 'Regex' }))
  expect(onChange).toHaveBeenCalledWith({
    keyword_mode: undefined,
    case_sensitive: undefined,
  })
})

it('case sensitivity is disabled outside regex mode', () => {
  render(<PromptAuditKeywordOptions draft={{}} onChange={vi.fn()} />)
  expect(
    screen.getByRole('switch', { name: 'Case sensitive' })
  ).toHaveAttribute('aria-disabled', 'true')
})

it('a truncated scan offers to continue below the last id read', async () => {
  const onContinue = vi.fn()
  render(
    <PromptAuditScanStatus
      page={{ items: [], truncated: true, next_cursor: 42, scanned: 20000 }}
      continued={false}
      loading={false}
      onContinue={onContinue}
      onRestart={vi.fn()}
    />
  )
  expect(screen.getByRole('status')).toHaveTextContent('#42')
  await userEvent.click(screen.getByRole('button', { name: 'Continue search' }))
  expect(onContinue).toHaveBeenCalledWith(42)
  expect(
    screen.queryByRole('button', { name: 'Back to newest' })
  ).not.toBeInTheDocument()
})

it('a scan that timed out before any row offers to retry from its cursor', async () => {
  const onContinue = vi.fn()
  render(
    <PromptAuditScanStatus
      page={{
        items: [],
        truncated: true,
        next_cursor: 0,
        scanned: 0,
        stop_reason: 'time_budget',
      }}
      continued={false}
      loading={false}
      onContinue={onContinue}
      onRestart={vi.fn()}
    />
  )
  expect(screen.getByRole('status')).toHaveTextContent(
    'The search ran out of time before reading any event.'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Continue search' }))
  expect(onContinue).toHaveBeenCalledWith(0)
})

it('a finished scan offers no continuation', () => {
  render(
    <PromptAuditScanStatus
      page={{ items: [], truncated: false, next_cursor: 0, scanned: 5 }}
      continued
      loading={false}
      onContinue={vi.fn()}
      onRestart={vi.fn()}
    />
  )
  expect(
    screen.queryByRole('button', { name: 'Continue search' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Back to newest' })
  ).toBeInTheDocument()
})
