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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

// Matches controller/email_oauth.go. The provider redirects back to the email
// settings page, which posts code and state with the console session.
export const EMAIL_OAUTH_CALLBACK_PATH = '/system-settings/operations/email'

type BackendBody<T> = { success: boolean; message?: string; data?: T }

// sender is the sender address about to be saved; its permission is
// requested with this authorization.
export async function startEmailOAuthAuthorization(sender: string) {
  const res = await api.post<BackendBody<{ authorization_url: string }>>(
    '/api/option/email_oauth/authorize',
    { sender }
  )
  return requireServerSuccess(res.data)
}

export async function completeEmailOAuthAuthorization(request: {
  state: string
  code: string
  error: string
}) {
  const res = await api.post<BackendBody<{ email: string }>>(
    '/api/option/email_oauth/callback',
    request
  )
  return requireServerSuccess(res.data)
}

// Sends a test message from the sender address to the authorized mailbox.
export async function sendEmailOAuthTestMessage() {
  const res = await api.post<BackendBody<{ to: string; from: string }>>(
    '/api/option/email_oauth/test'
  )
  return requireServerSuccess(res.data)
}

export async function revokeEmailOAuthAuthorization() {
  const res = await api.delete<BackendBody<null>>(
    '/api/option/email_oauth/authorization'
  )
  return requireServerSuccess(res.data)
}
