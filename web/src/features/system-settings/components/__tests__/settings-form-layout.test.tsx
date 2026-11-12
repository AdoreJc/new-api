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
import { describe, expect, it } from 'vitest'

import { SettingsForm, SettingsFormGrid } from '../settings-form-layout'

// Two fields share a row. When only one has a description, the other is
// stretched to the row height; its label and input rows must stay at the top
// so both labels and both inputs line up (jsdom has no layout, so the
// alignment rule is the contract checked here).
describe('settings form two-column alignment', () => {
  it.each([
    ['SettingsForm', SettingsForm],
    ['SettingsFormGrid', SettingsFormGrid],
  ] as const)(
    '%s keeps form fields packed at the top of their row',
    (_, Layout) => {
      render(
        <Layout>
          <div data-slot='form-item'>Client ID</div>
          <div data-slot='form-item'>Client secret</div>
        </Layout>
      )
      const layout = screen.getByText('Client ID').parentElement
      expect(layout?.className).toContain(
        '[&>[data-slot=form-item]]:content-start'
      )
    }
  )
})
