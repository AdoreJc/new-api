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
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Form } from '@/components/ui/form'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { LocalizedContentField } from '../general/localized-content-field'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  normalizeLocalizedContentOption,
  toLocalizedContentOptionValue,
} from '../utils/localized-content'

const noticeSchema = z.object({
  Notice: z.string().optional(),
  site_content_i18n: z.object({ notice: z.string() }),
})

type NoticeFormValues = z.infer<typeof noticeSchema>

type NoticeSectionProps = {
  defaultValue: string
  /** Stored site_content_i18n.notice JSON (per-language notices). */
  defaultTranslations: string
}

export function NoticeSection({
  defaultValue,
  defaultTranslations,
}: NoticeSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const initialTranslations =
    normalizeLocalizedContentOption(defaultTranslations)
  const form = useForm<NoticeFormValues>({
    resolver: zodResolver(noticeSchema),
    defaultValues: {
      Notice: defaultValue ?? '',
      site_content_i18n: { notice: initialTranslations },
    },
  })

  useEffect(() => {
    form.reset({
      Notice: defaultValue ?? '',
      site_content_i18n: { notice: initialTranslations },
    })
  }, [defaultValue, initialTranslations, form])

  const onSubmit = async (values: NoticeFormValues) => {
    const normalized = values.Notice ?? ''
    if (normalized !== (defaultValue ?? '')) {
      await updateOption.mutateAsync({
        key: 'Notice',
        value: normalized,
      })
    }
    const translations = toLocalizedContentOptionValue(
      values.site_content_i18n.notice
    )
    if (translations !== toLocalizedContentOptionValue(initialTranslations)) {
      await updateOption.mutateAsync({
        key: 'site_content_i18n.notice',
        value: translations,
      })
    }
  }

  return (
    <SettingsSection title={t('System Notice')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save notice'
          />
          <LocalizedContentField
            control={form.control}
            name='Notice'
            translationsName='site_content_i18n.notice'
            label={t('Announcement content')}
            placeholder={t('Planned maintenance on Friday at 22:00 UTC...')}
            rows={8}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
