import { useState, useEffect } from 'react'
import type { GitGraphData } from '../types'
import { api } from '../provider'

const POLL_INTERVAL_MS = 15_000

const defaultData: GitGraphData = {
  lines: [],
  total_count: 0,
  last_refresh: '',
}

export function useGitGraph(limit = 100): GitGraphData {
  const [data, setData] = useState<GitGraphData>(defaultData)

  useEffect(() => {
    async function poll() {
      try {
        const d = await api.GetGitGraph(limit)
        if (d) setData(d)
      } catch {
        // Graceful degradation — keep previous values
      }
    }

    poll()
    const id = setInterval(poll, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [limit])

  return data
}
