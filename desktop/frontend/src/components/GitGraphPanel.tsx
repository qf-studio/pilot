import React, { useRef, useEffect, useState } from 'react'
import { Card } from './ui/Card'
import type { GitGraphData, GitGraphLine } from '../types'

// Track colors matching TUI gitgraph.go palette
const TRACK_COLORS = ['#7eb8da', '#7ec699', '#d4a054', '#d48a8a', '#8b949e']

// Ref badge colors
const HEAD_COLOR = '#7eb8da'
const TAG_COLOR = '#d4a054'
const BRANCH_COLOR = '#7ec699'

// Graph characters that indicate track boundaries
const GRAPH_CHARS = new Set(['*', '●', '|', '│', '\\', '/', '╮', '╯', '╰', '╭', '─', '╌'])

interface GitGraphPanelProps {
  data: GitGraphData
}

function trackColorAt(col: number): string {
  const track = Math.floor(col / 2)
  return TRACK_COLORS[track % TRACK_COLORS.length]
}

/** Colorize graph characters by track position */
function renderGraphChars(chars: string): React.ReactNode[] {
  const nodes: React.ReactNode[] = []
  for (let i = 0; i < chars.length; i++) {
    const ch = chars[i]
    if (GRAPH_CHARS.has(ch)) {
      nodes.push(
        <span key={i} style={{ color: trackColorAt(i) }}>
          {ch}
        </span>
      )
    } else {
      nodes.push(<span key={i}>{ch}</span>)
    }
  }
  return nodes
}

/** Parse and colorize ref decorations */
function renderRefs(refs: string): React.ReactNode {
  if (!refs) return null

  // Strip outer parens if present: "(HEAD -> main, tag: v1.0)" → "HEAD -> main, tag: v1.0"
  let inner = refs.trim()
  if (inner.startsWith('(') && inner.endsWith(')')) {
    inner = inner.slice(1, -1)
  }

  const parts = inner.split(',').map((s) => s.trim()).filter(Boolean)
  const nodes: React.ReactNode[] = []

  for (let i = 0; i < parts.length; i++) {
    let part = parts[i]
    // Clean prefixes
    part = part.replace(/^refs\/remotes\//, '').replace(/^refs\/heads\//, '').replace(/^refs\//, '')

    if (i > 0) nodes.push(<span key={`sep-${i}`} className="text-gray">{', '}</span>)

    if (part.startsWith('HEAD')) {
      nodes.push(
        <span key={i} style={{ color: HEAD_COLOR, fontWeight: 'bold' }}>
          {part}
        </span>
      )
    } else if (part.startsWith('tag:')) {
      nodes.push(
        <span key={i} style={{ color: TAG_COLOR, fontWeight: 'bold' }}>
          {part}
        </span>
      )
    } else {
      nodes.push(
        <span key={i} style={{ color: BRANCH_COLOR }}>
          {part}
        </span>
      )
    }
  }

  return (
    <span>
      <span className="text-gray">{'('}</span>
      {nodes}
      <span className="text-gray">{')'}</span>
      {' '}
    </span>
  )
}

function GraphLine({ line }: { line: GitGraphLine }) {
  return (
    <div className="flex gap-0 leading-tight whitespace-pre py-px">
      <span className="shrink-0">{renderGraphChars(line.graph_chars)}</span>
      {line.refs && <span className="shrink-0 ml-1">{renderRefs(line.refs)}</span>}
      {line.message && <span className="text-lightgray ml-1 truncate">{line.message}</span>}
      {line.sha && <span className="text-gray ml-2 shrink-0">{line.sha}</span>}
      {line.author && <span className="text-midgray ml-2 shrink-0">{line.author}</span>}
    </div>
  )
}

export function GitGraphPanel({ data }: GitGraphPanelProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const [visibleEnd, setVisibleEnd] = useState(0)

  useEffect(() => {
    const el = scrollRef.current
    if (!el) return

    function updateCounter() {
      if (!el) return
      const lineH = 16 // approx line height at text-[11px]
      const end = Math.min(
        Math.ceil((el.scrollTop + el.clientHeight) / lineH),
        data.lines.length
      )
      setVisibleEnd(end)
    }

    updateCounter()
    el.addEventListener('scroll', updateCounter)
    return () => el.removeEventListener('scroll', updateCounter)
  }, [data.lines.length])

  return (
    <Card title="GIT GRAPH" className="flex-1 min-h-0">
      <div className="flex flex-col h-full min-h-0">
        <div ref={scrollRef} className="flex-1 overflow-y-auto log-scroll text-[11px] min-h-0">
          {data.lines.length === 0 ? (
            <div className="text-gray text-[10px]">
              {data.error ? data.error : 'no git graph data'}
            </div>
          ) : (
            data.lines.map((line, i) => <GraphLine key={i} line={line} />)
          )}
        </div>
        {data.total_count > 0 && (
          <div className="shrink-0 text-gray text-[10px] border-t border-border pt-0.5 mt-0.5">
            [{visibleEnd} of {data.total_count}]
          </div>
        )}
      </div>
    </Card>
  )
}
