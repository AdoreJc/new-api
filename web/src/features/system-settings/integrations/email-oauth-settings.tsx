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
  sendEmailOAuthTestMessage,
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
    | 'EmailOAuthFrom'
    | 'EmailOAuthSharedSendGranted'
  >
}

export function EmailOAuthSettings(props: EmailOAuthSettingsProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useFormContext<EmailFormValues>()
  const { status } = useStatus()
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const [oauthProvider, clientId, tenant, secret, sender] = useWatch({
    control: form.control,
    name: [
      'EmailOAuthProvider',
      'EmailOAuthClientId',
      'EmailOAuthTenant',
      'EmailOAuthClientSecret',
      'EmailOAuthFrom',
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
    secret.trim() !== '' ||
    // The saved sender address decides whether the shared-mailbox
    // permission is requested, and the test email uses it.
    sender.trim() !== props.saved.EmailOAuthFrom.trim()
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
  const sendTest = useMutation({
    mutationFn: sendEmailOAuthTestMessage,
    onSuccess: (response) =>
      toast.success(
        t('Test email sent to {{email}}', { email: response.data?.to ?? '' })
      ),
    onError: (error) => handleServerError(error),
  })
  const busy =
    props.isSaving ||
    authorize.isPending ||
    revoke.isPending ||
    sendTest.isPending
  const microsoft = oauthProvider === 'microsoft'
  const savedSender = props.saved.EmailOAuthFrom.trim()
  // Sending from a shared Microsoft mailbox needs a grant that includes
  // Mail.Send.Shared; the server refuses instead of using the mailbox.
  const sharedNeedsReauthorization =
    microsoft &&
    authorized &&
    savedSender !== '' &&
    savedSender.toLowerCase() !== authorizedEmail.toLowerCase() &&
    !props.saved.EmailOAuthSharedSendGranted
  const consoleLinks = microsoft
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
          microsoft ? (
            <>
              <p>
                {t(
                  'In Microsoft Entra, register an app, add a Web platform with the redirect URI below, create a client secret under Certificates & secrets, and add the delegated Microsoft Graph permissions Mail.Send, User.Read and offline_access. Copy the IDs below from the app Overview page and the secret Value (not the Secret ID).'
                )}
              </p>
              <p>
                {t(
                  'To send from a shared mailbox, give the authorized account Send As permission on it in the Exchange admin center (Mailboxes > the shared mailbox > Delegation), enter it as the sender address below, save, and re-authorize.'
                )}
              </p>
            </>
          ) : (
            <>
              <p>
                {t(
                  'In Google Cloud Console, enable the Gmail API, then create an OAuth client ID of type Web application with the redirect URI below. On the OAuth consent screen, choose Internal for Google Workspace; with External, add the sending mailbox as a test user or publish the app, because while in Testing other accounts are denied and refresh tokens expire after 7 days.'
                )}
              </p>
              <p>
                {t(
                  'To send from another address, add and verify it in Gmail under Settings > Accounts > Send mail as, then enter it as the sender address below.'
                )}
              </p>
            </>
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
            {/* Microsoft fields use the Entra portal's own names so they can
                be matched and copied from the app's Overview page. */}
            <FormLabel>
              {microsoft ? t('Application (client) ID') : t('Client ID')}
            </FormLabel>
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
            <FormLabel>
              {microsoft ? t('Client secret (Value)') : t('Client secret')}
            </FormLabel>
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
      {microsoft && (
        <FormField
          control={form.control}
          name='EmailOAuthTenant'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Directory (tenant) ID')}</FormLabel>
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
                  'Copy it from the Overview page of the Entra app. Required for single-tenant apps (the default); multi-tenant apps can use organizations or leave it blank (common); use consumers for personal Microsoft accounts only.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      )}
      <FormField
        control={form.control}
        name='EmailOAuthFrom'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Sender email address')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                type='email'
                autoComplete='off'
                disabled={busy}
                placeholder={authorizedEmail || 'name@example.com'}
              />
            </FormControl>
            <FormDescription>
              {microsoft
                ? t(
                    'Optional. Leave blank to send from the authorized mailbox. A shared mailbox needs Send As or Send on Behalf permission for the authorized account and works only with work or school accounts.'
                  )
                : t(
                    'Optional. Leave blank to send from the authorized mailbox. Must be a verified "Send mail as" address of this Gmail account, or the message may bounce.'
                  )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
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
              disabled={busy || appUnsaved}
              onClick={() => sendTest.mutate()}
            >
              {t('Send test email')}
            </Button>
          )}
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
        {sharedNeedsReauthorization && (
          <p className='text-destructive text-sm' role='alert'>
            {t('Re-authorize the mailbox to send from the shared mailbox')}
          </p>
        )}
        <FormDescription>{authorizeHint}</FormDescription>
        {authorized && (
          <FormDescription>
            {t(
              'The test email is sent from the sender address to the authorized mailbox.'
            )}
          </FormDescription>
        )}
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
