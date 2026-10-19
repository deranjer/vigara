import { useCallback, useEffect, useRef, useState } from 'react'
import { isStreamEnd, parseRecords, streamUrl } from './container-utils'

export type StreamStatus = 'idle' | 'connecting' | 'live' | 'ended' | 'failed'
export type StreamState = { status: StreamStatus; message: string }

/**
 * Opens a same-origin WebSocket to `path` and hands each batch of newline-delimited JSON records to
 * `onRecords`. Passing null closes the stream. The socket is reopened whenever `path` changes or
 * `reconnect` is called, and is always closed when the component unmounts, so leaving the page
 * tells the server to stop the agent's stream.
 */
export function useStream<T>(path: string | null, onRecords: (records: T[]) => void) {
  const [state, setState] = useState<StreamState>({ status: 'idle', message: '' })
  const [attempt, setAttempt] = useState(0)
  const handler = useRef(onRecords)
  useEffect(() => { handler.current = onRecords })

  useEffect(() => {
    if (!path) return
    setState({ status: 'connecting', message: '' })
    let finished = false
    const socket = new WebSocket(streamUrl(path, window.location))
    socket.onopen = () => setState((current) => current.status === 'connecting' ? { status: 'live', message: '' } : current)
    socket.onmessage = (event) => {
      if (typeof event.data !== 'string') return
      const data: T[] = []
      for (const record of parseRecords<T>(event.data)) {
        if (isStreamEnd(record)) {
          finished = true
          setState({ status: 'ended', message: record.message ?? '' })
        } else data.push(record)
      }
      if (data.length > 0) handler.current(data)
    }
    socket.onerror = () => { /* onclose follows and reports the failure. */ }
    socket.onclose = () => {
      if (!finished) setState({ status: 'failed', message: 'The connection to the server was lost.' })
    }
    return () => { finished = true; socket.close() }
  }, [path, attempt])

  const reconnect = useCallback(() => setAttempt((value) => value + 1), [])
  return { ...state, reconnect }
}
