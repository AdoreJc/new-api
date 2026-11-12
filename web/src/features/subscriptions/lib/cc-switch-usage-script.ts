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

export type CcSwitchUsageScriptLabels = {
  /** Shown when the query fails and the server sent no message */
  failed: string
  /** Shown when the key's account has no bound subscription */
  noSubscription: string
  /** Text before the time left until a window resets */
  resetsInPrefix: string
  /** Text after the time left until a window resets */
  resetsInSuffix: string
  /** Time left of an hour or more, with the hour and minute placeholders */
  hoursMinutes: string
  /** Time left under an hour, with the minute placeholder */
  minutes: string
}

/** Placeholders in the hoursMinutes and minutes labels */
export const CC_SWITCH_HOURS_PLACEHOLDER = '__HOURS__'
export const CC_SWITCH_MINUTES_PLACEHOLDER = '__MINUTES__'

/**
 * Builds a CC Switch usage script ("Custom" template) that reads
 * GET /api/usage/subscription with the provider's own API key and returns
 * one CC Switch entry per usage window, plus an invalid entry for a bound
 * subscription whose usage could not be fetched. CC Switch substitutes {{baseUrl}}
 * and {{apiKey}}; a trailing /v1 in the base URL is removed. The script
 * only issues a GET: the endpoint is read-only.
 */
export function buildCcSwitchUsageScript(
  provider: string,
  labels: CcSwitchUsageScriptLabels
): string {
  const query = provider === 'codex' ? 'codex' : 'claude'
  const text = (value: string) => JSON.stringify(value)
  return `({
  request: {
    url: "{{baseUrl}}".replace(/\\/+$/, "").replace(/\\/v1$/, "") + "/api/usage/subscription?provider=${query}",
    method: "GET",
    headers: { "Authorization": "Bearer {{apiKey}}" }
  },
  extractor: function (r) {
    if (!r || r.success !== true || !r.data) {
      return { isValid: false, invalidMessage: (r && r.message) || ${text(labels.failed)} };
    }
    var items = r.data.items || [];
    if (items.length === 0) {
      return { isValid: false, invalidMessage: ${text(labels.noSubscription)} };
    }
    var now = Date.now();
    return items.map(function (it) {
      if (it.isValid === false) {
        return { planName: it.planName, isValid: false, invalidMessage: it.invalidMessage || ${text(labels.failed)} };
      }
      var notes = [];
      var t = Date.parse(it.resetsAt || "");
      if (!isNaN(t) && t > now) {
        var m = Math.round((t - now) / 60000);
        var left = m >= 60
          ? ${text(labels.hoursMinutes)}.replace("${CC_SWITCH_HOURS_PLACEHOLDER}", String(Math.floor(m / 60))).replace("${CC_SWITCH_MINUTES_PLACEHOLDER}", String(m % 60))
          : ${text(labels.minutes)}.replace("${CC_SWITCH_MINUTES_PLACEHOLDER}", String(m));
        notes.push(${text(labels.resetsInPrefix)} + left + ${text(labels.resetsInSuffix)});
      }
      if (it.extra) notes.push(it.extra);
      return {
        planName: it.planName,
        used: it.used,
        total: it.total,
        remaining: it.remaining,
        unit: it.unit,
        extra: notes.join(" · ")
      };
    });
  }
})`
}
