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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { createRule } from '../lib/rule-form'
import { RuleEditorDialog } from '../rule-editor-dialog'

const REROUTE_NOTICE =
  "Reroute only applies to verified allowlist users; other users get the fallback. Make sure rerouting complies with the upstream model's terms of use and licensing."

function renderEditor(onSave = vi.fn()) {
  render(
    <RuleEditorDialog
      rule={createRule([])}
      isDefault={false}
      otherIds={[]}
      promptAuditCategories={['jailbreak']}
      moderationCategories={['harassment']}
      relayFormats={['openai']}
      onSave={onSave}
      onOpenChange={() => undefined}
    />
  )
  return onSave
}

it('shows the reroute target and compliance notice only while a reroute action is chosen', async () => {
  const user = userEvent.setup()
  renderEditor()
  await screen.findByRole('dialog')
  expect(screen.queryByText(REROUTE_NOTICE)).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Reroute model')).not.toBeInTheDocument()

  await user.click(screen.getByRole('combobox', { name: 'Action' }))
  await user.click(await screen.findByRole('option', { name: 'Reroute' }))

  expect(screen.getByText(REROUTE_NOTICE)).toBeVisible()
  expect(screen.getByLabelText('Reroute model')).toBeVisible()

  await user.click(screen.getByRole('combobox', { name: 'Action' }))
  await user.click(await screen.findByRole('option', { name: 'Block' }))

  expect(screen.queryByText(REROUTE_NOTICE)).not.toBeInTheDocument()
})

it('keeps the dialog open when a reroute rule has no target', async () => {
  const user = userEvent.setup()
  const onSave = renderEditor()
  await screen.findByRole('dialog')

  await user.click(screen.getByRole('combobox', { name: 'Action' }))
  await user.click(await screen.findByRole('option', { name: 'Reroute' }))
  await user.click(screen.getByRole('button', { name: 'Apply' }))

  expect(
    await screen.findByText('Enter a model or group to reroute to')
  ).toBeVisible()
  expect(onSave).not.toHaveBeenCalled()
})
