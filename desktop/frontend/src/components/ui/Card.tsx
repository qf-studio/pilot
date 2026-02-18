import React from 'react'

interface CardProps {
  title: string
  children: React.ReactNode
  className?: string
}

export function Card({ title, children, className = '' }: CardProps) {
  return (
    <div
      className={`border border-border rounded bg-card flex flex-col ${className}`}
    >
      <div className="px-3 py-1.5 border-b border-border text-midgray uppercase tracking-wider text-[10px]">
        {title}
      </div>
      <div className="flex-1 px-3 py-2">{children}</div>
    </div>
  )
}
