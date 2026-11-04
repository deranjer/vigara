import { Alert } from '@mantine/core'
import { passwordWhitespaceWarning } from './password-utils'

export function PasswordWhitespaceWarning({ value }: { value: string }) {
  const warning = passwordWhitespaceWarning(value)
  return warning ? <Alert color="yellow" title="Whitespace in password" role="status" aria-live="polite">{warning}</Alert> : null
}
