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
  formatBillingCurrencyFromUSD,
  formatPaymentCurrencyAmount,
  type CurrencyFormatOptions,
} from '@/lib/currency'

/**
 * Currency of the recharge price display. The "Price" setting (`priceRate`)
 * is the RMB a user pays through the recharge channel for $1 of credit.
 */
export const RECHARGE_PRICE_CURRENCY_LABEL = 'CNY'

export type ModelPriceDisplayMode = {
  showRechargePrice?: boolean
  priceRate?: number
}

/**
 * Format a model price stored in USD for the model marketplace.
 *
 * - Standard mode: the site display currency (formatBillingCurrencyFromUSD).
 * - Recharge mode: what the price costs through the RMB recharge channel,
 *   `amountUSD × priceRate`, always shown as ¥. It does not depend on the
 *   quota display type or on `usd_exchange_rate`.
 *
 * Example: quota display USD, priceRate 6.8, model price $2 → "¥13.6".
 */
export function formatModelPriceFromUSD(
  amountUSD: number,
  mode: ModelPriceDisplayMode,
  options: CurrencyFormatOptions
): string {
  if (!mode.showRechargePrice) {
    return formatBillingCurrencyFromUSD(amountUSD, options)
  }
  return formatPaymentCurrencyAmount(amountUSD * (mode.priceRate ?? 1), options)
}
