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
import { zodResolver } from '@hookform/resolvers/zod'
import type { Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import {
  SettingsForm,
  SettingsFormGrid,
  SettingsFormGridItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  normalizeLocalizedContentOption,
  toLocalizedContentOptionValue,
} from '../utils/localized-content'
import { LocalizedContentField } from './localized-content-field'
import { isValidTaskPublicAddress } from './task-public-address'

const _systemInfoSchema = z.object({
  SystemName: z.string().min(1),
  ServerAddress: z.string().optional(),
  TaskPublicAddress: z.string().refine(isValidTaskPublicAddress),
  Logo: z.string().url().optional().or(z.literal('')),
  Footer: z.string().optional(),
  About: z.string().optional(),
  HomePageContent: z.string().optional(),
  general_setting: z.object({
    docs_link: z.string(),
  }),
  legal: z.object({
    user_agreement: z.string().optional(),
    privacy_policy: z.string().optional(),
  }),
  site_content_i18n: z.object({
    footer: z.string(),
    about: z.string(),
    home_page_content: z.string(),
    user_agreement: z.string(),
    privacy_policy: z.string(),
  }),
})

type SystemInfoFormValues = z.infer<typeof _systemInfoSchema>

type SystemInfoSectionProps = {
  defaultValues: SystemInfoFormValues
}

function normalizeValue(value: unknown): string {
  if (value === undefined || value === null) return ''
  return typeof value === 'string' ? value : String(value)
}

export function SystemInfoSection({ defaultValues }: SystemInfoSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const normalizedDefaults: SystemInfoFormValues = {
    SystemName: normalizeValue(defaultValues.SystemName),
    ServerAddress: normalizeValue(defaultValues.ServerAddress),
    TaskPublicAddress: normalizeValue(defaultValues.TaskPublicAddress),
    Logo: normalizeValue(defaultValues.Logo),
    Footer: normalizeValue(defaultValues.Footer),
    About: normalizeValue(defaultValues.About),
    HomePageContent: normalizeValue(defaultValues.HomePageContent),
    general_setting: {
      docs_link: normalizeValue(defaultValues.general_setting?.docs_link),
    },
    legal: {
      user_agreement: normalizeValue(defaultValues.legal?.user_agreement),
      privacy_policy: normalizeValue(defaultValues.legal?.privacy_policy),
    },
    site_content_i18n: {
      footer: normalizeLocalizedContentOption(
        defaultValues.site_content_i18n?.footer
      ),
      about: normalizeLocalizedContentOption(
        defaultValues.site_content_i18n?.about
      ),
      home_page_content: normalizeLocalizedContentOption(
        defaultValues.site_content_i18n?.home_page_content
      ),
      user_agreement: normalizeLocalizedContentOption(
        defaultValues.site_content_i18n?.user_agreement
      ),
      privacy_policy: normalizeLocalizedContentOption(
        defaultValues.site_content_i18n?.privacy_policy
      ),
    },
  }

  const systemInfoSchemaWithI18n = z.object({
    SystemName: z.string().min(1, {
      error: () => t('System name is required'),
    }),
    ServerAddress: z.string().optional(),
    TaskPublicAddress: z.string().refine(isValidTaskPublicAddress, {
      error: () =>
        t(
          'Enter an absolute HTTP(S) URL without credentials, query parameters, or fragments'
        ),
    }),
    Logo: z.string().url().optional().or(z.literal('')),
    Footer: z.string().optional(),
    About: z.string().optional(),
    HomePageContent: z.string().optional(),
    general_setting: z.object({
      docs_link: z.string(),
    }),
    legal: z.object({
      user_agreement: z.string().optional(),
      privacy_policy: z.string().optional(),
    }),
    site_content_i18n: z.object({
      footer: z.string(),
      about: z.string(),
      home_page_content: z.string(),
      user_agreement: z.string(),
      privacy_policy: z.string(),
    }),
  })

  const { form, handleSubmit, handleReset, isDirty, isSubmitting } =
    useSettingsForm<SystemInfoFormValues>({
      resolver: zodResolver(systemInfoSchemaWithI18n) as Resolver<
        SystemInfoFormValues,
        unknown,
        SystemInfoFormValues
      >,
      defaultValues: normalizedDefaults,
      onSubmit: async (_data, changedFields) => {
        for (const [key, value] of Object.entries(changedFields)) {
          let v = normalizeValue(value)
          if (key === 'ServerAddress' || key === 'TaskPublicAddress') {
            v = v.replace(/\/+$/, '')
          }
          if (key.startsWith('site_content_i18n.')) {
            v = toLocalizedContentOptionValue(v)
          }
          await updateOption.mutateAsync({
            key,
            value: v,
          })
        }
      },
    })

  return (
    <>
      <FormNavigationGuard when={isDirty} />

      <SettingsSection title={t('System Information')}>
        <Form {...form}>
          <SettingsForm onSubmit={handleSubmit}>
            <SettingsPageFormActions
              onSave={handleSubmit}
              onReset={handleReset}
              isSaving={isSubmitting || updateOption.isPending}
              isResetDisabled={!isDirty}
            />
            <FormDirtyIndicator isDirty={isDirty} />
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='SystemName'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('System Name')}</FormLabel>
                    <FormControl>
                      <Input placeholder={t('New API')} {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('The name displayed across the application')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='ServerAddress'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Server Address')}</FormLabel>
                    <FormControl>
                      <Input placeholder='https://yourdomain.com' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'The public URL of your server, used for OAuth callbacks, webhooks, and other external integrations'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='TaskPublicAddress'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Async Task Public Address')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder='https://media.example.com/tasks'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Public base URL for async task media. Supports a dedicated media domain, port, or Nginx path prefix; falls back to Server Address when empty.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='Logo'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Logo URL')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('https://example.com/logo.png')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('URL to your logo image (optional)')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='general_setting.docs_link'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Documentation Link')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('https://docs.example.com')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Link to your documentation site')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <LocalizedContentField
                control={form.control}
                name='Footer'
                translationsName='site_content_i18n.footer'
                label={t('Footer')}
                placeholder={t('© 2025 Your Company. All rights reserved.')}
                description={t('Footer text displayed at the bottom of pages')}
              />

              <LocalizedContentField
                control={form.control}
                name='About'
                translationsName='site_content_i18n.about'
                label={t('About')}
                placeholder={t(
                  'Enter HTML code (e.g., <p>About us...</p>) or a URL (e.g., https://example.com) to embed as iframe'
                )}
                description={t(
                  'Supports HTML markup or iframe embedding. Enter HTML code directly, or provide a complete URL to automatically embed it as an iframe.'
                )}
              />

              <SettingsFormGridItem span='full'>
                <LocalizedContentField
                  control={form.control}
                  name='HomePageContent'
                  translationsName='site_content_i18n.home_page_content'
                  label={t('Home Page Content')}
                  placeholder={t('Welcome to our New API...')}
                  rows={6}
                  description={t(
                    'Content displayed on the home page (supports Markdown)'
                  )}
                />
              </SettingsFormGridItem>

              <LocalizedContentField
                control={form.control}
                name='legal.user_agreement'
                translationsName='site_content_i18n.user_agreement'
                label={t('User Agreement')}
                placeholder={t(
                  'Provide Markdown, HTML, or an external URL for the user agreement'
                )}
                rows={6}
                description={t(
                  'Leave empty to disable the agreement requirement. Supports Markdown, HTML, or a full URL to redirect users.'
                )}
              />

              <LocalizedContentField
                control={form.control}
                name='legal.privacy_policy'
                translationsName='site_content_i18n.privacy_policy'
                label={t('Privacy Policy')}
                placeholder={t(
                  'Provide Markdown, HTML, or an external URL for the privacy policy'
                )}
                rows={6}
                description={t(
                  'Leave empty to disable the privacy policy requirement. Supports Markdown, HTML, or a full URL to redirect users.'
                )}
              />
            </SettingsFormGrid>
          </SettingsForm>
        </Form>
      </SettingsSection>
    </>
  )
}
