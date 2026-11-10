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
import { useState } from 'react'
import {
  useController,
  type Control,
  type FieldPath,
  type FieldValues,
} from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

import {
  CONTENT_LANGUAGES,
  parseLocalizedContent,
  serializeLocalizedContent,
} from '../utils/localized-content'

const DEFAULT_TAB = 'default'

type LocalizedContentFieldProps<TValues extends FieldValues> = {
  control: Control<TValues>
  /** Form path of the default (single-value) content. */
  name: FieldPath<TValues>
  /** Form path of the JSON string holding per-language overrides. */
  translationsName: FieldPath<TValues>
  label: string
  description?: string
  placeholder?: string
  rows?: number
}

/**
 * A site-content textarea with a tab per interface language. The Default tab
 * edits the existing option; each language tab edits one entry of the
 * site_content_i18n.* JSON map. A language left empty is omitted from the map
 * and shows the default content.
 */
export function LocalizedContentField<TValues extends FieldValues>(
  props: LocalizedContentFieldProps<TValues>
) {
  const { t } = useTranslation()
  const [activeTab, setActiveTab] = useState<string>(DEFAULT_TAB)
  const translationsController = useController({
    control: props.control,
    name: props.translationsName,
  })
  const translations = parseLocalizedContent(translationsController.field.value)

  const updateTranslation = (tag: string, text: string) => {
    translationsController.field.onChange(
      serializeLocalizedContent({ ...translations, [tag]: text })
    )
  }

  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <Tabs
            value={activeTab}
            onValueChange={(value) => setActiveTab(String(value))}
            className='gap-2'
          >
            <TabsList
              aria-label={t('Content language')}
              className='h-auto flex-wrap justify-start'
            >
              <TabsTrigger value={DEFAULT_TAB} className='flex-none'>
                {t('Default')}
              </TabsTrigger>
              {CONTENT_LANGUAGES.map((language) => (
                <TabsTrigger
                  key={language.tag}
                  value={language.tag}
                  className='flex-none'
                >
                  {language.label}
                  {translations[language.tag]?.trim() ? (
                    <>
                      <span
                        aria-hidden='true'
                        className='bg-primary size-1.5 rounded-full'
                      />
                      <span className='sr-only'>{t('Translated')}</span>
                    </>
                  ) : null}
                </TabsTrigger>
              ))}
            </TabsList>
            <TabsContent value={DEFAULT_TAB}>
              <FormControl>
                <Textarea
                  placeholder={props.placeholder}
                  rows={props.rows ?? 4}
                  {...field}
                />
              </FormControl>
            </TabsContent>
            {CONTENT_LANGUAGES.map((language) => (
              <TabsContent key={language.tag} value={language.tag}>
                <Textarea
                  aria-label={`${props.label} (${language.label})`}
                  placeholder={t('Leave empty to use the default content')}
                  rows={props.rows ?? 4}
                  value={translations[language.tag] ?? ''}
                  onChange={(event) =>
                    updateTranslation(language.tag, event.target.value)
                  }
                  onBlur={translationsController.field.onBlur}
                />
              </TabsContent>
            ))}
          </Tabs>
          {props.description ? (
            <FormDescription>{props.description}</FormDescription>
          ) : null}
          <FormDescription>
            {t(
              'Use the language tabs to enter a translation. Languages left empty show the default content.'
            )}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
