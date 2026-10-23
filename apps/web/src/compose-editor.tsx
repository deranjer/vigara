import { useEffect, useRef, useState } from 'react'
import { Button, Group, Stack, Text, useComputedColorScheme } from '@mantine/core'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/editor/editor.all'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import YamlWorker from 'monaco-yaml/yaml.worker?worker'
import { configureMonacoYaml } from 'monaco-yaml'
import composeSchema from './compose-spec.json'
import { indentation } from './compose-editor-utils'

self.MonacoEnvironment = { getWorker: (_id, label) => label === 'yaml' ? new YamlWorker() : new EditorWorker() }
const yaml = configureMonacoYaml(monaco, { enableSchemaRequest: false })

type Props = {
  stackId: string; path: string; value: string; original: string; compare: boolean
  composeFile: boolean; disabled: boolean; onChange: (value: string) => void; onSave: () => void
}

export default function ComposeEditor({ stackId, path, value, original, compare, composeFile, disabled, onChange, onSave }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const models = useRef<{ draft: monaco.editor.ITextModel; saved: monaco.editor.ITextModel } | null>(null)
  const instance = useRef<monaco.editor.IStandaloneCodeEditor | monaco.editor.IStandaloneDiffEditor | null>(null)
  const callbacks = useRef({ onChange, onSave })
  callbacks.current = { onChange, onSave }
  const [expanded, setExpanded] = useState(false)
  const [tabMovesFocus, setTabMovesFocus] = useState(false)
  const theme = useComputedColorScheme('light')

  useEffect(() => {
    const language = /\.ya?ml$/i.test(path) ? 'yaml' : 'plaintext'
    const uri = (side: string) => monaco.Uri.from({ scheme: 'inmemory', path: '/stacks/' + stackId + '/' + side + '/' + path })
    const draft = monaco.editor.createModel('', language, uri('draft'))
    const saved = monaco.editor.createModel('', language, uri('saved'))
    draft.updateOptions(indentation); saved.updateOptions(indentation)
    models.current = { draft, saved }
    const subscription = draft.onDidChangeContent(() => callbacks.current.onChange(draft.getValue()))
    return () => { subscription.dispose(); draft.dispose(); saved.dispose(); models.current = null }
  }, [path, stackId])

  useEffect(() => {
    if (!models.current) return
    void yaml.update({ enableSchemaRequest: false, indentation: '  ', format: { enable: true }, schemas: composeFile ? [{
      uri: 'https://compose-spec.io/schema/compose-spec.json', schema: composeSchema,
      fileMatch: [models.current.draft.uri.toString(), models.current.saved.uri.toString()],
    }] : [] })
  }, [composeFile, path, stackId])

  useEffect(() => {
    if (!host.current || !models.current) return
    const options: monaco.editor.IStandaloneEditorConstructionOptions = {
      ...indentation, automaticLayout: true, minimap: { enabled: false }, fontSize: 14,
      scrollBeyondLastLine: false, wordWrap: 'on', ariaLabel: 'Your draft: ' + path,
      tabFocusMode: false, padding: { top: 12, bottom: 12 },
    }
    const editor = compare
      ? monaco.editor.createDiffEditor(host.current, { ...options, originalEditable: false, renderSideBySide: true, originalAriaLabel: 'Saved version: ' + path, modifiedAriaLabel: 'Your draft: ' + path })
      : monaco.editor.create(host.current, { ...options, model: models.current.draft })
    if (compare) (editor as monaco.editor.IStandaloneDiffEditor).setModel({ original: models.current.saved, modified: models.current.draft })
    const editable = compare ? (editor as monaco.editor.IStandaloneDiffEditor).getModifiedEditor() : editor as monaco.editor.IStandaloneCodeEditor
    editable.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => callbacks.current.onSave())
    const configuration = editable.onDidChangeConfiguration(() => setTabMovesFocus(editable.getOption(monaco.editor.EditorOption.tabFocusMode)))
    setTabMovesFocus(false)
    instance.current = editor
    return () => { configuration.dispose(); editor.dispose(); instance.current = null }
  }, [compare, path, stackId])

  useEffect(() => {
    const model = models.current?.draft
    if (model && model.getValue() !== value) model.setValue(value)
  }, [value, compare, path, stackId])
  useEffect(() => {
    const model = models.current?.saved
    if (model && model.getValue() !== original) model.setValue(original)
  }, [original, compare, path, stackId])
  useEffect(() => { instance.current?.updateOptions({ readOnly: disabled }) }, [disabled, compare, path, stackId])
  useEffect(() => { monaco.editor.setTheme(theme === 'dark' ? 'vs-dark' : 'vs') }, [theme])
  useEffect(() => {
    if (!expanded) return
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setExpanded(false) }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [expanded])

  return <Stack gap="xs" className={expanded ? 'compose-editor compose-editor-expanded' : 'compose-editor'}>
    <Group justify="space-between"><Text size="sm" fw={600}>{compare ? 'Saved version ↔ Your draft' : 'Your draft'} · {path}</Text><Button size="xs" variant="default" onClick={() => setExpanded((current) => !current)}>{expanded ? 'Exit expanded view' : 'Expand editor'}</Button></Group>
    <div ref={host} className="compose-editor-surface" />
    <Text size="xs" c="dimmed">{tabMovesFocus ? 'Tab moves focus' : 'Tab indents by 2 spaces'} · Shift+Tab unindents · Ctrl/Cmd+S saves to Git · Ctrl+M (Mac: Ctrl+Shift+M) toggles Tab focus · F1 for commands</Text>
  </Stack>
}
