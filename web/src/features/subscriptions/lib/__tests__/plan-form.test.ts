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

import { subscriptionPlanSchema } from '../../types'
import {
  PLAN_FORM_DEFAULTS,
  formValuesToPlanPayload,
  planToFormValues,
} from '../plan-form'

const basePlan = {
  id: 1,
  title: 'Dedicated Claude',
  price_amount: 10,
  duration_unit: 'month',
  duration_value: 1,
  quota_reset_period: 'never',
  enabled: true,
  sort_order: 0,
  max_purchase_per_user: 0,
  total_amount: 0,
}

describe('shared pool fallback switch', () => {
  test('new plans start with the fallback off', () => {
    expect(PLAN_FORM_DEFAULTS.fallback_to_shared_pool_on_limit).toBe(false)
  })

  test('a plan saved before the switch existed loads with the fallback off', () => {
    const plan = subscriptionPlanSchema.parse(basePlan)

    expect(planToFormValues(plan).fallback_to_shared_pool_on_limit).toBe(false)
  })

  test('an enabled fallback is loaded and sent back to the API', () => {
    const plan = subscriptionPlanSchema.parse({
      ...basePlan,
      fallback_to_shared_pool_on_limit: true,
    })

    const payload = formValuesToPlanPayload(planToFormValues(plan))

    expect(payload.plan.fallback_to_shared_pool_on_limit).toBe(true)
  })
})
