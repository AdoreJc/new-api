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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { buildCcSwitchUsageScript } from '../cc-switch-usage-script'

type UsageScript = {
  request: { url: string; method: string; headers: Record<string, string> }
  extractor: (response: unknown) => unknown
}

const labels = {
  failed: 'Failed to fetch usage',
  noSubscription: 'No bound "subscription"',
  resetsInPrefix: 'Resets in ',
  resetsInSuffix: '',
}

// CC Switch substitutes the placeholders as plain text and evaluates the
// script as a JavaScript expression.
function loadScript(provider: string, baseUrl: string): UsageScript {
  const source = buildCcSwitchUsageScript(provider, labels)
    .replaceAll('{{baseUrl}}', baseUrl)
    .replaceAll('{{apiKey}}', 'sk-test')
  return new Function(`return ${source}`)() as UsageScript
}

afterEach(() => {
  vi.useRealTimers()
})

describe('buildCcSwitchUsageScript', () => {
  test('requests the read-only endpoint for the provider without the /v1 suffix', () => {
    const script = loadScript('codex', 'https://relay.example.com/v1/')

    expect(script.request).toEqual({
      url: 'https://relay.example.com/api/usage/subscription?provider=codex',
      method: 'GET',
      headers: { Authorization: 'Bearer sk-test' },
    })
  })

  test('maps every endpoint item to one CC Switch entry with the time left and server notes', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-12T17:00:00Z'))
    const script = loadScript('claude', 'https://relay.example.com')

    const entries = script.extractor({
      success: true,
      message: '',
      data: {
        subscriptions: [],
        items: [
          {
            planName: 'Claude 5h',
            used: 23,
            total: 100,
            remaining: 77,
            unit: '%',
            resetsAt: '2026-11-12T19:05:00Z',
          },
          {
            planName: 'Claude 7d Fable',
            used: 5,
            total: 100,
            remaining: 95,
            unit: '%',
            resetsAt: '2026-11-12T17:30:00Z',
            extra: 'sampled from relayed responses',
          },
        ],
      },
    })

    expect(entries).toEqual([
      {
        planName: 'Claude 5h',
        used: 23,
        total: 100,
        remaining: 77,
        unit: '%',
        extra: 'Resets in 2h5m',
      },
      {
        planName: 'Claude 7d Fable',
        used: 5,
        total: 100,
        remaining: 95,
        unit: '%',
        extra: 'Resets in 30m · sampled from relayed responses',
      },
    ])
  })

  test('reports a failed query or an account without bindings as invalid', () => {
    const script = loadScript('claude', 'https://relay.example.com')

    expect(
      script.extractor({
        success: false,
        message: 'Upstream returned status 500',
      })
    ).toEqual({
      isValid: false,
      invalidMessage: 'Upstream returned status 500',
    })
    expect(script.extractor(null)).toEqual({
      isValid: false,
      invalidMessage: 'Failed to fetch usage',
    })
    expect(
      script.extractor({
        success: true,
        message: 'no bound subscription',
        data: { subscriptions: [], items: [] },
      })
    ).toEqual({ isValid: false, invalidMessage: 'No bound "subscription"' })
  })
})
