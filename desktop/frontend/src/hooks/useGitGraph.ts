import { useState, useEffect } from 'react'
import type { GitGraphData } from '../types'
import { api } from '../provider'

const POLL_INTERVAL_MS = 15_000

const defaultData: GitGraphData = {
  lines: [],
  total_count: 0,
  last_refresh: '',
}

/**
 * Polls the git graph of one project. `project` is the selected project name,
 * '' for the daemon's default project (legacy GetGitGraph), or null while the
 * selection is still loading (nothing is fetched).
 */
export function useGitGraph(project: string | null, limit = 100): GitGraphData {
  const [data, setData] = useState<GitGraphData>(defaultData)

  useEffect(() => {
    if (project === null) return

    // Decision: a response for a project that is no longer selected is dropped
    // via this flag, set by the cleanup that runs on every project change.
    let cancelled = false

    async function poll() {
      try {
        const d = project
          ? await api.GetGitGraphForProject(project, limit)
          : await api.GetGitGraph(limit)
        if (d && !cancelled) setData(d)
      } catch {
        // Graceful degradation — keep previous values
      }
    }

    // Do not show the previous project's graph under the new tab while loading.
    setData(defaultData)
    poll()
    const id = setInterval(poll, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [project, limit])

  return data
}
