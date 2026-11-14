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
import { BarChart3, KeyRound, Network, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

import { FeatureItem } from '../feature-item'

interface FeaturesProps {
  className?: string
}

export function Features(_props: FeaturesProps) {
  const { t } = useTranslation()

  const features = [
    {
      title: t('Unified access'),
      desc: t(
        'Reach many AI models and providers through one standard API endpoint.'
      ),
      icon: <Network className='size-5 text-amber-500' />,
    },
    {
      title: t('Routing and load balancing'),
      desc: t(
        'Route requests across channels with groups, priorities, weights, and automatic retry.'
      ),
      icon: <Route className='size-5 text-amber-500' />,
    },
    {
      title: t('Usage monitoring and analytics'),
      desc: t(
        'Track requests, tokens, latency, and cost with logs and dashboards.'
      ),
      icon: <BarChart3 className='size-5 text-amber-500' />,
    },
    {
      title: t('Key and billing management'),
      desc: t(
        'Issue API keys with quotas and limits, and manage balances and billing.'
      ),
      icon: <KeyRound className='size-5 text-amber-500' />,
    },
  ]

  return (
    <section className='border-border/40 relative z-10 border-y px-6 py-20 md:py-24'>
      <div className='mx-auto max-w-6xl'>
        <AnimateInView className='mb-12 text-center'>
          <h2 className='text-2xl font-bold tracking-tight md:text-3xl'>
            {t('Manage, route, monitor, and analyze AI API access')}
          </h2>
        </AnimateInView>
        <div className='grid gap-6 sm:grid-cols-2 lg:grid-cols-4'>
          {features.map((feature, i) => (
            <AnimateInView key={feature.title} delay={i * 100}>
              <FeatureItem
                title={feature.title}
                description={feature.desc}
                icon={feature.icon}
              />
            </AnimateInView>
          ))}
        </div>
      </div>
    </section>
  )
}
