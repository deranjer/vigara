import { useEffect, useRef } from 'react'
import { useComputedColorScheme } from '@mantine/core'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/editor/editor.all'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import 'monaco-editor/esm/vs/basic-languages/ini/ini.contribution'
import 'monaco-editor/esm/vs/basic-languages/shell/shell.contribution'
import 'monaco-editor/esm/vs/language/json/monaco.contribution'
import './monaco-workers'
import { fileLanguage } from './mount-file-utils'

monaco.languages.register({ id: 'caddyfile' })
monaco.languages.setMonarchTokensProvider('caddyfile', { tokenizer: { root: [
  [/#.*$/, 'comment'], [/"(?:[^"\\]|\\.)*"/, 'string'], [/[{}]/, 'delimiter.bracket'],
  [/\b(?:reverse_proxy|import|handle|handle_path|route|respond|redir|header|tls|encode|root|file_server|log|bind)\b/, 'keyword'],
  [/https?:\/\/[^\s{}]+/, 'string'], [/\b\d+(?:\.\d+){3}(?::\d+)?\b/, 'number'],
] } })
monaco.languages.json.jsonDefaults.setDiagnosticsOptions({ validate: true, enableSchemaRequest: false })

type Props = { name: string; value: string; original: string; compare: boolean; readOnly: boolean; onChange: (text: string) => void }

export default function MountTextEditor({ name, value, original, compare, readOnly, onChange }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const models = useRef<{ draft: monaco.editor.ITextModel; saved: monaco.editor.ITextModel } | null>(null)
  const instance = useRef<monaco.editor.IStandaloneCodeEditor | monaco.editor.IStandaloneDiffEditor | null>(null)
  const callback = useRef(onChange)
  const applying = useRef(false)
  callback.current = onChange
  const theme = useComputedColorScheme('light')

  useEffect(() => {
    if (!host.current) return
    const draft = monaco.editor.createModel('', fileLanguage(name))
    const saved = monaco.editor.createModel('', fileLanguage(name))
    models.current = { draft, saved }
    const options: monaco.editor.IStandaloneEditorConstructionOptions = {
      automaticLayout: true, minimap: { enabled: false }, fontSize: 14, scrollBeyondLastLine: false,
      wordWrap: 'on', tabSize: 2, ariaLabel: `Text file ${name}`, readOnly: true,
    }
    const editor = compare
      ? monaco.editor.createDiffEditor(host.current, { ...options, originalEditable: false, ignoreTrimWhitespace: false, renderSideBySide: true })
      : monaco.editor.create(host.current, { ...options, model: draft })
    if (compare) (editor as monaco.editor.IStandaloneDiffEditor).setModel({ original: saved, modified: draft })
    const listener = draft.onDidChangeContent(() => { if (!applying.current) callback.current(draft.getValue(monaco.editor.EndOfLinePreference.TextDefined, true)) })
    instance.current = editor
    return () => { listener.dispose(); editor.dispose(); draft.dispose(); saved.dispose(); models.current = null; instance.current = null }
  }, [name, compare])

  useEffect(() => {
    applying.current = true
    try { if (models.current?.draft.getValue(monaco.editor.EndOfLinePreference.TextDefined, true) !== value) models.current?.draft.setValue(value) }
    finally { applying.current = false }
  }, [value, name, compare])
  useEffect(() => { if (models.current?.saved.getValue(monaco.editor.EndOfLinePreference.TextDefined, true) !== original) models.current?.saved.setValue(original) }, [original, name, compare])
  useEffect(() => { instance.current?.updateOptions({ readOnly }) }, [readOnly, name, compare])
  useEffect(() => { monaco.editor.setTheme(theme === 'dark' ? 'vs-dark' : 'vs') }, [theme, name, compare])

  return <div ref={host} style={{ height: 'min(55vh, 560px)', minHeight: 260, border: '1px solid var(--mantine-color-default-border)' }} />
}
