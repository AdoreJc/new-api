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
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'

import { completeEmailOAuthAuthorization } from '../integrations/email-oauth-api'

export type EmailOAuthCallback = { state: string; code: string; error: string }

/** The provider returns to the settings page with state and code or error. */
export function readEmailOAuthCallback(
  search: string
): EmailOAuthCallback | null {
  const params = new URLSearchParams(search)
  const state = params.get('state') ?? ''
  const code = params.get('code') ?? ''
  const error = params.get('error') ?? ''
  if (!state || (!code && !error)) return null
  return { state, code, error }
}

/** Completes a provider redirect once and removes code and state from the URL. */
export function useEmailOAuthCallback(callback: EmailOAuthCallback | null) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const handled = useRef(false)
  useEffect(() => {
    if (!callback || handled.current) return
    handled.current = true
    // Not a navigation: only scrub the one-time code from the address bar.
    window.history.replaceState(
      window.history.state,
      '',
      window.location.pathname
    )
    const complete = async () => {
      try {
        const response = await completeEmailOAuthAuthorization(callback)
        toast.success(
          t('Sender mailbox authorized: {{email}}', {
            email: response.data?.email ?? '',
          })
        )
      } catch (error) {
        handleServerError(error)
      } finally {
        await queryClient.invalidateQueries({ queryKey: ['system-options'] })
      }
    }
    void complete()
  }, [callback, queryClient, t])
}
