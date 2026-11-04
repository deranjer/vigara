import { describe, expect, it } from 'vitest'
import { passwordWhitespaceWarning } from './password-utils'

describe('password whitespace warning', () => {
  it('does not warn for an empty password or one without whitespace', () => {
    expect(passwordWhitespaceWarning('')).toBeNull()
    expect(passwordWhitespaceWarning('correct-password-123')).toBeNull()
  })
  it('warns about leading or trailing whitespace, including Unicode whitespace', () => {
    for (const password of [' leading-password', 'trailing-password ', '\tpassword-value', 'password-value\n', '\u00a0password-value\u00a0']) {
      expect(passwordWhitespaceWarning(password)).toContain('starts or ends')
      expect(passwordWhitespaceWarning(password)).toContain('kept exactly')
    }
  })
  it('explains that whitespace inside a passphrase is significant', () => {
    expect(passwordWhitespaceWarning('correct horse battery staple')).toContain('must be included')
    expect(passwordWhitespaceWarning('internal\ttab-password')).toContain('kept exactly')
  })
})
