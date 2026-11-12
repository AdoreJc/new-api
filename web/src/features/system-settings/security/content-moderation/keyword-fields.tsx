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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import {
  deleteContentModerationKeywordList,
  getContentModerationKeywordList,
  getContentModerationKeywordLists,
  KEYWORD_LISTS_QUERY_KEY,
  saveContentModerationKeywordList,
} from './api'
import type {
  ContentModerationFormInput,
  ContentModerationFormValues,
} from './lib/form'

type ContentModerationForm = UseFormReturn<
  ContentModerationFormInput,
  unknown,
  ContentModerationFormValues
>

const KEYWORD_MODES = [
  { value: 'api_only', label: 'Moderation API only' },
  { value: 'keyword_only', label: 'Keywords only (no API call)' },
  {
    value: 'keyword_and_api',
    label: 'Keywords flag at once; other inputs go to the API',
  },
  {
    value: 'keyword_gated_api',
    label: 'Only inputs that match a list are reviewed by the API',
  },
] as const

const LIST_NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/

type Editing = { name: string; isNew: boolean }

export function ContentModerationKeywordFields(props: {
  form: ContentModerationForm
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const lists = useQuery({
    queryKey: KEYWORD_LISTS_QUERY_KEY,
    queryFn: getContentModerationKeywordLists,
  })
  const [editing, setEditing] = useState<Editing | null>(null)
  const [words, setWords] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: KEYWORD_LISTS_QUERY_KEY })

  const open = useMutation({
    mutationFn: (name: string) => getContentModerationKeywordList(name),
    onSuccess: (data) => {
      setWords(data.words)
      setEditing({ name: data.name, isNew: false })
    },
    onError: (error) => handleServerError(error),
  })
  const save = useMutation({
    mutationFn: (editingList: Editing) =>
      saveContentModerationKeywordList(editingList.name.trim(), words),
    onSuccess: (data) => {
      toast.success(
        t('Saved keyword list {{name}} with {{count}} keywords', {
          name: data.name,
          count: data.count,
        })
      )
      setEditing(null)
      refresh()
    },
    onError: (error) => handleServerError(error),
  })
  const remove = useMutation({
    mutationFn: (name: string) => deleteContentModerationKeywordList(name),
    onSuccess: () => {
      setDeleting(null)
      refresh()
    },
    onError: (error) => handleServerError(error),
  })
  const nameValid = editing !== null && LIST_NAME_PATTERN.test(editing.name)

  return (
    <div className='flex flex-col gap-4 rounded-lg border p-4'>
      <div className='min-w-0'>
        <h4 className='text-sm font-medium'>{t('Keyword lists')}</h4>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Matching ignores case. The default list is the sensitive words list from the content settings; its existing checks are unchanged.'
          )}
        </p>
      </div>
      <FormField
        control={props.form.control}
        name='keyword_mode'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Keyword mode')}</FormLabel>
            <Select
              items={KEYWORD_MODES.map((mode) => ({
                value: mode.value,
                label: t(mode.label),
              }))}
              onValueChange={(value) => {
                if (value !== null) field.onChange(value)
              }}
              value={field.value}
            >
              <FormControl>
                <SelectTrigger aria-label={t('Keyword mode')}>
                  <SelectValue />
                </SelectTrigger>
              </FormControl>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {KEYWORD_MODES.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {t(mode.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <FormDescription>
              {t(
                'Keywords only: a match is flagged (blocked in block mode, recorded in observe mode) and nothing else is checked. Keywords then the API: a match is flagged the same way without an API call, and every other input goes to the API as usual. Gated: only inputs that match a list are reviewed by the API, which decides; all other inputs are not checked.'
              )}
            </FormDescription>
          </FormItem>
        )}
      />
      <FormField
        control={props.form.control}
        name='keyword_lists'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Lists in use')}</FormLabel>
            <div className='flex flex-col gap-2'>
              {(lists.data ?? []).map((list) => (
                <div
                  key={list.name}
                  className='flex flex-wrap items-center gap-2 text-sm'
                >
                  <label className='flex items-center gap-2'>
                    <Checkbox
                      checked={(field.value ?? []).includes(list.name)}
                      onCheckedChange={(next) =>
                        field.onChange(
                          next
                            ? [...(field.value ?? []), list.name]
                            : (field.value ?? []).filter(
                                (value) => value !== list.name
                              )
                        )
                      }
                    />
                    <span className='font-mono'>{list.name}</span>
                  </label>
                  <span className='text-muted-foreground text-xs'>
                    {t('{{count}} keywords', { count: list.count })}
                  </span>
                  {!list.builtin && (
                    <>
                      <Button
                        type='button'
                        size='sm'
                        variant='ghost'
                        className='h-7 px-2'
                        disabled={open.isPending}
                        onClick={() => open.mutate(list.name)}
                      >
                        {t('Edit')}
                      </Button>
                      <Button
                        type='button'
                        size='sm'
                        variant='ghost'
                        className='text-destructive h-7 px-2'
                        onClick={() => setDeleting(list.name)}
                      >
                        {t('Delete')}
                      </Button>
                    </>
                  )}
                </div>
              ))}
            </div>
            <FormMessage />
          </FormItem>
        )}
      />
      <div>
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() => {
            setWords('')
            setEditing({ name: '', isNew: true })
          }}
        >
          {t('New keyword list')}
        </Button>
      </div>
      <Dialog
        open={editing !== null}
        onOpenChange={(next) => {
          if (!next) setEditing(null)
        }}
        title={editing?.isNew ? t('New keyword list') : t('Edit keyword list')}
        description={t(
          'One keyword per line, up to 10000 keywords of up to 200 characters.'
        )}
        bodyClassName='space-y-3'
        footer={
          <Button
            type='button'
            disabled={!nameValid || save.isPending}
            onClick={() => {
              if (editing) save.mutate(editing)
            }}
          >
            {t('Save')}
          </Button>
        }
      >
        <Input
          aria-label={t('List name')}
          placeholder={t('List name')}
          value={editing?.name ?? ''}
          disabled={!editing?.isNew}
          onChange={(event) =>
            setEditing((previous) =>
              previous ? { ...previous, name: event.target.value } : previous
            )
          }
        />
        <Textarea
          aria-label={t('Keywords')}
          value={words}
          rows={10}
          className='font-mono'
          onChange={(event) => setWords(event.target.value)}
        />
      </Dialog>
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(next) => {
          if (!next) setDeleting(null)
        }}
        title={t('Delete keyword list')}
        desc={t(
          'Delete keyword list {{name}}? Settings that use it stop matching it. This cannot be undone.',
          { name: deleting ?? '' }
        )}
        destructive
        confirmText={t('Delete')}
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
      />
    </div>
  )
}
