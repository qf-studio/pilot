import React from 'react'

interface SparklineProps {
  data: number[]
  color?: string // hex color for bars
  width?: number
  height?: number
  showDot?: boolean
}

// Sparkline renders a 7-bar SVG sparkline chart.
// Heights are normalized to 1-8 scale; 0 values render at minimum height.
export function Sparkline({
  data,
  color = '#7eb8da',
  width = 120,
  height = 24,
  showDot = true,
}: SparklineProps) {
  const bars = data.slice(-7)
  while (bars.length < 7) bars.unshift(0)

  const max = Math.max(...bars, 1)
  const barWidth = Math.floor(width / 7) - 1
  const gap = 1

  const normalizedHeights = bars.map((v) => {
    if (v === 0) return 2 // minimum visible height
    return Math.max(2, Math.round((v / max) * (height - 4)))
  })

  const lastDotX = (barWidth + gap) * 6 + barWidth / 2
  const lastH = normalizedHeights[6]
  const lastDotY = height - lastH - 2

  return (
    <svg width={width} height={height} style={{ display: 'block' }}>
      {bars.map((_, i) => {
        const x = i * (barWidth + gap)
        const h = normalizedHeights[i]
        const y = height - h
        const isLast = i === 6
        const barColor = isLast ? color : `${color}88`
        return (
          <rect
            key={i}
            x={x}
            y={y}
            width={barWidth}
            height={h}
            fill={barColor}
            rx={1}
          />
        )
      })}
      {showDot && (
        <circle
          cx={lastDotX}
          cy={lastDotY}
          r={2.5}
          fill={color}
          className="pulse"
        />
      )}
    </svg>
  )
}
