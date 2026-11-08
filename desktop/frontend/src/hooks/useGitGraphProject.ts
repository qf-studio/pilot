import { useState, useEffect, useCallback } from 'react'
import type { GitGraphProject } from '../types'
import { api } from '../provider'

const STORAGE_KEY = 'pilot.gitgraph.project'

export interface GitGraphProjectState {
  /** Configured projects, default first as served; empty when the list call failed. */
  projects: GitGraphProject[]
  /** Selected project name; '' means use the legacy single-project GetGitGraph; null while loading. */
  selected: string | null
  select: (name: string) => void
}

function readStored(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

function writeStored(name: string) {
  try {
    window.localStorage.setItem(STORAGE_KEY, name)
  } catch {
    // Storage unavailable — selection just won't survive a reload
  }
}

function resolveSelection(projects: GitGraphProject[]): string {
  const stored = readStored()
  if (stored && projects.some((p) => p.name === stored)) return stored
  return (projects.find((p) => p.default) ?? projects[0]).name
}

export function useGitGraphProject(): GitGraphProjectState {
  const [projects, setProjects] = useState<GitGraphProject[]>([])
  const [selected, setSelected] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .ListGitGraphProjects()
      .then((list) => {
        if (cancelled) return
        if (Array.isArray(list) && list.length > 0) {
          setProjects(list)
          setSelected(resolveSelection(list))
        } else {
          setSelected('')
        }
      })
      .catch(() => {
        // Older daemon without the list endpoint — no tabs, legacy graph call
        if (!cancelled) setSelected('')
      })
    return () => {
      cancelled = true
    }
  }, [])

  const select = useCallback((name: string) => {
    setSelected(name)
    writeStored(name)
  }, [])

  return { projects, selected, select }
}
