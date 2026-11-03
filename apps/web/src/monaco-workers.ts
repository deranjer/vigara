import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'
import YamlWorker from 'monaco-yaml/yaml.worker?worker'

// Both editors share Monaco's environment, including when loaded in either order.
self.MonacoEnvironment = { getWorker: (_id, label) => label === 'yaml' ? new YamlWorker() : label === 'json' ? new JsonWorker() : new EditorWorker() }
