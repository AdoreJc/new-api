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
import { AlertTriangle, ExternalLink, Link2, Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { createServerError } from '@/lib/server-error-message'

import {
  exchangeChannelOAuth,
  startChannelOAuth,
  type OAuthExchangeResponse,
  type OAuthProvider,
} from '../../api'

export type OAuthAuthorizationResult = NonNullable<
  OAuthExchangeResponse['data']
> & { key: string }

type OAuthAuthorizationDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  provider: OAuthProvider
  /** Channel proxy used for the token exchange request, if any */
  proxy?: string
  onComplete: (result: OAuthAuthorizationResult) => void
}

const PROVIDER_COPY: Record<
  OAuthProvider,
  {
    titleKey: string
    introKey: string
    pasteLabelKey: string
    pasteHintKey: string
    pastePlaceholder: string
  }
> = {
  claude: {
    titleKey: 'Authorize Claude Subscription',
    introKey:
      'Open the authorization link in a browser signed in to the Claude account you want to use. After approving, the page shows a code in the form code#state; paste the whole value below.',
    pasteLabelKey: 'Authorization code (code#state)',
    pasteHintKey:
      'The link is valid for 30 minutes and can be used once. The code is exchanged by this server and never leaves it.',
    pastePlaceholder: 'xxxxxxxx#yyyyyyyy',
  },
  codex: {
    titleKey: 'Authorize ChatGPT Subscription (Codex)',
    introKey:
      'Open the authorization link in a browser signed in to the ChatGPT account you want to use. After approving, the browser is redirected to http://localhost:1455/auth/callback, which usually cannot load; copy that full URL from the address bar and paste it below.',
    pasteLabelKey: 'Callback URL',
    pasteHintKey:
      'The link is valid for 30 minutes and can be used once. The code is exchanged by this server and never leaves it.',
    pastePlaceholder: 'http://localhost:1455/auth/callback?code=...&state=...',
  },
}

export function OAuthAuthorizationDialog(props: OAuthAuthorizationDialogProps) {
  const { t } = useTranslation()
  const copy = PROVIDER_COPY[props.provider]
  const [sessionId, setSessionId] = useState('')
  const [authUrl, setAuthUrl] = useState('')
  const [code, setCode] = useState('')
  const [isStarting, setIsStarting] = useState(false)
  const [isExchanging, setIsExchanging] = useState(false)
  const [errorMessage, setErrorMessage] = useState('')

  const reset = () => {
    setSessionId('')
    setAuthUrl('')
    setCode('')
    setIsStarting(false)
    setIsExchanging(false)
    setErrorMessage('')
  }

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      reset()
    }
    props.onOpenChange(nextOpen)
  }

  const handleStart = async () => {
    if (isStarting) return
    setIsStarting(true)
    setErrorMessage('')
    setCode('')
    try {
      const res = await startChannelOAuth(props.provider)
      if (!res.success || !res.data?.auth_url || !res.data.session_id) {
        throw createServerError(
          res,
          t('Failed to generate the authorization link')
        )
      }
      setSessionId(res.data.session_id)
      setAuthUrl(res.data.auth_url)
    } catch (error) {
      setErrorMessage(
        error instanceof Error
          ? error.message
          : t('Failed to generate the authorization link')
      )
    } finally {
      setIsStarting(false)
    }
  }

  const handleExchange = async () => {
    const trimmed = code.trim()
    if (!sessionId || !trimmed || isExchanging) return
    setIsExchanging(true)
    setErrorMessage('')
    try {
      const res = await exchangeChannelOAuth(props.provider, {
        session_id: sessionId,
        code: trimmed,
        proxy: props.proxy?.trim() || undefined,
      })
      if (!res.success || !res.data?.key) {
        throw createServerError(res, t('Authorization failed'))
      }
      props.onComplete({ ...res.data, key: res.data.key })
      handleOpenChange(false)
    } catch (error) {
      // A rejected exchange consumed the session; the admin must start over.
      setSessionId('')
      setAuthUrl('')
      setErrorMessage(
        error instanceof Error ? error.message : t('Authorization failed')
      )
    } finally {
      setIsExchanging(false)
    }
  }

  const hasLink = Boolean(authUrl && sessionId)

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={t(copy.titleKey)}
      description={t(copy.introKey)}
      contentClassName='sm:max-w-[640px]'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => handleOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={handleExchange}
            disabled={!hasLink || !code.trim() || isExchanging}
          >
            {isExchanging ? (
              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
            ) : null}
            {isExchanging ? t('Exchanging...') : t('Complete authorization')}
          </Button>
        </>
      }
    >
      <div className='flex flex-col gap-4'>
        <div className='flex flex-col gap-2'>
          <Label htmlFor='oauth-authorization-link'>
            {t('Step 1: Generate the authorization link')}
          </Label>
          <div className='flex flex-col gap-2 sm:flex-row sm:items-center'>
            <Input
              id='oauth-authorization-link'
              readOnly
              value={authUrl}
              placeholder={t('Click Generate to create a link')}
              className='font-mono text-xs'
            />
            <div className='flex shrink-0 items-center gap-1'>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={handleStart}
                disabled={isStarting || isExchanging}
              >
                {isStarting ? (
                  <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                ) : (
                  <Link2 className='mr-2 h-4 w-4' />
                )}
                {hasLink ? t('Regenerate') : t('Generate')}
              </Button>
              {hasLink ? (
                <>
                  <CopyButton
                    value={authUrl}
                    variant='outline'
                    size='icon'
                    aria-label={t('Copy authorization link')}
                  />
                  <Button
                    type='button'
                    variant='outline'
                    size='icon'
                    aria-label={t('Open authorization link')}
                    render={
                      <a href={authUrl} target='_blank' rel='noreferrer' />
                    }
                  >
                    <ExternalLink />
                  </Button>
                </>
              ) : null}
            </div>
          </div>
        </div>

        <div className='flex flex-col gap-2'>
          <Label htmlFor='oauth-authorization-code'>
            {t('Step 2: Paste the result')} · {t(copy.pasteLabelKey)}
          </Label>
          <Textarea
            id='oauth-authorization-code'
            rows={3}
            value={code}
            onChange={(event) => setCode(event.target.value)}
            placeholder={copy.pastePlaceholder}
            disabled={!hasLink || isExchanging}
            className='font-mono text-xs'
            spellCheck={false}
          />
          <p className='text-muted-foreground text-xs leading-5'>
            {t(copy.pasteHintKey)}
          </p>
        </div>

        {errorMessage ? (
          <Alert variant='destructive'>
            <AlertTriangle />
            <AlertTitle>{t('Authorization failed')}</AlertTitle>
            <AlertDescription>{errorMessage}</AlertDescription>
          </Alert>
        ) : null}
      </div>
    </Dialog>
  )
}
