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

import { StructureFingerprintTable } from './structure-fingerprint-table'
import { StructureSignalTable } from './structure-signal-table'

/** Read-only request structure statistics: hashes and counts, no prompt text. */
export function PromptAuditStructureStats() {
  const { t } = useTranslation()
  return (
    <div className='space-y-6'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Request structure statistics are measurement only: they never change audit decisions and hold only hashes, counts, lengths and times, never prompt text.'
        )}
      </p>
      <section
        aria-labelledby='structure-fingerprints-title'
        className='space-y-2.5'
      >
        <h3 id='structure-fingerprints-title' className='text-sm font-semibold'>
          {t('System prompt fingerprints')}
        </h3>
        <StructureFingerprintTable />
      </section>
      <section
        aria-labelledby='structure-signals-title'
        className='space-y-2.5'
      >
        <h3 id='structure-signals-title' className='text-sm font-semibold'>
          {t('Structure signals by day')}
        </h3>
        <StructureSignalTable />
      </section>
    </div>
  )
}
