/** Advisory only: callers must preserve every character of the submitted password. */
export function passwordWhitespaceWarning(password: string): string | null {
  if (!password || !/\s/u.test(password)) return null
  if (password.trim() !== password) return 'Your password starts or ends with whitespace. It will be kept exactly, so include it when signing in.'
  return 'Your password contains spaces or other whitespace. They will be kept exactly and must be included when signing in.'
}
