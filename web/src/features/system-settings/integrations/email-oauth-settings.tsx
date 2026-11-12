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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ExternalLink } from 'lucide-react'
import { useState } from 'react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { useStatus } from '@/hooks/use-status'
import { handleServerError } from '@/lib/handle-server-error'

import { OAuthSetupGuide } from '../components/oauth-setup-guide'
import {
  EMAIL_OAUTH_CALLBACK_PATH,
  revokeEmailOAuthAuthorization,
  startEmailOAuthAuthorization,
} from './email-oauth-api'
import type { EmailFormValues } from './email-settings-schema'

// Where each provider issues the Client ID and client secret.
const EMAIL_OAUTH_CONSOLE_URLS = {
  googleCredentials: 'https://console.cloud.google.com/apis/credentials',
  gmailApi:
    'https://console.cloud.google.com/apis/library/gmail.googleapis.com',
  microsoftApps:
    'https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade',
}

type EmailOAuthSettingsProps = {
  isSaving: boolean
  saved: Pick<
    EmailFormValues,
    | 'EmailOAuthProvider'
    | 'EmailOAuthClientId'
    | 'EmailOAuthTenant'
    | 'EmailOAuthSecretConfigured'
    | 'EmailOAuthEmail'
  >
}

export function EmailOAuthSettings(props: EmailOAuthSettingsProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useFormContext<EmailFormValues>()
  const { status } = useStatus()
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const [oauthProvider, clientId, tenant, secret] = useWatch({
    control: form.control,
    name: [
      'EmailOAuthProvider',
      'EmailOAuthClientId',
      'EmailOAuthTenant',
      'EmailOAuthClientSecret',
    ],
  })

  const authorizedEmail = props.saved.EmailOAuthEmail
  const authorized = authorizedEmail !== ''
  const serverAddress =
    typeof status?.server_address === 'string'
      ? status.server_address.trim().replace(/\/+$/, '')
      : ''
  const redirectURI = serverAddress
    ? `${serverAddress}${EMAIL_OAUTH_CALLBACK_PATH}`
    : ''
  const appUnsaved =
    oauthProvider !== props.saved.EmailOAuthProvider ||
    clientId.trim() !== props.saved.EmailOAuthClientId.trim() ||
    tenant.trim() !== props.saved.EmailOAuthTenant.trim() ||
    secret.trim() !== ''
  const appSaved =
    props.saved.EmailOAuthClientId.trim() !== '' &&
    props.saved.EmailOAuthSecretConfigured

  const authorize = useMutation({
    mutationFn: startEmailOAuthAuthorization,
    onSuccess: (response) => {
      const url = response.data?.authorization_url
      // The consent page belongs to the provider, outside this app.
      if (url) window.location.assign(url)
    },
    onError: (error) => handleServerError(error),
  })
  const revoke = useMutation({
    mutationFn: revokeEmailOAuthAuthorization,
    onSuccess: () => {
      toast.success(t('Mailbox authorization removed'))
      setConfirmRevoke(false)
    },
    onError: (error) => handleServerError(error),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: ['system-options'] }),
  })
  const busy = props.isSaving || authorize.isPending || revoke.isPending
  const consoleLinks =
    oauthProvider === 'microsoft'
      ? [
          {
            label: t('Get a Client ID and client secret'),
            href: EMAIL_OAUTH_CONSOLE_URLS.microsoftApps,
          },
        ]
      : [
          {
            label: t('Get a Client ID and client secret'),
            href: EMAIL_OAUTH_CONSOLE_URLS.googleCredentials,
          },
          {
            label: t('Enable the Gmail API'),
            href: EMAIL_OAUTH_CONSOLE_URLS.gmailApi,
          },
        ]

  let authorizeHint = t(
    'Opens the provider sign-in page. Sign in with the mailbox that sends email.'
  )
  if (!redirectURI) {
    authorizeHint = t(
      'Set the server address before authorizing the sender mailbox'
    )
  } else if (appUnsaved || !appSaved) {
    authorizeHint = t('Save the OAuth app settings before authorizing')
  }

  return (
    <>
      <FormField
        control={form.control}
        name='EmailOAuthProvider'
        render={({ field }) => (
          <FormItem data-settings-form-span='full'>
            <FormLabel>{t('Mail service')}</FormLabel>
            <FormControl>
              <RadioGroup
                value={field.value}
                onValueChange={field.onChange}
                disabled={busy || authorized}
                className='grid-cols-1 gap-3 sm:grid-cols-2'
              >
                <Label
                  className='flex cursor-pointer items-center gap-3 rounded-lg border p-4'
                  htmlFor='email-oauth-provider-google'
                >
                  <RadioGroupItem
                    value='google'
                    id='email-oauth-provider-google'
                  />
                  Gmail / Google Workspace
                </Label>
                <Label
                  className='flex cursor-pointer items-center gap-3 rounded-lg border p-4'
                  htmlFor='email-oauth-provider-microsoft'
                >
                  <RadioGroupItem
                    value='microsoft'
                    id='email-oauth-provider-microsoft'
                  />
                  Outlook / Microsoft 365
                </Label>
              </RadioGroup>
            </FormControl>
            {authorized && (
              <FormDescription>
                {t('Remove the mailbox authorization to change the OAuth app')}
              </FormDescription>
            )}
            <FormMessage />
          </FormItem>
        )}
      />
      <OAuthSetupGuide
        title={t('Setup guide')}
        description={
          oauthProvider === 'microsoft'
            ? t(
                'In Microsoft Entra, register an app, add a Web platform with the redirect URI below, create a client secret under Certificates & secrets, and add the delegated Microsoft Graph permissions Mail.Send, User.Read and offline_access. A single-tenant app (the default) needs its Directory (tenant) ID in Tenant below; use common only for multi-tenant or personal-account apps.'
              )
            : t(
                'In Google Cloud Console, enable the Gmail API, then create an OAuth client ID of type Web application with the redirect URI below. On the OAuth consent screen, choose Internal for Google Workspace; with External, add the sending mailbox as a test user or publish the app, because while in Testing other accounts are denied and refresh tokens expire after 7 days.'
              )
        }
      >
        <div className='flex flex-wrap gap-x-4 gap-y-1'>
          {consoleLinks.map((link) => (
            <a
              key={link.href}
              href={link.href}
              target='_blank'
              rel='noopener noreferrer'
              className='text-primary inline-flex w-fit items-center gap-1 underline underline-offset-3 hover:no-underline'
            >
              {link.label}
              <ExternalLink className='size-3' aria-hidden='true' />
            </a>
          ))}
        </div>
      </OAuthSetupGuide>
      <FormField
        control={form.control}
        name='EmailOAuthClientId'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Client ID')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                autoComplete='off'
                disabled={busy || authorized}
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name='EmailOAuthClientSecret'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Client secret')}</FormLabel>
            <FormControl>
              <PasswordInput
                {...field}
                autoComplete='new-password'
                disabled={busy}
                placeholder={t('Enter new secret to update')}
              />
            </FormControl>
            <FormDescription>
              {t('Leave blank to keep the existing secret')}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      {oauthProvider === 'microsoft' && (
        <FormField
          control={form.control}
          name='EmailOAuthTenant'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Tenant')}</FormLabel>
              <FormControl>
                <Input
                  {...field}
                  autoComplete='off'
                  disabled={busy || authorized}
                  placeholder='common'
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Directory (tenant) ID or domain. Leave blank to use common.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      )}
      <FormItem data-settings-form-span='full'>
        <FormLabel>{t('Redirect URI')}</FormLabel>
        {redirectURI ? (
          <div className='flex items-center gap-2'>
            <Input
              value={redirectURI}
              readOnly
              aria-label={t('Redirect URI')}
            />
            <CopyButton value={redirectURI} variant='outline' />
          </div>
        ) : (
          <p className='text-destructive text-sm'>
            {t('Set the server address before authorizing the sender mailbox')}
          </p>
        )}
        <FormDescription>
          {t('Register this address in the OAuth app of your mail service')}
        </FormDescription>
      </FormItem>
      <FormItem data-settings-form-span='full'>
        <FormLabel>{t('Authorized mailbox')}</FormLabel>
        <p className='text-sm' data-testid='email-oauth-status'>
          {authorized ? authorizedEmail : t('Not authorized')}
        </p>
        <div className='flex flex-wrap gap-2'>
          <Button
            type='button'
            variant='outline'
            disabled={busy || !redirectURI || appUnsaved || !appSaved}
            onClick={() => authorize.mutate()}
          >
            {authorized ? t('Re-authorize mailbox') : t('Authorize mailbox')}
          </Button>
          {authorized && (
            <Button
              type='button'
              variant='outline'
              disabled={busy}
              onClick={() => setConfirmRevoke(true)}
            >
              {t('Remove authorization')}
            </Button>
          )}
        </div>
        <FormDescription>{authorizeHint}</FormDescription>
      </FormItem>
      <ConfirmDialog
        open={confirmRevoke}
        onOpenChange={setConfirmRevoke}
        title={t('Remove mailbox authorization?')}
        desc={t(
          'The stored token is deleted. To revoke access completely, also remove this app in the mailbox account settings.'
        )}
        destructive
        isLoading={revoke.isPending}
        handleConfirm={() => revoke.mutate()}
      />
    </>
  )
}
