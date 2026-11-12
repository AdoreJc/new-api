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
export const CONTENT_AUDIT_QUERY_KEYS = {
  config: ['content-audit', 'config'] as const,
  runtime: ['content-audit', 'runtime'] as const,
  events: ['content-audit', 'events'] as const,
  event: (id: number) => ['content-audit', 'event', id] as const,
}

export const CONTENT_AUDIT_RUNTIME_POLL_MS = 10_000
export const CONTENT_AUDIT_MAX_ENDPOINTS = 8
export const CONTENT_AUDIT_MAX_BATCH_DELETE = 500

/** i18n label keys for the scanner ids the backend reports. */
export const CONTENT_AUDIT_SCANNER_LABEL_KEYS: Record<string, string> = {
  violent: 'Violence',
  non_violent_illegal_acts: 'Non-violent illegal acts',
  sexual_content_or_sexual_acts: 'Sexual content',
  pii: 'Personal information (PII)',
  suicide_and_self_harm: 'Suicide and self-harm',
  unethical_acts: 'Unethical acts',
  politically_sensitive_topics: 'Politically sensitive topics',
  copyright_violation: 'Copyright violation',
  jailbreak: 'Jailbreak attempts',
}

export function contentAuditScannerLabelKey(id: string): string {
  return CONTENT_AUDIT_SCANNER_LABEL_KEYS[id] ?? id
}
