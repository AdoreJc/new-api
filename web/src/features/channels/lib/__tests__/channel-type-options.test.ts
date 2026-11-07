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
import { describe, expect, test } from 'vitest'

import {
  CHANNEL_TYPE_CLAUDE_OAUTH,
  CHANNEL_TYPE_OPTIONS,
  CHANNEL_TYPE_TASK_PLUGIN,
  channelTypeOptionsForTaskPluginBind,
} from '../../constants'
import { getChannelTypeConfig } from '../channel-type-config'
import { getChannelTypeIcon } from '../channel-utils'

describe('channel type options for task plugin bind', () => {
  test('hides the task plugin type when the caller cannot bind', () => {
    const options = channelTypeOptionsForTaskPluginBind(false)

    expect(
      options.some((option) => option.value === CHANNEL_TYPE_TASK_PLUGIN)
    ).toBe(false)
  })

  test('shows the task plugin type when the caller can bind', () => {
    const options = channelTypeOptionsForTaskPluginBind(true)

    expect(options).toEqual(CHANNEL_TYPE_OPTIONS)
    expect(
      options.some((option) => option.value === CHANNEL_TYPE_TASK_PLUGIN)
    ).toBe(true)
  })
})

describe('Claude Subscription (OAuth) channel type icon', () => {
  test('uses the Claude icon instead of the OpenAI fallback', () => {
    expect(getChannelTypeIcon(CHANNEL_TYPE_CLAUDE_OAUTH)).toBe('Claude')
    expect(getChannelTypeIcon(CHANNEL_TYPE_CLAUDE_OAUTH)).toBe(
      getChannelTypeIcon(14)
    )
  })

  test('shares the Anthropic channel config icon', () => {
    expect(getChannelTypeConfig(CHANNEL_TYPE_CLAUDE_OAUTH).icon).toBe(
      getChannelTypeConfig(14).icon
    )
    expect(getChannelTypeConfig(CHANNEL_TYPE_CLAUDE_OAUTH).name).toBe(
      'Claude Subscription (OAuth)'
    )
  })
})
