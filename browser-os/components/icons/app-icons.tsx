// ============================================================
// GPTAdmin Browser OS — App Icons (SVG)
// ============================================================

import React from 'react';

interface IconProps {
  size?: number;
  className?: string;
}

export function FilesIcon({ size = 48, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" className={className}>
      <defs>
        <linearGradient id="files-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#4FC3F7" />
          <stop offset="100%" stopColor="#1565C0" />
        </linearGradient>
      </defs>
      <rect x="8" y="4" width="32" height="40" rx="4" fill="url(#files-grad)" />
      <rect x="14" y="14" width="20" height="2" rx="1" fill="rgba(255,255,255,0.7)" />
      <rect x="14" y="20" width="16" height="2" rx="1" fill="rgba(255,255,255,0.5)" />
      <rect x="14" y="26" width="18" height="2" rx="1" fill="rgba(255,255,255,0.5)" />
      <rect x="14" y="32" width="12" height="2" rx="1" fill="rgba(255,255,255,0.3)" />
      <path d="M30 4 L30 12 Q30 14 32 14 L40 14" fill="rgba(255,255,255,0.2)" />
    </svg>
  );
}

export function TerminalIcon({ size = 48, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" className={className}>
      <defs>
        <linearGradient id="term-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#37474F" />
          <stop offset="100%" stopColor="#1a1a2e" />
        </linearGradient>
      </defs>
      <rect x="4" y="8" width="40" height="32" rx="6" fill="url(#term-grad)" />
      <rect x="4" y="8" width="40" height="10" rx="6" fill="rgba(255,255,255,0.08)" />
      <circle cx="12" cy="13" r="1.5" fill="#ef476f" />
      <circle cx="17" cy="13" r="1.5" fill="#ffd166" />
      <circle cx="22" cy="13" r="1.5" fill="#06d6a0" />
      <text x="14" y="30" fontFamily="monospace" fontSize="11" fontWeight="bold" fill="#06d6a0">&gt;_</text>
    </svg>
  );
}

export function OpenCodeIcon({ size = 48, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" className={className}>
      <defs>
        <linearGradient id="code-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#4361ee" />
          <stop offset="100%" stopColor="#3a0ca3" />
        </linearGradient>
      </defs>
      <rect x="4" y="4" width="40" height="40" rx="8" fill="url(#code-grad)" />
      <path d="M16 16 L10 24 L16 32" stroke="white" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <path d="M32 16 L38 24 L32 32" stroke="white" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <line x1="27" y1="14" x2="21" y2="34" stroke="rgba(255,255,255,0.7)" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

export function ComputersIcon({ size = 48, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" className={className}>
      <defs>
        <linearGradient id="comp-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#7c4dff" />
          <stop offset="100%" stopColor="#4a148c" />
        </linearGradient>
      </defs>
      <rect x="6" y="6" width="36" height="24" rx="3" fill="url(#comp-grad)" />
      <rect x="8" y="8" width="32" height="18" rx="1" fill="rgba(0,0,0,0.3)" />
      <rect x="18" y="30" width="12" height="4" rx="1" fill="rgba(124,77,255,0.8)" />
      <rect x="14" y="34" width="20" height="3" rx="1.5" fill="rgba(124,77,255,0.6)" />
      <circle cx="24" cy="17" r="4" fill="none" stroke="rgba(255,255,255,0.6)" strokeWidth="1.5" />
      <circle cx="24" cy="17" r="1.5" fill="rgba(255,255,255,0.6)" />
      <line x1="24" y1="12" x2="24" y2="14" stroke="rgba(255,255,255,0.4)" strokeWidth="1" />
      <line x1="24" y1="20" x2="24" y2="22" stroke="rgba(255,255,255,0.4)" strokeWidth="1" />
      <line x1="19" y1="17" x2="21" y2="17" stroke="rgba(255,255,255,0.4)" strokeWidth="1" />
      <line x1="27" y1="17" x2="29" y2="17" stroke="rgba(255,255,255,0.4)" strokeWidth="1" />
    </svg>
  );
}

export function SettingsIcon({ size = 48, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48" className={className}>
      <defs>
        <linearGradient id="set-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#78909C" />
          <stop offset="100%" stopColor="#455A64" />
        </linearGradient>
      </defs>
      <path
        d="M24 16 A8 8 0 1 0 24 32 A8 8 0 1 0 24 16 Z"
        fill="url(#set-grad)"
      />
      <circle cx="24" cy="24" r="5" fill="#546E7A" />
      <circle cx="24" cy="24" r="2" fill="rgba(255,255,255,0.4)" />
      {[0, 45, 90, 135, 180, 225, 270, 315].map((angle, i) => {
        const rad = (angle * Math.PI) / 180;
        const x1 = 24 + Math.cos(rad) * 10;
        const y1 = 24 + Math.sin(rad) * 10;
        const x2 = 24 + Math.cos(rad) * 14;
        const y2 = 24 + Math.sin(rad) * 14;
        return (
          <line
            key={i}
            x1={x1} y1={y1} x2={x2} y2={y2}
            stroke="url(#set-grad)"
            strokeWidth="4"
            strokeLinecap="round"
          />
        );
      })}
    </svg>
  );
}

export function CloudOSIcon({ size = 18, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={className}>
      <defs>
        <linearGradient id="cloud-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="white" />
          <stop offset="100%" stopColor="rgba(255,255,255,0.7)" />
        </linearGradient>
      </defs>
      <path
        d="M6 18 C3.8 18 2 16.2 2 14 C2 12 3.4 10.4 5.3 10.1 C5.1 9.4 5 8.7 5 8 C5 4.7 7.7 2 11 2 C13.5 2 15.7 3.6 16.6 5.8 C17.1 5.6 17.5 5.5 18 5.5 C20.5 5.5 22.5 7.5 22.5 10 C22.5 10.4 22.4 10.7 22.4 11 C23.3 11.8 24 12.8 24 14 C24 16.2 22.2 18 20 18 L6 18 Z"
        fill="url(#cloud-grad)"
      />
    </svg>
  );
}

export function FolderIcon({ size = 20, className = '', open = false }: IconProps & { open?: boolean }) {
  if (open) {
    return (
      <svg width={size} height={size} viewBox="0 0 20 20" className={className}>
        <path d="M2 5 L2 16 Q2 17 3 17 L17 17 Q18 17 18 16 L18 7 Q18 6 17 6 L10 6 L8 4 L3 4 Q2 4 2 5 Z" fill="#4FC3F7" />
        <path d="M2 8 L18 8 L17 17 L3 17 Z" fill="#81D4FA" />
      </svg>
    );
  }
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" className={className}>
      <path d="M2 5 L2 16 Q2 17 3 17 L17 17 Q18 17 18 16 L18 7 Q18 6 17 6 L10 6 L8 4 L3 4 Q2 4 2 5 Z" fill="#4FC3F7" />
    </svg>
  );
}

export function FileTextIcon({ size = 20, className = '' }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" className={className}>
      <rect x="4" y="2" width="12" height="16" rx="2" fill="#E8EAF6" />
      <rect x="7" y="6" width="6" height="1" rx="0.5" fill="#9FA8DA" />
      <rect x="7" y="9" width="6" height="1" rx="0.5" fill="#9FA8DA" />
      <rect x="7" y="12" width="4" height="1" rx="0.5" fill="#9FA8DA" />
    </svg>
  );
}

export const appIconMap: Record<string, React.FC<IconProps>> = {
  files: FilesIcon,
  terminal: TerminalIcon,
  opencode: OpenCodeIcon,
  computers: ComputersIcon,
  settings: SettingsIcon,
};
