import React from 'react'

interface CardProps {
  title: string
  children: React.ReactNode
  className?: string
}

export function Card({ title, children, className = '' }: CardProps) {
  return (
    <div
      className={`border border-border rounded bg-card flex flex-col overflow-hidden ${className}`}
    >
      <div className="px-2 py-1 border-b border-border text-midgray uppercase tracking-wider text-[10px] shrink-0">
        {title}
      </div>
      <div className="flex-1 px-2 py-1.5 min-h-0 overflow-hidden">{children}</div>
    </div>
  )
}
