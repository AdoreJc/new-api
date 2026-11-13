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
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorStatus } from '@/lib/server-error-message'

import { SettingsPageFormActions } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import {
  AUDIT_POLICY_QUERY_KEY,
  getAuditPolicy,
  resetAuditPolicy,
  updateAuditPolicy,
  type AuditPolicyConfig,
  type AuditPolicyResponse,
  type AuditPolicyRule,
} from './api'
import {
  CONTENT_MODERATION_MODE_LABELS,
  PROMPT_AUDIT_MODE_LABELS,
} from './lib/constants'
import {
  charCount,
  createRule,
  duplicateRule,
  editorKey,
  MAX_NOTE_CHARS,
} from './lib/rule-form'
import { ListEntriesEditor } from './list-entries-editor'
import { RuleEditorDialog } from './rule-editor-dialog'
import { RuleList } from './rule-list'
import { AuditPolicyTestPanel } from './test-panel'

export function AuditPolicySection() {
  const { t } = useTranslation()
  const policy = useQuery({
    queryKey: AUDIT_POLICY_QUERY_KEY,
    queryFn: getAuditPolicy,
  })
  if (policy.isPending) return <LoadingState />
  if (policy.isError || !policy.data) {
    return (
      <ErrorState
        title={t('Failed to load the audit policy')}
        onRetry={() => void policy.refetch()}
      />
    )
  }
  // Remounting on a new version drops the draft and starts from the server.
  // Derived rules keep their version while the engine settings change, so
  // their content is part of the key.
  return (
    <AuditPolicyEditor
      key={editorKey(policy.data)}
      data={policy.data}
      onReload={() => void policy.refetch()}
    />
  )
}

/** index -1 is the default rule; isNew appends the rule on apply. */
type Editing = { index: number; rule: AuditPolicyRule; isNew: boolean }

function AuditPolicyEditor(props: {
  data: AuditPolicyResponse
  onReload: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const initial = props.data.config
  const [rules, setRules] = useState<AuditPolicyRule[]>(initial.rules ?? [])
  const [defaultRule, setDefaultRule] = useState(initial.default)
  const [allowlist, setAllowlist] = useState(initial.allowlist ?? [])
  const [blocklist, setBlocklist] = useState(initial.blocklist ?? [])
  const [editing, setEditing] = useState<Editing | null>(null)
  const [conflict, setConflict] = useState(false)
  const [confirmReset, setConfirmReset] = useState(false)
  const [confirmFirstSave, setConfirmFirstSave] = useState(false)

  const draft: AuditPolicyConfig = {
    ...initial,
    rules,
    default: defaultRule,
    allowlist,
    blocklist,
  }
  const dirty =
    JSON.stringify(draft) !==
    JSON.stringify({
      ...initial,
      rules: initial.rules ?? [],
      allowlist: initial.allowlist ?? [],
      blocklist: initial.blocklist ?? [],
    })

  const onSaveError = (error: unknown) => {
    if (getServerErrorStatus(error) === 409) setConflict(true)
    handleServerError(error)
  }
  const save = useMutation({
    mutationFn: () => updateAuditPolicy(draft, initial.config_version),
    onSuccess: (data) => {
      queryClient.setQueryData(AUDIT_POLICY_QUERY_KEY, data)
      toast.success(t('Audit policy saved'))
    },
    onError: onSaveError,
  })
  const reset = useMutation({
    mutationFn: () => resetAuditPolicy(initial.config_version),
    onSuccess: (data) => {
      queryClient.setQueryData(AUDIT_POLICY_QUERY_KEY, data)
      setConfirmReset(false)
      toast.success(t('The audit policy now follows the engine settings'))
    },
    onError: (error) => {
      setConfirmReset(false)
      onSaveError(error)
    },
  })
  const onSave = () => {
    const invalidEntry = [...allowlist, ...blocklist].some(
      (entry) => entry.user_id <= 0
    )
    if (invalidEntry) {
      toast.error(t('Audit policy list entries need a positive user ID'))
      return
    }
    if (defaultRule.degraded || rules.some((rule) => rule.degraded)) {
      toast.error(
        t(
          'Edit or delete the rules that could not be read before saving the audit policy'
        )
      )
      return
    }
    const longNote = [...allowlist, ...blocklist].some(
      (entry) => charCount(entry.note) > MAX_NOTE_CHARS
    )
    if (longNote) {
      toast.error(t('Audit policy list notes are at most 255 characters'))
      return
    }
    if (!props.data.saved) {
      setConfirmFirstSave(true)
      return
    }
    save.mutate()
  }

  const ruleIds = rules.map((rule) => rule.id)
  const applyRule = (rule: AuditPolicyRule) => {
    if (editing === null) return
    if (editing.index === -1) {
      setDefaultRule(rule)
    } else if (editing.isNew) {
      setRules([...rules, rule])
    } else {
      setRules(
        rules.map((current, position) =>
          position === editing.index ? rule : current
        )
      )
    }
    setEditing(null)
  }
  const otherIds =
    editing === null
      ? []
      : ruleIds.filter((_, position) => position !== editing.index)

  return (
    <SettingsSection title={t('Audit policy')}>
      <SettingsPageFormActions
        onSave={onSave}
        isSaving={save.isPending}
        isSaveDisabled={props.data.saved && !dirty && !props.data.degraded}
        saveLabel='Save audit policy'
        onReset={() => setConfirmReset(true)}
        isResetDisabled={!props.data.saved || reset.isPending}
        resetLabel='Reset to derived rules'
      />
      {props.data.degraded && (
        <Alert variant='destructive'>
          <AlertDescription>
            <p>
              {t(
                'The saved audit policy could not be read in full. Rules marked unreadable run no checks; where they asked to block, their failure policy decides. Edit or delete each of them, then save to repair the policy.'
              )}
            </p>
            <ul className='mt-2 list-disc ps-5'>
              {(props.data.problems ?? []).map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      {!props.data.saved && (
        <Alert>
          <AlertDescription>
            {t(
              'These rules are derived from the prompt audit and content moderation settings. Saving stores them as the audit policy; the scope settings on those pages then no longer apply.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {conflict && (
        <Alert variant='destructive'>
          <AlertDescription className='flex flex-wrap items-center justify-between gap-2'>
            <span>
              {t(
                'The audit policy was changed by someone else. Reload and try again'
              )}
            </span>
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={props.onReload}
            >
              {t('Reload')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      <EngineStatus data={props.data} />
      <div className='flex flex-col gap-2'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <div className='flex items-center gap-2'>
            <h4 className='text-sm font-medium'>{t('Rules')}</h4>
            {dirty && (
              <StatusBadge
                label={t('Unsaved changes')}
                variant='warning'
                copyable={false}
              />
            )}
          </div>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() =>
              setEditing({
                index: rules.length,
                rule: createRule(ruleIds),
                isNew: true,
              })
            }
          >
            <Plus data-icon='inline-start' />
            {t('Add rule')}
          </Button>
        </div>
        <RuleList
          rules={rules}
          defaultRule={defaultRule}
          onRulesChange={setRules}
          onEdit={(index) =>
            setEditing({
              index,
              rule: index === -1 ? defaultRule : rules[index],
              isNew: false,
            })
          }
          onDuplicate={(index) => {
            const copy = duplicateRule(rules[index], ruleIds)
            setRules([
              ...rules.slice(0, index + 1),
              copy,
              ...rules.slice(index + 1),
            ])
          }}
        />
      </div>
      <ListEntriesEditor
        title={t('Allowlist')}
        description={t(
          'Rules can match allowlisted users. Only verified entries can be rerouted.'
        )}
        entries={allowlist}
        onChange={setAllowlist}
        showVerified
      />
      <ListEntriesEditor
        title={t('Blocklist')}
        description={t('Rules can match blocklisted users.')}
        entries={blocklist}
        onChange={setBlocklist}
        showVerified={false}
      />
      <p className='text-muted-foreground text-xs'>
        {t(
          'Changes apply when you save the audit policy. Every save is recorded in the audit log.'
        )}
      </p>
      <AuditPolicyTestPanel
        relayFormats={props.data.relay_formats ?? []}
        draft={draft}
      />
      {editing !== null && (
        <RuleEditorDialog
          rule={editing.rule}
          isDefault={editing.index === -1}
          otherIds={otherIds}
          promptAuditCategories={props.data.prompt_audit_categories ?? []}
          moderationCategories={props.data.content_moderation_categories ?? []}
          relayFormats={props.data.relay_formats ?? []}
          onSave={applyRule}
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
        />
      )}
      <ConfirmDialog
        open={confirmFirstSave}
        onOpenChange={setConfirmFirstSave}
        title={t('Save the audit policy')}
        desc={t(
          'The rules are saved as they are now. After saving they no longer follow changes to the prompt audit and content moderation scope settings; change them here instead.'
        )}
        confirmText={t('Save audit policy')}
        isLoading={save.isPending}
        handleConfirm={() => {
          setConfirmFirstSave(false)
          save.mutate()
        }}
      />
      <ConfirmDialog
        open={confirmReset}
        onOpenChange={setConfirmReset}
        title={t('Reset to derived rules')}
        desc={t(
          'The saved audit policy is removed and the rules are derived from the prompt audit and content moderation settings again. This cannot be undone.'
        )}
        destructive
        confirmText={t('Reset')}
        isLoading={reset.isPending}
        handleConfirm={() => reset.mutate()}
      />
    </SettingsSection>
  )
}

function EngineStatus(props: { data: AuditPolicyResponse }) {
  const { t } = useTranslation()
  const promptAuditMode =
    PROMPT_AUDIT_MODE_LABELS[props.data.prompt_audit_mode] ??
    props.data.prompt_audit_mode
  const moderationMode =
    CONTENT_MODERATION_MODE_LABELS[props.data.content_moderation_mode] ??
    props.data.content_moderation_mode
  return (
    <div className='flex flex-col gap-2 rounded-lg border p-4'>
      <h4 className='text-sm font-medium'>{t('Engine switches')}</h4>
      <dl className='grid gap-x-4 gap-y-2 text-sm sm:grid-cols-2'>
        <div className='flex min-w-0 flex-col'>
          <dt className='text-muted-foreground text-xs'>{t('Prompt audit')}</dt>
          <dd className='flex flex-wrap items-center gap-1.5'>
            <StatusBadge
              label={
                props.data.prompt_audit_enabled ? t('Enabled') : t('Disabled')
              }
              variant={props.data.prompt_audit_enabled ? 'success' : 'neutral'}
              copyable={false}
            />
            <span>{t(promptAuditMode)}</span>
          </dd>
        </div>
        <div className='flex min-w-0 flex-col'>
          <dt className='text-muted-foreground text-xs'>
            {t('Content moderation')}
          </dt>
          <dd>{t(moderationMode)}</dd>
        </div>
      </dl>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Rules only block or reroute prompts while the prompt audit mode is Allow blocking (synchronous), and inputs while the content moderation mode is Block. Otherwise their findings are only recorded.'
        )}
      </p>
    </div>
  )
}
