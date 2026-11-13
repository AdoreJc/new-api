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
import { useTranslation } from 'react-i18next'

import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

import type { PolicyOption } from './lib/constants'

type PolicySelectProps = {
  value: string
  onChange: (value: string) => void
  options: PolicyOption[]
  ariaLabel: string
  className?: string
  size?: 'sm' | 'default'
}

/** A select over fixed policy options whose labels are i18n keys. */
export function PolicySelect(props: PolicySelectProps) {
  const { t } = useTranslation()
  return (
    <Select
      items={props.options.map((option) => ({
        value: option.value,
        label: t(option.label),
      }))}
      value={props.value}
      onValueChange={(value) => {
        if (typeof value === 'string') props.onChange(value)
      }}
    >
      <SelectTrigger
        aria-label={props.ariaLabel}
        size={props.size}
        className={cn('w-full', props.className)}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        <SelectGroup>
          {props.options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {t(option.label)}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}
